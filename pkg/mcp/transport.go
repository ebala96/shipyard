package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MCPServer implements the MCP Streamable HTTP transport (2025-03-26 spec).
//
// Single endpoint: POST /mcp
//
//	Client sets Accept: application/json       → direct JSON response (most calls)
//	Client sets Accept: text/event-stream      → SSE stream for server-push
//
// The old /mcp/sse and /mcp/messages endpoints are kept as aliases
// so existing mcp-remote installs keep working during the transition.
type MCPServer struct {
	client   *ShipyardClient
	mu       sync.Mutex
	sessions map[string]*sseSession
}

type sseSession struct {
	id      string
	ch      chan []byte
	created time.Time
}

// NewMCPServer creates an MCP server backed by the Shipyard API.
func NewMCPServer(apiURL string) *MCPServer {
	return &MCPServer{
		client:   NewShipyardClient(apiURL),
		sessions: make(map[string]*sseSession),
	}
}

// Mount registers all MCP endpoints.
func (s *MCPServer) Mount(mux *http.ServeMux) {
	// Primary — Streamable HTTP (2025-03-26 spec)
	mux.HandleFunc("/mcp", s.handleStreamable)

	// Legacy aliases — kept for mcp-remote compatibility
	mux.HandleFunc("/mcp/sse", s.handleStreamable)
	mux.HandleFunc("/mcp/messages", s.handleStreamable)

	// Health check
	mux.HandleFunc("/mcp/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":    "ok",
			"server":    "shipyard-mcp",
			"transport": "streamable-http",
			"spec":      "2025-03-26",
		})
	})

	log.Printf("mcp: Streamable HTTP transport at /mcp (spec 2025-03-26)")
}

// handleStreamable is the single unified handler for all MCP traffic.
//
// GET  → open SSE stream (server-push channel, optional)
// POST → process JSON-RPC, respond directly in body OR upgrade to SSE
// OPTIONS → CORS preflight
func (s *MCPServer) handleStreamable(w http.ResponseWriter, r *http.Request) {
	s.setCORS(w)

	switch r.Method {
	case http.MethodOptions:
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		// Client wants a persistent SSE channel for server-initiated pushes.
		s.handleSSEStream(w, r)
	case http.MethodPost:
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "text/event-stream") {
			// Client wants response streamed via SSE inside this same request.
			s.handlePostSSE(w, r)
		} else {
			// Standard: process request and respond directly in the body.
			s.handlePostDirect(w, r)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handlePostDirect handles POST requests and writes the JSON-RPC response
// directly in the HTTP response body. This is the primary Streamable HTTP flow.
func (s *MCPServer) handlePostDirect(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "parse error: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, rpcErr := s.dispatch(ctx, req.Method, req.Params)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Mcp-Session-Id", fmt.Sprintf("sess_%d", time.Now().UnixNano()))
	w.WriteHeader(http.StatusOK)

	resp := jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	json.NewEncoder(w).Encode(resp)
}

// handlePostSSE handles POST requests where the client wants the response
// streamed back as SSE events within the same HTTP connection.
func (s *MCPServer) handlePostSSE(w http.ResponseWriter, r *http.Request) {
	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flush(w)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, rpcErr := s.dispatch(ctx, req.Method, req.Params)

	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}

	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	flush(w)
}

// handleSSEStream opens a persistent GET SSE channel for server-initiated pushes.
// The client connects once and receives events as they happen.
func (s *MCPServer) handleSSEStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	sessionID := fmt.Sprintf("sess_%d", time.Now().UnixNano())
	sess := &sseSession{
		id:      sessionID,
		ch:      make(chan []byte, 32),
		created: time.Now(),
	}

	s.mu.Lock()
	s.sessions[sessionID] = sess
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.sessions, sessionID)
		s.mu.Unlock()
	}()

	// Send session ID so client can correlate future POSTs.
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp?sessionId=%s\n\n", sessionID)
	flush(w)

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-sess.ch:
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
			flush(w)
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flush(w)
		}
	}
}

// dispatch routes a JSON-RPC method to the correct handler.
func (s *MCPServer) dispatch(ctx context.Context, method string, params json.RawMessage) (interface{}, *jsonRPCError) {
	var p map[string]interface{}
	if len(params) > 0 {
		json.Unmarshal(params, &p)
	}
	if p == nil {
		p = map[string]interface{}{}
	}

	switch method {
	case "initialize":
		version := "2025-03-26"
		if v, ok := p["protocolVersion"].(string); ok && v != "" {
			version = v
		}
		return map[string]interface{}{
			"protocolVersion": version,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "shipyard", "version": "1.0.0"},
		}, nil

	case "notifications/initialized":
		return map[string]interface{}{}, nil

	case "ping":
		return map[string]interface{}{}, nil

	case "tools/list":
		return map[string]interface{}{"tools": s.toolList()}, nil

	case "tools/call":
		name, _ := p["name"].(string)
		args, _ := p["arguments"].(map[string]interface{})
		if args == nil {
			args = map[string]interface{}{}
		}
		if name == "" {
			return nil, &jsonRPCError{Code: -32602, Message: "missing tool name"}
		}
		result, err := s.callTool(ctx, name, args)
		if err != nil {
			return nil, &jsonRPCError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	default:
		return nil, &jsonRPCError{Code: -32601, Message: "method not found: " + method}
	}
}

