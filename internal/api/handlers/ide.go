package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/datadir"
	"github.com/shipyard/shipyard/pkg/idemanager"
	"github.com/shipyard/shipyard/pkg/proxy"
)

// IDEHandler handles on-demand code-server IDE operations.
type IDEHandler struct {
	mgr        *idemanager.Manager
	proxy      *proxy.Proxy
	svcHandler *ServiceHandler
}

// NewIDEHandler creates an IDEHandler.
func NewIDEHandler(mgr *idemanager.Manager, prx *proxy.Proxy, svc *ServiceHandler) *IDEHandler {
	return &IDEHandler{mgr: mgr, proxy: prx, svcHandler: svc}
}

// Spawn handles POST /api/v1/ide/:name
// Starts a code-server container for the named service if not already running.
// Returns the IDE URL immediately if already running.
func (h *IDEHandler) Spawn(c *gin.Context) {
	name := c.Param("name")

	// Look up the service to get its source directory.
	record, ok := h.svcHandler.GetRecord(name)
	if !ok {
		c.JSON(http.StatusNotFound, errorResponse(fmt.Sprintf("service %q not found — onboard it first", name)))
		return
	}

	// Use the service's source directory.
	sourceDir := record.ContextDir
	if sourceDir == "" {
		sourceDir = datadir.ServiceSourceDir(name)
	}

	// Build the proxy URL for this IDE.
	idePrefix := proxy.IDEPrefix(name)
	proxyURL := h.proxy.ServiceURL(idePrefix)

	// Spawn or return existing.
	instance, err := h.mgr.Spawn(c.Request.Context(), name, sourceDir, proxyURL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to spawn IDE: %v", err)))
		return
	}

	// Register with the proxy so it's accessible via /ide/<name>/.
	if err := h.proxy.Register(idePrefix, instance.DirectURL, "IDE: "+name); err != nil {
		// Non-fatal — direct URL still works.
		fmt.Printf("ide handler: failed to register proxy route: %v\n", err)
	}
	instance.ProxyURL = proxyURL

	c.JSON(http.StatusOK, gin.H{
		"message":  "IDE ready",
		"instance": instance,
	})
}

// Stop handles DELETE /api/v1/ide/:name
// Stops the code-server container for the named service.
func (h *IDEHandler) Stop(c *gin.Context) {
	name := c.Param("name")

	if err := h.mgr.Stop(c.Request.Context(), name); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to stop IDE: %v", err)))
		return
	}

	// Remove from proxy.
	h.proxy.Deregister(proxy.IDEPrefix(name))

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("IDE for %q stopped", name)})
}

// List handles GET /api/v1/ide
// Returns all running IDE instances.
func (h *IDEHandler) List(c *gin.Context) {
	instances := h.mgr.List()
	c.JSON(http.StatusOK, gin.H{
		"instances": instances,
		"count":     len(instances),
	})
}
