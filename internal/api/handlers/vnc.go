package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/proxy"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/vnc"
)

// VNCHandler handles VNC session management endpoints.
type VNCHandler struct {
	launcher   *vnc.Launcher
	registry   *vnc.Registry
	store      *store.Store
	prx        *proxy.Proxy
	svcHandler *ServiceHandler
}

// NewVNCHandler creates a VNCHandler.
func NewVNCHandler(launcher *vnc.Launcher, registry *vnc.Registry, st *store.Store, prx *proxy.Proxy, svc *ServiceHandler) *VNCHandler {
	return &VNCHandler{
		launcher:   launcher,
		registry:   registry,
		store:      st,
		prx:        prx,
		svcHandler: svc,
	}
}

// Get handles GET /api/v1/services/:name/vnc
// Returns the active VNC session for a service, or 404 if none.
func (h *VNCHandler) Get(c *gin.Context) {
	serviceName := c.Param("name")

	inst, ok := h.registry.Get(serviceName)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no active VNC session for " + serviceName})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"serviceName":   inst.ServiceName,
		"containerID":   inst.ContainerID,
		"containerName": inst.ContainerName,
		"hostPort":      inst.HostPort,
		"url":           inst.URL,
		"proxyURL":      h.prx.ServiceURL(proxy.VNCPrefix(serviceName)),
	})
}

// Start handles POST /api/v1/services/:name/vnc/start
// Launches a noVNC sidecar for the named service's running container.
func (h *VNCHandler) Start(c *gin.Context) {
	serviceName := c.Param("name")

	// Return existing session if already running.
	if inst, ok := h.registry.Get(serviceName); ok {
		c.JSON(http.StatusOK, gin.H{
			"message":  "VNC session already running",
			"url":      inst.URL,
			"proxyURL": h.prx.ServiceURL(proxy.VNCPrefix(serviceName)),
		})
		return
	}

	// Look up the running container from etcd to get its name and mode.
	containerName, mode, networkName, vncPort, err := h.resolveContainerInfo(c.Request.Context(), serviceName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	inst, err := h.launcher.Launch(c.Request.Context(), serviceName, mode, networkName, containerName, vncPort)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("VNC launch failed: %v", err)})
		return
	}

	// Register proxy route: /vnc/<name> → noVNC sidecar.
	prefix := proxy.VNCPrefix(serviceName)
	if err := h.prx.Register(prefix, fmt.Sprintf("http://localhost:%d", inst.HostPort), "VNC — "+serviceName); err != nil {
		fmt.Printf("vnc: proxy registration failed (non-fatal): %v\n", err)
	}

	h.registry.Put(serviceName, inst)

	c.JSON(http.StatusCreated, gin.H{
		"message":  "VNC session started",
		"url":      inst.URL,
		"proxyURL": h.prx.ServiceURL(prefix),
	})
}

// Stop handles POST /api/v1/services/:name/vnc/stop
// Stops the noVNC sidecar and deregisters the proxy route.
func (h *VNCHandler) Stop(c *gin.Context) {
	serviceName := c.Param("name")

	inst, ok := h.registry.Get(serviceName)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "no active VNC session for " + serviceName})
		return
	}

	mode := inst.ContainerName // ContainerName encodes the mode via SidecarName convention.
	// Derive mode from container name: shipyard_<service>_<mode>_vnc → mode is the third segment.
	mode = deriveMode(serviceName, inst.ContainerName)

	if err := h.launcher.Stop(c.Request.Context(), serviceName, mode); err != nil {
		fmt.Printf("vnc: stop sidecar failed (non-fatal): %v\n", err)
	}

	h.prx.Deregister(proxy.VNCPrefix(serviceName))
	h.registry.Delete(serviceName)

	c.JSON(http.StatusOK, gin.H{"message": "VNC session stopped"})
}

// List handles GET /api/v1/vnc
// Returns all active VNC sessions.
func (h *VNCHandler) List(c *gin.Context) {
	sessions := h.registry.List()
	c.JSON(http.StatusOK, gin.H{"sessions": sessions, "count": len(sessions)})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// resolveContainerInfo looks up the running container details for a service
// from the etcd stack state. Returns containerName, mode, networkName, vncPort.
func (h *VNCHandler) resolveContainerInfo(ctx context.Context, serviceName string) (containerName, mode, networkName string, vncPort int, err error) {
	if h.store == nil {
		return "", "production", "", 5900, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	states, listErr := h.store.ListStackStates(ctx)
	if listErr != nil {
		return "", "production", "", 5900, nil
	}

	for _, state := range states {
		if state.ServiceName != serviceName {
			continue
		}
		if len(state.Containers) == 0 {
			continue
		}
		ctr := state.Containers[0]
		network := ""
		if state.StackName != "" {
			network = "shipyard_" + state.StackName
		}
		vncPort = 5900
		if ctr.VNCInstance != nil && ctr.VNCInstance.HostPort > 0 {
			// Already has a VNC record — use its port as a hint.
			vncPort = 5900
		}
		return ctr.ContainerName, state.Mode, network, vncPort, nil
	}

	// Fallback: use service record to find the container.
	if h.svcHandler != nil {
		if _, ok := h.svcHandler.GetRecord(serviceName); ok {
			return "shipyard_" + serviceName + "_production", "production", "", 5900, nil
		}
	}

	return "", "", "", 0, fmt.Errorf("no running container found for service %q — deploy it first", serviceName)
}

// deriveMode extracts the mode from a VNC sidecar container name.
// Container name format: shipyard_<service>_<mode>_vnc
func deriveMode(serviceName, containerName string) string {
	// Strip "shipyard_" prefix and "_vnc" suffix, then remove service name.
	suffix := "_vnc"
	name := containerName
	if len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix {
		name = name[:len(name)-len(suffix)]
	}
	prefix := "shipyard_" + sanitizeName(serviceName) + "_"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}
	return "production"
}

func sanitizeName(s string) string {
	result := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			result[i] = c + 32
		} else if c == '-' || c == ' ' {
			result[i] = '_'
		} else {
			result[i] = c
		}
	}
	return string(result)
}