// ── Types ─────────────────────────────────────────────────────────────────────

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── Tool call dispatcher ──────────────────────────────────────────────────────

func (s *MCPServer) callTool(ctx context.Context, name string, args map[string]interface{}) (interface{}, error) {
	var text string
	var err error

	switch name {
	case "list_services":
		text, err = s.client.ListServices(ctx)
	case "deploy_service":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.DeployService(ctx, svcName, strArg(args, "mode"), strArg(args, "platform"))
	case "stop_service":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.StopService(ctx, svcName)
	case "start_service":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.StartService(ctx, svcName)
	case "restart_service":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.RestartService(ctx, svcName)
	case "get_logs":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.GetLogs(ctx, svcName, strArg(args, "tail"))
	case "get_metrics":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.GetMetrics(ctx, svcName)
	case "scale_service":
		svcName := strArg(args, "name")
		replicas := intArg(args, "replicas")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		if replicas < 1 {
			return nil, fmt.Errorf("replicas must be >= 1")
		}
		text, err = s.client.ScaleService(ctx, svcName, replicas)
	case "list_blueprints":
		text, err = s.client.ListBlueprints(ctx)
	case "deploy_blueprint":
		bpName := strArg(args, "name")
		if bpName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.DeployBlueprint(ctx, bpName, strArg(args, "profile"))
	case "resolve_service":
		svcName := strArg(args, "name")
		if svcName == "" {
			return nil, fmt.Errorf("name is required")
		}
		text, err = s.client.ResolveService(ctx, svcName)
	case "get_nodes":
		text, err = s.client.GetNodes(ctx)
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}

	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	}, nil
}

// ── Tool schema ───────────────────────────────────────────────────────────────

func (s *MCPServer) toolList() []map[string]interface{} {
	return []map[string]interface{}{
		tool("list_services", "List all Shipyard services with their current status", props(), nil),
		tool("deploy_service", "Deploy a service by name", props(
			strProp("name", "Service name to deploy"),
			strProp("mode", "Deployment mode e.g. production (optional)"),
			strProp("platform", "Platform: docker, compose, kubernetes, nomad (optional)"),
		), []string{"name"}),
		tool("stop_service",    "Stop a running service",  props(strProp("name", "Service name")), []string{"name"}),
		tool("start_service",   "Start a stopped service", props(strProp("name", "Service name")), []string{"name"}),
		tool("restart_service", "Restart a service",       props(strProp("name", "Service name")), []string{"name"}),
		tool("get_logs", "Fetch recent logs for a service", props(
			strProp("name", "Service name"),
			strProp("tail", "Number of log lines (default 50)"),
		), []string{"name"}),
		tool("get_metrics", "Get current CPU and RAM usage for a service",
			props(strProp("name", "Service name")), []string{"name"}),
		tool("scale_service", "Change the number of replicas for a service", props(
			strProp("name", "Service name"),
			intProp("replicas", "Number of replicas (min 1)"),
		), []string{"name", "replicas"}),
		tool("list_blueprints", "List all blueprints in the catalog", props(), nil),
		tool("deploy_blueprint", "Deploy a blueprint from the catalog with a power profile", props(
			strProp("name", "Blueprint name"),
			strProp("profile", "Power profile: eco, balanced, performance, max"),
		), []string{"name"}),
		tool("resolve_service", "Get the Shiplink URL and DNS name for a service",
			props(strProp("name", "Service name")), []string{"name"}),
		tool("get_nodes", "List all registered nodes with CPU and RAM usage", props(), nil),
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (s *MCPServer) setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, DELETE")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Mcp-Session-Id")
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
}

func (s *MCPServer) writeError(w http.ResponseWriter, id interface{}, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: msg},
	})
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func tool(name, desc string, properties map[string]interface{}, required []string) map[string]interface{} {
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{
		"name":        name,
		"description": desc,
		"inputSchema": map[string]interface{}{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
	}
}

func props(fields ...map[string]interface{}) map[string]interface{} {
	m := make(map[string]interface{})
	for _, f := range fields {
		for k, v := range f {
			m[k] = v
		}
	}
	return m
}

func strProp(name, desc string) map[string]interface{} {
	return map[string]interface{}{name: map[string]interface{}{"type": "string", "description": desc}}
}

func intProp(name, desc string) map[string]interface{} {
	return map[string]interface{}{name: map[string]interface{}{"type": "integer", "description": desc}}
}

func strArg(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func intArg(args map[string]interface{}, key string) int {
	v := strArg(args, key)
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), ".0"))
	return n
}