package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/datadir"
	"github.com/shipyard/shipyard/pkg/shipfile"
	"gopkg.in/yaml.v3"
)

// ManifestHandler serves and accepts raw shipfile.yml edits.
type ManifestHandler struct {
	svcHandler *ServiceHandler
}

// NewManifestHandler creates a ManifestHandler.
func NewManifestHandler(svc *ServiceHandler) *ManifestHandler {
	return &ManifestHandler{svcHandler: svc}
}

// Get handles GET /api/v1/services/:name/manifest
// Returns the raw YAML content of the service's shipfile.yml.
func (h *ManifestHandler) Get(c *gin.Context) {
	name := c.Param("name")

	record, ok := h.svcHandler.GetRecord(name)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	path := resolveManifestPath(name, record.ContextDir)
	if path == "" {
		c.JSON(http.StatusNotFound, errorResponse("shipfile.yml not found for this service"))
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to read config: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"name":    name,
		"path":    path,
		"content": string(data),
	})
}

// Put handles PUT /api/v1/services/:name/manifest
// Validates and saves new YAML content to the service's shipfile.yml.
func (h *ManifestHandler) Put(c *gin.Context) {
	name := c.Param("name")

	record, ok := h.svcHandler.GetRecord(name)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found", name)))
		return
	}

	var body struct {
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("content field is required"))
		return
	}

	// Stage 1: parse the YAML to catch syntax errors before saving.
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(body.Content), &raw); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid YAML: %v", err)))
		return
	}

	// Stage 2: parse as a Shipfile to catch structural errors.
	var sf shipfile.Shipfile
	if err := yaml.Unmarshal([]byte(body.Content), &sf); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid shipfile structure: %v", err)))
		return
	}
	if sf.Service.Name == "" {
		c.JSON(http.StatusBadRequest, errorResponse("service.name is required"))
		return
	}

	// Write to the canonical manifest path.
	path := resolveManifestPath(name, record.ContextDir)
	if path == "" {
		// Create it at the default location.
		path = datadir.ServiceShipfilePath(name)
	}

	// Backup the existing file.
	if existing, err := os.ReadFile(path); err == nil {
		backupPath := path + ".bak"
		_ = os.WriteFile(backupPath, existing, 0644)
	}

	if err := os.WriteFile(path, []byte(body.Content), 0644); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to write config: %v", err)))
		return
	}

	fmt.Printf("config: saved %q for service %q\n", path, name)
	c.JSON(http.StatusOK, gin.H{
		"message": "config saved",
		"path":    path,
	})
}

// resolveManifestPath finds the shipfile.yml for a service.
// Checks the canonical location then the context directory.
func resolveManifestPath(name, contextDir string) string {
	candidates := []string{
		datadir.ServiceShipfilePath(name),
	}
	if contextDir != "" {
		candidates = append(candidates,
			filepath.Join(contextDir, "shipfile.yml"),
			filepath.Join(contextDir, "..", "shipfile.yml"),
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}