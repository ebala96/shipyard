package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/datadir"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/pipeline"
	"github.com/shipyard/shipyard/pkg/scheduler"
	shipfilelib "github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

// DeployRequest is the JSON body for deploy and redeploy endpoints.
type DeployRequest struct {
	Platform  string `json:"platform"`
	File      string `json:"file"`
	StackName string `json:"stackName"`
	Mode      string `json:"mode"`
}

// DeployHandler handles service deployment operations.
type DeployHandler struct {
	orch       *orchestrator.Orchestrator
	svcHandler *ServiceHandler
	store      *store.Store
	bus        *telemetry.Bus
	scheduler  *scheduler.Scheduler
}

// NewDeployHandler creates a DeployHandler.
func NewDeployHandler(orch *orchestrator.Orchestrator, st *store.Store, bus *telemetry.Bus, sched *scheduler.Scheduler) *DeployHandler {
	return &DeployHandler{orch: orch, store: st, bus: bus, scheduler: sched}
}

// SetServiceHandler wires the service registry into the deploy handler.
func (h *DeployHandler) SetServiceHandler(svc *ServiceHandler) {
	h.svcHandler = svc
}

// Deploy handles POST /api/v1/services/:name/deploy
func (h *DeployHandler) Deploy(c *gin.Context) {
	serviceName := c.Param("name")

	var req DeployRequest
	// ShouldBindJSON is lenient — no required fields.
	c.ShouldBindJSON(&req)

	if h.svcHandler == nil {
		c.JSON(http.StatusInternalServerError, errorResponse("service registry not initialised"))
		return
	}

	record, ok := h.svcHandler.GetRecord(serviceName)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found — onboard it first", serviceName)))
		return
	}

	sf, err := loadShipfile(serviceName, record.ContextDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	// Apply platform and file overrides from the request.
	if req.Platform != "" {
		sf.Service.Engine.Type = shipfilelib.EngineType(req.Platform)
	}
	if req.File != "" {
		applyFileOverride(sf, req.File)
	}

	// Resolve mode — use request value, fall back to "production", then first available.
	mode := resolveMode(sf, req.Mode)

	// ── Stage 1-6: IaC pipeline ─────────────────────────────────────────────
	pipe := pipeline.New(h.store)
	pipeResult, pipeErr := pipe.Run(c.Request.Context(), pipeline.Request{
		ServiceName: serviceName,
		StackName:   req.StackName,
		ContextDir:  record.ContextDir,
		Mode:        mode,
		Shipfile:    sf,
		Operator:    "user",
	})
	if pipeErr != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("pipeline failed: %v", pipeErr)))
		return
	}

	// No change detected — skip Docker deploy.
	if pipeResult.Plan != nil && pipeResult.Plan.NoChange() {
		c.JSON(http.StatusOK, gin.H{
			"message": "no changes detected — already up to date",
			"plan":    pipeResult.Plan.Summary(),
		})
		return
	}

	// Log policy warnings.
	if pipeResult.PolicyReport != nil {
		for _, w := range pipeResult.PolicyReport.Warnings {
			fmt.Printf("policy warning [%s]: %s\n", serviceName, w.Message)
		}
	}

	// ── Scheduler: pick the best node ────────────────────────────────────────
	targetNode := ""
	if h.scheduler != nil {
		result, err := h.scheduler.Place(c.Request.Context(), scheduler.Request{
			ServiceName: serviceName,
			Mode:        mode,
		})
		if err == nil && result != nil {
			targetNode = result.NodeHostname
			fmt.Printf("scheduler: placed %q on node %q\n", serviceName, targetNode)
		}
	}

	// ── Stage 7: Deploy ──────────────────────────────────────────────────────
	deployed, err := h.orch.Deploy(c.Request.Context(), orchestrator.DeployRequest{
		ServiceName: serviceName,
		Mode:        mode,
		ContextDir:  record.ContextDir,
		TargetNode:  targetNode,
		Shipfile:    sf,
		StackName:   req.StackName,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("deploy failed: %v", err)))
		return
	}

	// Write state to etcd + publish NATS event.
	h.recordDeploy(serviceName, targetNode, deployed)

	c.JSON(http.StatusCreated, gin.H{
		"message":   "service deployed successfully",
		"container": deployed,
		"plan":      pipeResult.Plan.Summary(),
	})
}

