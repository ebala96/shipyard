package handlers

import (
	"fmt"
	"net/http"
	"text/template"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/relay"
	"github.com/shipyard/shipyard/pkg/vnc"
)

// RelayHandler manages VNC session sharing via the WebSocket relay.
type RelayHandler struct {
	manager     *relay.Manager
	vncRegistry *vnc.Registry
	baseURL     string // e.g. "http://192.168.1.10:8888"
}

// NewRelayHandler creates a RelayHandler.
func NewRelayHandler(manager *relay.Manager, registry *vnc.Registry, baseURL string) *RelayHandler {
	return &RelayHandler{manager: manager, vncRegistry: registry, baseURL: baseURL}
}

// Share handles POST /api/v1/services/:name/vnc/share
// Creates a relay room for the service's active VNC session and returns a
// shareable URL that any browser can open to view the session.
func (h *RelayHandler) Share(c *gin.Context) {
	serviceName := c.Param("name")

	inst, ok := h.vncRegistry.Get(serviceName)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("no active VNC session for %q — start VNC first", serviceName),
		})
		return
	}

	// Build the websockify WebSocket URL for the noVNC sidecar.
	// theasp/novnc exposes websockify at /websockify on its HTTP port.
	upstreamWSURL := fmt.Sprintf("ws://localhost:%d/websockify", inst.HostPort)

	room := h.manager.Create(serviceName, upstreamWSURL)

	viewURL := fmt.Sprintf("%s/relay/%s/view", h.baseURL, room.Token)
	wsURL := fmt.Sprintf("%s/relay/%s", h.baseURL, room.Token)

	c.JSON(http.StatusCreated, gin.H{
		"token":   room.Token,
		"viewURL": viewURL,
		"wsURL":   wsURL,
		"message": "share this URL with the other user",
	})
}

// Connect handles GET /relay/:token  (WebSocket upgrade)
// Viewers connect here. Each gets its own proxied connection to the upstream.
func (h *RelayHandler) Connect(c *gin.Context) {
	token := c.Param("token")

	room, ok := h.manager.Get(token)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "relay session not found or expired"})
		return
	}

	// ServeViewer blocks until the viewer disconnects — run synchronously
	// so Gin can handle the WebSocket lifecycle naturally.
	if err := room.ServeViewer(c.Writer, c.Request); err != nil {
		// Normal close — no need to surface as HTTP error.
		fmt.Printf("relay: viewer disconnected from %q: %v\n", token, err)
	}
}

// View handles GET /relay/:token/view
// Serves a minimal noVNC HTML page that connects to the relay WebSocket.
// This is the URL shared with the remote user.
func (h *RelayHandler) View(c *gin.Context) {
	token := c.Param("token")

	if _, ok := h.manager.Get(token); !ok {
		c.String(http.StatusNotFound, "relay session not found or expired")
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	viewerPageTmpl.Execute(c.Writer, map[string]string{ //nolint:errcheck
		"Token": token,
		"Host":  c.Request.Host,
	})
}

// Sessions handles GET /api/v1/relay — list active relay rooms.
func (h *RelayHandler) Sessions(c *gin.Context) {
	rooms := h.manager.List()
	type info struct {
		Token       string `json:"token"`
		ServiceName string `json:"serviceName"`
		CreatedAt   string `json:"createdAt"`
	}
	result := make([]info, len(rooms))
	for i, r := range rooms {
		result[i] = info{Token: r.Token, ServiceName: r.ServiceName, CreatedAt: r.CreatedAt.Format("2006-01-02T15:04:05Z")}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": result, "count": len(result)})
}

// Delete handles DELETE /api/v1/relay/:token — revoke a relay room.
func (h *RelayHandler) Delete(c *gin.Context) {
	h.manager.Delete(c.Param("token"))
	c.JSON(http.StatusOK, gin.H{"message": "relay session revoked"})
}

// ── Viewer HTML page ──────────────────────────────────────────────────────────

// viewerPageTmpl is a self-contained noVNC viewer page loaded via CDN.
// The WebSocket URL points to the relay endpoint on the Shipyard server.
var viewerPageTmpl = template.Must(template.New("vnc-viewer").Parse(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Shipyard VNC — shared session</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body { background: #1a1a2e; color: #eee; font-family: sans-serif; }
    #toolbar {
      display: flex; align-items: center; gap: 12px;
      padding: 8px 16px; background: #16213e; border-bottom: 1px solid #0f3460;
    }
    #toolbar span { font-size: 13px; opacity: .7; }
    #status { font-size: 12px; color: #4ade80; }
    #screen { width: 100vw; height: calc(100vh - 41px); background: #000; }
  </style>
</head>
<body>
  <div id="toolbar">
    <span>Shipyard VNC Share</span>
    <span id="status">connecting…</span>
  </div>
  <div id="screen"></div>

  <script type="module">
    import RFB from 'https://unpkg.com/@novnc/novnc@1.5.0/core/rfb.js';

    const proto = location.protocol === 'https:' ? 'wss' : 'ws';
    const wsURL = proto + '://' + '{{.Host}}' + '/relay/{{.Token}}';
    const status = document.getElementById('status');

    let rfb;
    try {
      rfb = new RFB(document.getElementById('screen'), wsURL);
      rfb.scaleViewport = true;
      rfb.resizeSession = false;

      rfb.addEventListener('connect',    () => { status.textContent = 'connected'; status.style.color = '#4ade80'; });
      rfb.addEventListener('disconnect', () => { status.textContent = 'disconnected'; status.style.color = '#f87171'; });
    } catch (e) {
      status.textContent = 'error: ' + e.message;
      status.style.color = '#f87171';
    }
  </script>
</body>
</html>
`))
