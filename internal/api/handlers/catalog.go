package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/catalog"
	"github.com/shipyard/shipyard/pkg/importer"
	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/shipfile"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

// CatalogHandler serves blueprint catalog endpoints.
type CatalogHandler struct {
	cat        *catalog.Catalog
	svcHandler *ServiceHandler
	orch       *orchestrator.Orchestrator
	store      *store.Store
	bus        *telemetry.Bus
}

// NewCatalogHandler creates a CatalogHandler.
func NewCatalogHandler(cat *catalog.Catalog, svc *ServiceHandler, orch *orchestrator.Orchestrator, st *store.Store, bus *telemetry.Bus) *CatalogHandler {
	return &CatalogHandler{cat: cat, svcHandler: svc, orch: orch, store: st, bus: bus}
}

// List handles GET /api/v1/catalog
func (h *CatalogHandler) List(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bps, err := h.cat.List(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}

	// Filter by tag if provided.
	if tag := c.Query("tag"); tag != "" {
		var filtered []*catalog.Blueprint
		for _, bp := range bps {
			for _, t := range bp.Tags {
				if strings.EqualFold(t, tag) {
					filtered = append(filtered, bp)
					break
				}
			}
		}
		bps = filtered
	}

	if bps == nil {
		bps = []*catalog.Blueprint{}
	}
	c.JSON(http.StatusOK, gin.H{
		"blueprints": bps,
		"count":      len(bps),
		"profiles":   catalog.GetProfiles(),
	})
}

// Get handles GET /api/v1/catalog/:name
func (h *CatalogHandler) Get(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bp, err := h.cat.Get(ctx, c.Param("name"))
	if err != nil || bp == nil {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("blueprint %q not found", c.Param("name"))))
		return
	}
	c.JSON(http.StatusOK, bp)
}

// Delete handles DELETE /api/v1/catalog/:name
func (h *CatalogHandler) Delete(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.cat.Delete(ctx, c.Param("name")); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "blueprint deleted"})
}

// Profiles handles GET /api/v1/catalog/profiles
func (h *CatalogHandler) Profiles(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"profiles": catalog.GetProfiles()})
}

// Import handles POST /api/v1/catalog/import
// Analyses a repo with AI and saves it as a blueprint.
func (h *CatalogHandler) Import(c *gin.Context) {
	var req struct {
		GitURL string `json:"gitURL" binding:"required"`
		Name   string `json:"name"`
		Branch string `json:"branch"`
		Mode   string `json:"mode"` // "ai" | "repo" | "catalog"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid request: %v", err)))
		return
	}

	if req.Name == "" {
		req.Name = extractBlueprintName(req.GitURL)
	}
	if req.Mode == "" {
		req.Mode = "ai"
	}

	ctx := c.Request.Context()

	// Reuse existing local clone if already onboarded.
	var localPath string
	if record, ok := h.svcHandler.GetRecord(req.Name); ok {
		localPath = record.ContextDir
	}

	var sf *shipfile.Shipfile
	importMode := catalog.ImportModeAI

	switch req.Mode {
	case "ai":
		ai := importer.NewAIImporter("")
		result, err := ai.Import(ctx, localPath, req.GitURL, req.Name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("AI import failed: %v", err)))
			return
		}
		sf = result.Shipfile
	default:
		importMode = catalog.ImportModeRepo
		sf = buildMinimalBlueprintShipfile(req.Name)
	}

	bp := &catalog.Blueprint{
		Name:       req.Name,
		ImportMode: importMode,
		GitURL:     req.GitURL,
		GitBranch:  req.Branch,
		LocalPath:  localPath,
		Manifest:   sf,
		Tags:       blueprintTags(sf),
	}
	if sf != nil {
		bp.Description = sf.Service.Description
	}

	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.cat.Put(saveCtx, bp); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to save blueprint: %v", err)))
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "blueprint imported", "blueprint": bp})
}