// Redeploy handles POST /api/v1/services/:name/redeploy
func (h *DeployHandler) Redeploy(c *gin.Context) {
	serviceName := c.Param("name")

	var req DeployRequest
	c.ShouldBindJSON(&req)

	if h.svcHandler == nil {
		c.JSON(http.StatusInternalServerError, errorResponse("service registry not initialised"))
		return
	}

	record, ok := h.svcHandler.GetRecord(serviceName)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", serviceName)))
		return
	}

	sf, err := loadShipfile(serviceName, record.ContextDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	if req.Platform != "" {
		sf.Service.Engine.Type = shipfilelib.EngineType(req.Platform)
	}
	if req.File != "" {
		applyFileOverride(sf, req.File)
	}

	mode := resolveMode(sf, req.Mode)

	deployed, err := h.orch.Deploy(c.Request.Context(), orchestrator.DeployRequest{
		ServiceName: serviceName,
		Mode:        mode,
		ContextDir:  record.ContextDir,
		Shipfile:    sf,
		StackName:   req.StackName,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("redeploy failed: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   "service redeployed successfully",
		"container": deployed,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// loadShipfile finds and parses the shipfile for a service.
func loadShipfile(serviceName, contextDir string) (*shipfilelib.Shipfile, error) {
	candidates := []string{
		datadir.ServiceShipfilePath(serviceName),
		contextDir + "/shipfile.yml",
		filepath.Join(contextDir, "..", "shipfile.yml"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return shipfilelib.Parse(p)
		}
	}
	return nil, fmt.Errorf("shipfile.yml not found for service %q", serviceName)
}

// resolveMode returns the mode to deploy in.
// Priority: request value → "production" → first available mode.
func resolveMode(sf *shipfilelib.Shipfile, requested string) string {
	if requested != "" && sf.HasMode(requested) {
		return requested
	}
	if sf.HasMode("production") {
		return "production"
	}
	for m := range sf.Service.Modes {
		return m
	}
	return "production"
}

// applyFileOverride sets the appropriate build file in all modes based on
// what file the user selected in the Deploy tab.
func applyFileOverride(sf *shipfilelib.Shipfile, file string) {
	for name, mode := range sf.Service.Modes {
		switch {
		case isComposeFile(file):
			mode.Build.ComposeFile = file
			mode.Build.Dockerfile = ""
		case isK8sDir(file):
			mode.Build.ManifestDir = file
			mode.Build.Dockerfile = ""
		case isNomadFile(file):
			mode.Build.NomadJob = file
			mode.Build.Dockerfile = ""
		default:
			mode.Build.Dockerfile = file
			mode.Build.ComposeFile = ""
		}
		sf.Service.Modes[name] = mode
	}
}

func isComposeFile(f string) bool {
	return f == "compose.yaml" || f == "compose.yml" ||
		f == "docker-compose.yml" || f == "docker-compose.yaml"
}

func isK8sDir(f string) bool {
	return f == "k8s" || f == "kubernetes" || f == "manifests" || f == "deploy" || f == "helm"
}

func isNomadFile(f string) bool {
	return len(f) > 6 && f[len(f)-6:] == ".nomad"
}

// ScaleRequest is the JSON body for the scale endpoint.
type ScaleRequest struct {
	Instances int    `json:"instances" binding:"required,min=1"`
	Mode      string `json:"mode"`
	StackName string `json:"stackName"`
}

// Scale handles POST /api/v1/services/:name/scale
// Adjusts the number of running instances without rebuilding.
func (h *DeployHandler) Scale(c *gin.Context) {
	serviceName := c.Param("name")

	var req ScaleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid request: %v", err)))
		return
	}

	record, ok := h.svcHandler.GetRecord(serviceName)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", serviceName)))
		return
	}

	sf, err := loadShipfile(serviceName, record.ContextDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = "production"
	}

	resolved, err := shipfilelib.Resolve(sf, mode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("resolve failed: %v", err)))
		return
	}

	// Get current running count.
	current, err := h.orch.CountInstances(c.Request.Context(), serviceName, mode, req.StackName)
	if err != nil {
		current = 0
	}

	target := req.Instances
	var added, removed []string

	if target > current {
		for i := 0; i < target-current; i++ {
			deployed, err := h.orch.ScaleUp(c.Request.Context(), serviceName, mode, req.StackName, resolved)
			if err != nil {
				c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("scale up failed: %v", err)))
				return
			}
			added = append(added, deployed.ContainerID)
		}
	} else if target < current {
		for i := 0; i < current-target; i++ {
			if err := h.orch.ScaleDown(c.Request.Context(), serviceName, mode, req.StackName); err != nil {
				c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("scale down failed: %v", err)))
				return
			}
			removed = append(removed, "1 instance")
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  fmt.Sprintf("scaled %q to %d instance(s)", serviceName, target),
		"added":    added,
		"removed":  removed,
		"current":  target,
	})
}

