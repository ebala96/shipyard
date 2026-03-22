package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shipyard/shipyard/pkg/orchestrator"
)

// LogsHandler handles container log streaming and fetching.
type LogsHandler struct {
	orch *orchestrator.Orchestrator
}

// NewLogsHandler creates a LogsHandler.
func NewLogsHandler(orch *orchestrator.Orchestrator) *LogsHandler {
	return &LogsHandler{orch: orch}
}

// Stream handles GET /api/v1/containers/:id/logs
// Streams logs as Server-Sent Events (SSE).
// The React dashboard subscribes to this with EventSource.
// Query params:
//
//	tail=50  — number of historical lines to include before live stream
func (h *LogsHandler) Stream(c *gin.Context) {
	id := c.Param("id")
	tail := c.DefaultQuery("tail", "50")

	// Set SSE headers — these tell the browser this is a live event stream.
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // disable nginx buffering if behind a proxy

	// Get the underlying http.ResponseWriter to flush after each event.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, errorResponse("streaming not supported"))
		return
	}

	// Use the request context so the stream stops when the client disconnects.
	ctx := c.Request.Context()
	lines, errs := h.orch.StreamLogs(ctx, id, tail)

	for {
		select {
		case <-ctx.Done():
			// Client disconnected — stop streaming.
			return

		case err, ok := <-errs:
			if !ok {
				return
			}
			writeSSEEvent(c, "error", gin.H{"error": err.Error()})
			flusher.Flush()
			return

		case line, ok := <-lines:
			if !ok {
				// Channel closed — container stopped or context cancelled.
				writeSSEEvent(c, "close", gin.H{"message": "stream ended"})
				flusher.Flush()
				return
			}

			writeSSEEvent(c, "log", gin.H{
				"containerID": line.ContainerID,
				"stream":      line.Stream,
				"text":        line.Text,
			})
			flusher.Flush()
		}
	}
}

// Fetch handles GET /api/v1/containers/:id/logs/fetch
// Returns a snapshot of recent log lines as JSON — no streaming.
// Useful for the initial dashboard load before SSE connection is established.
// Query params:
//
//	tail=100 — number of recent lines to return
func (h *LogsHandler) Fetch(c *gin.Context) {
	id := c.Param("id")
	tail := c.DefaultQuery("tail", "100")

	logs, err := h.orch.FetchLogs(c.Request.Context(), id, tail)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse(fmt.Sprintf("failed to fetch logs: %v", err)))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"containerID": id,
		"lines":       logs,
		"count":       len(logs),
	})
}

// writeSSEEvent writes a single SSE event in the format:
//
//	event: <eventType>\n
//	data: <json>\n\n
func writeSSEEvent(c *gin.Context, eventType string, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", eventType, string(data))
}
