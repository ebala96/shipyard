package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
	"github.com/shipyard/shipyard/pkg/templates"
)

// TemplatesHandler serves the built-in service template catalog.
type TemplatesHandler struct {
	orch  *orchestrator.Orchestrator
	store *store.Store
	bus   *telemetry.Bus
}

// NewTemplatesHandler creates a TemplatesHandler.
func NewTemplatesHandler(orch *orchestrator.Orchestrator, st *store.Store, bus *telemetry.Bus) *TemplatesHandler {
	return &TemplatesHandler{orch: orch, store: st, bus: bus}
}

// List handles GET /api/v1/templates
// Returns all built-in templates, optionally filtered by ?category= or ?q=
func (h *TemplatesHandler) List(c *gin.Context) {
	var tmplList []*templates.Template

	if cat := c.Query("category"); cat != "" {
		tmplList = templates.ByCategory(templates.Category(cat))
	} else if q := c.Query("q"); q != "" {
		tmplList = templates.Search(q)
	} else {
		tmplList = templates.All()
	}

	// Group by category for the UI.
	grouped := make(map[string][]*templates.Template)
	for _, t := range tmplList {
		cat := string(t.Category)
		grouped[cat] = append(grouped[cat], t)
	}

	c.JSON(http.StatusOK, gin.H{
		"templates": tmplList,
		"grouped":   grouped,
		"count":     len(tmplList),
	})
}

// Get handles GET /api/v1/templates/:id
func (h *TemplatesHandler) Get(c *gin.Context) {
	t := templates.Get(c.Param("id"))
	if t == nil {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("template %q not found", c.Param("id"))))
		return
	}
	c.JSON(http.StatusOK, t)
}

// Deploy handles POST /api/v1/templates/:id/deploy
// Instantiates and deploys a built-in template.
func (h *TemplatesHandler) Deploy(c *gin.Context) {
	id := c.Param("id")

	t := templates.Get(id)
	if t == nil {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("template %q not found", id)))
		return
	}

	var req struct {
		Name       string            `json:"name"`       // custom service name (default: template ID)
		Parameters map[string]string `json:"parameters"` // user-supplied values
		Profile    string            `json:"profile"`    // power profile override
	}
	c.ShouldBindJSON(&req)

	if req.Name == "" {
		req.Name = id
	}

	// Build the shipfile with user params.
	sf := t.BuildManifest(req.Parameters)
	sf.Service.Name = req.Name

	// Deploy directly via orchestrator — no local clone needed for image-based templates.
	deployCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deployed, err := h.orch.Deploy(deployCtx, orchestrator.DeployRequest{
		ServiceName: req.Name,
		StackName:   req.Name,
		Mode:        "production",
		ContextDir:  "/tmp/shipyard-template-" + req.Name,
		Shipfile:    sf,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("deploy failed: %v", err)))
		return
	}

	// Write stack state to etcd.
	if h.store != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			state := &store.StackState{
				Name:          req.Name,
				ServiceName:   req.Name,
				Platform:      "docker",
				Mode:          "production",
				State:         store.StateRunning,
				StateAt:       time.Now(),
				LastOperation: "deploy",
				Containers: []store.ContainerRecord{{
					ContainerID:   deployed.ContainerID,
					ContainerName: deployed.ContainerName,
					ServiceName:   req.Name,
					Mode:          "production",
					Status:        "running",
					Image:         deployed.ImageTag,
					Ports:         deployed.Ports,
					CreatedAt:     time.Now(),
				}},
			}
			if err := h.store.PutStackState(ctx, state); err != nil {
				fmt.Printf("templates: etcd write failed (non-fatal): %v\n", err)
			}
		}()
	}

	// Publish event.
	if h.bus != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = h.bus.PublishEvent(ctx, telemetry.Event{
				Type:        "deploy",
				ServiceName: req.Name,
				ContainerID: deployed.ContainerID,
				Status:      "success",
				Operator:    "template:" + id,
			})
		}()
	}

	c.JSON(http.StatusCreated, gin.H{
		"message":   fmt.Sprintf("template %q deployed as %q", t.Name, req.Name),
		"template":  t.Name,
		"container": deployed,
		"ports":     deployed.Ports,
	})
}