// ── Phase 1: etcd + NATS post-deploy hooks ────────────────────────────────

// recordDeploy writes the deployed container state to etcd and publishes
// a deploy event to NATS. Both are non-fatal — if etcd or NATS are not
// available the deploy still succeeds.
func (h *DeployHandler) recordDeploy(serviceName, node string, deployed *orchestrator.DeployedService) {
	if deployed == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Write stack state to etcd.
	if h.store != nil {
		containers := []store.ContainerRecord{
			{
				ContainerID:   deployed.ContainerID,
				ContainerName: deployed.ContainerName,
				ServiceName:   deployed.ServiceName,
				Mode:          deployed.Mode,
				Status:        "running",
				Image:         deployed.ImageTag,
				Ports:         deployed.Ports,
				CreatedAt:     time.Now(),
			},
		}

		if deployed.IDE != nil {
			containers[0].IDEInstance = &store.IDERecord{
				ContainerID:   deployed.IDE.ContainerID,
				ContainerName: deployed.IDE.ContainerName,
				HostPort:      deployed.IDE.HostPort,
				DirectURL:     deployed.IDE.URL,
			}
		}

		if deployed.VNC != nil {
			containers[0].VNCInstance = &store.VNCRecord{
				ContainerID:   deployed.VNC.ContainerID,
				ContainerName: deployed.VNC.ContainerName,
				HostPort:      deployed.VNC.HostPort,
				URL:           deployed.VNC.URL,
			}
		}

		stackName := serviceName
		if deployed.StackName != "" {
			stackName = deployed.StackName + "/" + serviceName
		}

		state := &store.StackState{
			Name:        stackName,
			ServiceName: serviceName,
			Platform:    deployed.Mode,
			Mode:        deployed.Mode,
			StackName:   deployed.StackName,
			Node:        node,
			State:       store.StateRunning,
			StateAt:     time.Now(),
			Containers:  containers,
		}
		if err := h.store.PutStackState(ctx, state); err != nil {
			fmt.Printf("deploy: etcd state write failed for %q (non-fatal): %v\n", serviceName, err)
		}

		// Write ledger entry for rollback support.
		entry := &store.LedgerEntry{
			Name:      stackName,
			State:     state,
			Containers: containers,
			Operation: "deploy",
			Operator:  "user",
		}
		if err := h.store.WriteLedgerEntry(ctx, entry); err != nil {
			fmt.Printf("deploy: etcd ledger write failed for %q (non-fatal): %v\n", serviceName, err)
		}
	}

	// Publish deploy event to NATS.
	if h.bus != nil {
		event := telemetry.Event{
			Type:        "deploy",
			ServiceName: serviceName,
			StackName:   deployed.StackName,
			ContainerID: deployed.ContainerID,
			Mode:        deployed.Mode,
			Status:      "success",
			Operator:    "user",
		}
		if err := h.bus.PublishEvent(ctx, event); err != nil {
			fmt.Printf("deploy: NATS publish failed for %q (non-fatal): %v\n", serviceName, err)
		}
	}
}