// Deploy handles POST /api/v1/catalog/:name/deploy
// Instantiates a blueprint with a power profile and runs the container.
func (h *CatalogHandler) Deploy(c *gin.Context) {
	var req struct {
		Profile    catalog.PowerProfile `json:"profile"`
		Parameters map[string]string   `json:"parameters"`
		Mode       string              `json:"mode"`
		StackName  string              `json:"stackName"`
	}
	c.ShouldBindJSON(&req)
	if req.Profile == "" {
		req.Profile = catalog.ProfileBalanced
	}
	if req.Mode == "" {
		req.Mode = "production"
	}

	name := c.Param("name")

	// Step 1: get blueprint from catalog.
	bpCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bp, err := h.cat.Get(bpCtx, name)
	if err != nil || bp == nil {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("blueprint %q not found", name)))
		return
	}

	// Step 2: instantiate with power profile + parameters.
	sf, err := h.cat.Instantiate(bpCtx, catalog.InstantiateRequest{
		BlueprintName: name,
		Parameters:    req.Parameters,
		Profile:       req.Profile,
		Mode:          req.Mode,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("instantiate failed: %v", err)))
		return
	}

	// Step 3: resolve context dir — use local clone if available.
	contextDir := bp.LocalPath
	if contextDir == "" {
		// Try the service registry for an existing clone.
		if record, ok := h.svcHandler.GetRecord(name); ok {
			contextDir = record.ContextDir
		}
	}
	if contextDir == "" {
		// No local clone — use a temp dir (for pure image-based blueprints).
		contextDir = "/tmp/shipyard-catalog-" + name
	}

	// Step 4: deploy via orchestrator.
	deployCtx, deployCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer deployCancel()

	deployed, err := h.orch.Deploy(deployCtx, orchestrator.DeployRequest{
		ServiceName: name,
		StackName:   req.StackName,
		Mode:        req.Mode,
		ContextDir:  contextDir,
		Shipfile:    sf,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("deploy failed: %v", err)))
		return
	}

	// Write state to etcd + NATS so Monitor tab picks it up.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if h.store != nil && deployed != nil {
			state := &store.StackState{
				Name:          name,
				ServiceName:   name,
				Platform:      string(sf.Service.Engine.Type),
				Mode:          req.Mode,
				State:         store.StateRunning,
				StateAt:       time.Now(),
				LastOperation: "deploy",
				Containers: []store.ContainerRecord{{
					ContainerID:   deployed.ContainerID,
					ContainerName: deployed.ContainerName,
					ServiceName:   name,
					Mode:          req.Mode,
					Status:        "running",
					Image:         deployed.ImageTag,
					Ports:         deployed.Ports,
					CreatedAt:     time.Now(),
				}},
			}
			_ = h.store.PutStackState(ctx, state)
			_ = h.store.WriteLedgerEntry(ctx, &store.LedgerEntry{
				Name:      name,
				State:     state,
				Containers: state.Containers,
				Operation: "deploy",
				Operator:  "catalog",
			})
		}
		if h.bus != nil && deployed != nil {
			_ = h.bus.PublishEvent(ctx, telemetry.Event{
				Type:        "deploy",
				ServiceName: name,
				ContainerID: deployed.ContainerID,
				Mode:        req.Mode,
				Status:      "success",
				Operator:    "catalog",
			})
		}
	}()

	c.JSON(http.StatusCreated, gin.H{
		"message":   fmt.Sprintf("blueprint %q deployed with %s profile", name, req.Profile),
		"container": deployed,
		"profile":   req.Profile,
	})
}

// SaveFromService handles POST /api/v1/catalog/save/:name
// Saves an already-onboarded service as a catalog blueprint.
func (h *CatalogHandler) SaveFromService(c *gin.Context) {
	name := c.Param("name")
	record, ok := h.svcHandler.GetRecord(name)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	sf, err := loadShipfile(name, record.ContextDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to load config: %v", err)))
		return
	}

	bp := &catalog.Blueprint{
		Name:        name,
		Description: sf.Service.Description,
		ImportMode:  catalog.ImportModeCatalog,
		LocalPath:   record.ContextDir,
		GitURL:      record.RepoURL,
		Manifest:    sf,
		Tags:        blueprintTags(sf),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.cat.Put(ctx, bp); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(err.Error()))
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"message":   fmt.Sprintf("service %q saved to catalog", name),
		"blueprint": bp,
	})
}

// ── helpers ───────────────────────────────────────────────────────────────

func extractBlueprintName(url string) string {
	url = strings.TrimRight(url, "/")
	if idx := strings.LastIndex(url, "/"); idx >= 0 {
		name := url[idx+1:]
		return strings.TrimSuffix(name, ".git")
	}
	return "unnamed"
}

func buildMinimalBlueprintShipfile(name string) *shipfile.Shipfile {
	return &shipfile.Shipfile{
		Service: shipfile.Service{
			Name:        name,
			Description: "Auto-generated config for " + name,
			Engine:      shipfile.EngineConfig{Type: shipfile.EngineDocker},
			Modes: map[string]shipfile.Mode{
				"production": {
					Build: shipfile.Build{Dockerfile: "Dockerfile"},
					Runtime: shipfile.Runtime{
						Ports: []shipfile.Port{{Name: "app", Internal: 80, External: "auto"}},
					},
				},
			},
		},
	}
}

func blueprintTags(sf *shipfile.Shipfile) []string {
	if sf == nil {
		return nil
	}
	var tags []string
	if sf.Service.Engine.Type != "" {
		tags = append(tags, string(sf.Service.Engine.Type))
	}
	return append(tags, sf.Service.Tags...)
}
