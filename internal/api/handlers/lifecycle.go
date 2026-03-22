package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/orchestrator"
)

// ExecRequest is the JSON body for the exec endpoint.
type ExecRequest struct {
	Cmd []string `json:"cmd" binding:"required"`
}

// LifecycleHandler handles container start/stop/restart/remove/exec operations.
type LifecycleHandler struct {
	orch *orchestrator.Orchestrator
}

// NewLifecycleHandler creates a LifecycleHandler.
func NewLifecycleHandler(orch *orchestrator.Orchestrator) *LifecycleHandler {
	return &LifecycleHandler{orch: orch}
}

// Start handles POST /api/v1/containers/:id/start
func (h *LifecycleHandler) Start(c *gin.Context) {
	id := c.Param("id")

	if err := h.orch.Start(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to start container: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("container %s started", id)})
}

// Stop handles POST /api/v1/containers/:id/stop
// Optional query params: service=<name>&mode=<mode> — also stops the IDE sidecar.
func (h *LifecycleHandler) Stop(c *gin.Context) {
	id := c.Param("id")
	service := c.Query("service")
	mode := c.Query("mode")

	var err error
	if service != "" && mode != "" {
		err = h.orch.StopWithIDE(c.Request.Context(), id, service, mode)
	} else {
		err = h.orch.Stop(c.Request.Context(), id)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to stop container: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("container %s stopped", id)})
}

// Restart handles POST /api/v1/containers/:id/restart
func (h *LifecycleHandler) Restart(c *gin.Context) {
	id := c.Param("id")

	if err := h.orch.Restart(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to restart container: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("container %s restarted", id)})
}

// Remove handles DELETE /api/v1/containers/:id
// Optional query params: service=<name>&mode=<mode> — also removes the IDE sidecar.
func (h *LifecycleHandler) Remove(c *gin.Context) {
	id := c.Param("id")
	force := c.Query("force") == "true"
	service := c.Query("service")
	mode := c.Query("mode")

	var err error
	if service != "" && mode != "" {
		err = h.orch.RemoveWithIDE(c.Request.Context(), id, service, mode, force)
	} else {
		err = h.orch.Remove(c.Request.Context(), id, force)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to remove container: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("container %s removed", id)})
}

// Status handles GET /api/v1/containers/:id/status
func (h *LifecycleHandler) Status(c *gin.Context) {
	id := c.Param("id")

	status, err := h.orch.Status(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to get container status: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"containerID": id,
		"status":      status,
	})
}

// Exec handles POST /api/v1/containers/:id/exec
func (h *LifecycleHandler) Exec(c *gin.Context) {
	id := c.Param("id")

	var req ExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(fmt.Sprintf("invalid request body: %v", err)))
		return
	}

	output, err := h.orch.Exec(c.Request.Context(), id, req.Cmd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("exec failed: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"containerID": id,
		"cmd":         req.Cmd,
		"output":      output,
	})
}
