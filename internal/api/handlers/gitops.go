package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/gitops"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/pipeline"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

// GitOpsHandler handles GitOps configuration and webhook endpoints.
type GitOpsHandler struct {
	manager *gitops.Manager
	orch    *orchestrator.Orchestrator
	store   *store.Store
	bus     *telemetry.Bus
}

// NewGitOpsHandler creates a GitOpsHandler.
func NewGitOpsHandler(st *store.Store, orch *orchestrator.Orchestrator, bus *telemetry.Bus) *GitOpsHandler {
	return &GitOpsHandler{
		manager: gitops.NewManager(st),
		orch:    orch,
		store:   st,
		bus:     bus,
	}
}

// Configure handles PUT /api/v1/gitops/:name
// Sets up GitOps for a service.
func (h *GitOpsHandler) Configure(c *gin.Context) {
	name := c.Param("name")
	var req struct {
		Branch        string `json:"branch"`
		WebhookSecret string `json:"webhookSecret"`
		AutoDeploy    bool   `json:"autoDeploy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	// Get the service's repo URL from its record.
	record, ok := h.getServiceRecord(c, name)
	if !ok {
		return
	}

	cfg := &gitops.SyncConfig{
		ServiceName:   name,
		RepoURL:       record.RepoURL,
		Branch:        req.Branch,
		WebhookSecret: req.WebhookSecret,
		AutoDeploy:    req.AutoDeploy,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.manager.Configure(ctx, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	webhookURL := fmt.Sprintf("%s/api/v1/gitops/%s/webhook", c.Request.Host, name)
	c.JSON(http.StatusOK, gin.H{
		"message":    fmt.Sprintf("GitOps configured for %q (branch: %s)", name, cfg.Branch),
		"webhookURL": webhookURL,
		"config":     cfg,
	})
}

// Get handles GET /api/v1/gitops/:name
func (h *GitOpsHandler) Get(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := h.manager.Get(ctx, c.Param("name"))
	if err != nil || cfg == nil {
		c.JSON(http.StatusNotFound, errorResponse("GitOps not configured for this service"))
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// List handles GET /api/v1/gitops
func (h *GitOpsHandler) List(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	configs, err := h.manager.List(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}
	if configs == nil {
		configs = []*gitops.SyncConfig{}
	}
	c.JSON(http.StatusOK, gin.H{"configs": configs, "count": len(configs)})
}

// Delete handles DELETE /api/v1/gitops/:name
func (h *GitOpsHandler) Delete(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.manager.Delete(ctx, c.Param("name")); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "GitOps disabled for " + c.Param("name")})
}

// Webhook handles POST /api/v1/gitops/:name/webhook
// GitHub calls this when a push happens.
func (h *GitOpsHandler) Webhook(c *gin.Context) {
	name := c.Param("name")

	// Read body for HMAC validation.
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("failed to read body"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Get GitOps config.
	cfg, err := h.manager.Get(ctx, name)
	if err != nil || cfg == nil {
		c.JSON(http.StatusNotFound, errorResponse("GitOps not configured for this service"))
		return
	}

	// Validate webhook signature.
	sig := c.GetHeader("X-Hub-Signature-256")
	if !gitops.ValidateWebhookSignature(cfg.WebhookSecret, sig, body) {
		c.JSON(http.StatusUnauthorized, errorResponse("invalid webhook signature"))
		return
	}

	// Check it's a push event.
	event := c.GetHeader("X-GitHub-Event")
	if event != "push" {
		c.JSON(http.StatusOK, gin.H{"message": "ignored — not a push event"})
		return
	}

	// Extract branch and SHA from payload.
	branch := gitops.ExtractPushBranch(body)
	headSHA := gitops.ExtractHeadSHA(body)

	// Only deploy if it's the tracked branch.
	if branch != cfg.Branch {
		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("ignored — push was to %q, tracking %q", branch, cfg.Branch),
		})
		return
	}

	// Respond immediately — deploy happens async.
	c.JSON(http.StatusAccepted, gin.H{
		"message":    fmt.Sprintf("webhook received for %q — deploying SHA %s", name, headSHA[:8]),
		"sha":        headSHA,
		"autoDeploy": cfg.AutoDeploy,
	})

	// Async deploy.
	if cfg.AutoDeploy {
		go h.syncAndDeploy(name, headSHA)
	}
}

// Sync handles POST /api/v1/gitops/:name/sync
// Manual trigger — pull latest and deploy.
func (h *GitOpsHandler) Sync(c *gin.Context) {
	name := c.Param("name")

	record, ok := h.getServiceRecord(c, name)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	status, err := h.manager.Sync(ctx, name, record.ContextDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	if !status.Changed {
		c.JSON(http.StatusOK, gin.H{
			"message": fmt.Sprintf("%q is already at the latest commit (%s)", name, status.CurrentSHA[:8]),
			"status":  status,
		})
		return
	}

	// Deploy since it changed.
	go h.syncAndDeploy(name, status.CurrentSHA)

	c.JSON(http.StatusAccepted, gin.H{
		"message": fmt.Sprintf("new commit %s detected — deploying %q", status.CurrentSHA[:8], name),
		"status":  status,
	})
}

// syncAndDeploy pulls latest code and redeploys a service.
func (h *GitOpsHandler) syncAndDeploy(serviceName, sha string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	record, err := h.store.GetService(ctx, serviceName)
	if err != nil || record == nil {
		log.Printf("gitops: service %q not found: %v", serviceName, err)
		return
	}

	// Pull latest.
	status, err := h.manager.Sync(ctx, serviceName, record.ContextDir)
	if err != nil {
		log.Printf("gitops: sync failed for %q: %v", serviceName, err)
		h.publishEvent(ctx, serviceName, "gitops_sync_failed", sha, err.Error())
		return
	}

	// Load and parse shipfile.
	sf, err := loadShipfile(serviceName, record.ContextDir)
	if err != nil {
		log.Printf("gitops: failed to load config for %q: %v", serviceName, err)
		return
	}

	// Run through the IaC pipeline.
	mode := "production"
	if len(record.Modes) > 0 {
		mode = record.Modes[0]
	}

	pipe := pipeline.New(h.store)
	_, err = pipe.Run(ctx, pipeline.Request{
		ServiceName: serviceName,
		StackName:   serviceName,
		Mode:        mode,
		ContextDir:  record.ContextDir,
		Shipfile:    sf,
		Operator:    "gitops",
	})
	if err != nil {
		log.Printf("gitops: pipeline failed for %q: %v", serviceName, err)
		h.publishEvent(ctx, serviceName, "gitops_pipeline_failed", sha, err.Error())
		return
	}

	// Deploy.
	deployed, err := h.orch.Deploy(ctx, orchestrator.DeployRequest{
		ServiceName: serviceName,
		StackName:   serviceName,
		Mode:        mode,
		ContextDir:  record.ContextDir,
		Shipfile:    sf,
	})
	if err != nil {
		log.Printf("gitops: deploy failed for %q: %v", serviceName, err)
		h.publishEvent(ctx, serviceName, "gitops_deploy_failed", sha, err.Error())
		return
	}

	log.Printf("gitops: ✅ deployed %q at SHA %s → container %s",
		serviceName, status.CurrentSHA[:8], deployed.ContainerID[:12])

	h.publishEvent(ctx, serviceName, "gitops_deployed", sha, "")
}

// publishEvent sends a NATS event for GitOps activity.
func (h *GitOpsHandler) publishEvent(ctx context.Context, service, eventType, sha, errMsg string) {
	if h.bus == nil {
		return
	}
	ev := telemetry.Event{
		Type:        eventType,
		ServiceName: service,
		Status:      "success",
		Operator:    "gitops",
	}
	if errMsg != "" {
		ev.Status = "failed"
	}
	_ = h.bus.PublishEvent(ctx, ev)
}

// getServiceRecord is a helper that fetches the service record and handles errors.
func (h *GitOpsHandler) getServiceRecord(c *gin.Context, name string) (*store.ServiceRecord, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	record, err := h.store.GetService(ctx, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return nil, false
	}
	if record == nil {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found — onboard it first", name)))
		return nil, false
	}
	return record, true
}
