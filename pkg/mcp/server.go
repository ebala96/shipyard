// Package mcp implements the Shipyard MCP (Model Context Protocol) server.
// It exposes Shipyard's capabilities as tools that Claude can call directly.
//
// Tools exposed:
//
//	list_services    — list all services with their status
//	deploy_service   — deploy a service by name
//	stop_service     — stop a running service
//	start_service    — start a stopped service
//	restart_service  — restart a service
//	get_logs         — fetch recent logs for a service
//	get_metrics      — get current CPU/RAM for a service
//	scale_service    — change replica count
//	list_blueprints  — list catalog blueprints
//	deploy_blueprint — deploy from catalog with a power profile
//	resolve_service  — get Shiplink URL for a service
//	get_nodes        — list registered nodes with resource usage
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ShipyardClient is a lightweight HTTP client for the Shipyard API.
type ShipyardClient struct {
	baseURL string
	http    *http.Client
}

// NewShipyardClient creates a client pointed at the Shipyard API.
func NewShipyardClient(baseURL string) *ShipyardClient {
	if baseURL == "" {
		baseURL = "http://localhost:8888"
	}
	return &ShipyardClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ShipyardClient) get(ctx context.Context, path string) (map[string]interface{}, error) {
	return c.do(ctx, "GET", path, nil)
}

func (c *ShipyardClient) post(ctx context.Context, path string, body interface{}) (map[string]interface{}, error) {
	return c.do(ctx, "POST", path, body)
}

func (c *ShipyardClient) do(ctx context.Context, method, path string, body interface{}) (map[string]interface{}, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = strings.NewReader(string(data))
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("shipyard API error: %w", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return result, nil
}

// ── Tool implementations ──────────────────────────────────────────────────────

// ListServices returns all services with their current status.
func (c *ShipyardClient) ListServices(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v1/services")
	if err != nil {
		return "", err
	}

	services, _ := data["services"].([]interface{})
	if len(services) == 0 {
		return "No services found. Use deploy_service to deploy one.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d service(s):\n\n", len(services)))
	for _, s := range services {
		svc, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name := strVal(svc, "name")
		status := strVal(svc, "status")
		mode := strVal(svc, "mode")
		repoURL := strVal(svc, "repoURL")
		sb.WriteString(fmt.Sprintf("• %s — %s (mode: %s)\n", name, status, mode))
		if repoURL != "" {
			sb.WriteString(fmt.Sprintf("  repo: %s\n", repoURL))
		}
	}
	return sb.String(), nil
}

// DeployService deploys a service by name.
func (c *ShipyardClient) DeployService(ctx context.Context, name, mode, platform string) (string, error) {
	body := map[string]interface{}{}
	if mode != "" {
		body["mode"] = mode
	}
	if platform != "" {
		body["platform"] = platform
	}

	data, err := c.post(ctx, "/api/v1/services/"+name+"/deploy", body)
	if err != nil {
		return "", err
	}

	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("deploy failed: %s", errMsg)
	}

	container, _ := data["container"].(map[string]interface{})
	if container == nil {
		return fmt.Sprintf("Deploy started for %q. Check the Monitor tab for status.", name), nil
	}

	ports, _ := container["ports"].(map[string]interface{})
	portStr := ""
	for portName, portVal := range ports {
		portStr += fmt.Sprintf(" %s→%v", portName, portVal)
	}

	return fmt.Sprintf("✅ Deployed %q successfully.\nContainer: %s\nPorts:%s",
		name, strVal(container, "containerID")[:12], portStr), nil
}

// StopService stops a running service.
func (c *ShipyardClient) StopService(ctx context.Context, name string) (string, error) {
	data, err := c.post(ctx, "/api/v1/stacks/"+name+"/stop", nil)
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("stop failed: %s", errMsg)
	}
	return fmt.Sprintf("⏹ Service %q stopped.", name), nil
}

// StartService starts a stopped service.
func (c *ShipyardClient) StartService(ctx context.Context, name string) (string, error) {
	data, err := c.post(ctx, "/api/v1/stacks/"+name+"/start", nil)
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("start failed: %s", errMsg)
	}
	return fmt.Sprintf("▶ Service %q started.", name), nil
}

// RestartService restarts a service.
func (c *ShipyardClient) RestartService(ctx context.Context, name string) (string, error) {
	data, err := c.post(ctx, "/api/v1/stacks/"+name+"/restart", nil)
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("restart failed: %s", errMsg)
	}
	return fmt.Sprintf("🔄 Service %q restarted.", name), nil
}

// GetLogs fetches recent logs for a service.
func (c *ShipyardClient) GetLogs(ctx context.Context, name, tail string) (string, error) {
	if tail == "" {
		tail = "50"
	}

	// First get the container ID for this service.
	svcData, err := c.get(ctx, "/api/v1/services/"+name)
	if err != nil {
		return "", fmt.Errorf("service %q not found: %w", name, err)
	}

	containers, _ := svcData["containers"].([]interface{})
	if len(containers) == 0 {
		return fmt.Sprintf("No running containers found for %q.", name), nil
	}

	ctr, _ := containers[0].(map[string]interface{})
	containerID := strVal(ctr, "containerID")
	if containerID == "" {
		return fmt.Sprintf("Could not find container ID for %q.", name), nil
	}

	logData, err := c.get(ctx, fmt.Sprintf("/api/v1/containers/%s/logs/fetch?tail=%s", containerID, tail))
	if err != nil {
		return "", err
	}

	lines, _ := logData["lines"].([]interface{})
	if len(lines) == 0 {
		return fmt.Sprintf("No logs found for %q.", name), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Last %s log lines for %q:\n\n", tail, name))
	for _, l := range lines {
		line, _ := l.(map[string]interface{})
		sb.WriteString(strVal(line, "text") + "\n")
	}
	return sb.String(), nil
}

// GetMetrics returns current CPU and RAM usage for a service.
func (c *ShipyardClient) GetMetrics(ctx context.Context, name string) (string, error) {
	data, err := c.get(ctx, "/api/v1/containers/stats")
	if err != nil {
		return "", err
	}

	stats, _ := data["stats"].([]interface{})
	for _, s := range stats {
		stat, _ := s.(map[string]interface{})
		svcName := strVal(stat, "serviceName")
		if svcName == "" {
			svcName = strVal(stat, "containerName")
		}
		if !strings.Contains(strings.ToLower(svcName), strings.ToLower(name)) {
			continue
		}
		cpu := stat["cpuPercent"]
		mem := stat["memUsageMB"]
		memPct := stat["memPercent"]
		return fmt.Sprintf("📊 Metrics for %q:\n  CPU: %.2f%%\n  RAM: %.0f MB (%.1f%%)",
			name, toFloat(cpu), toFloat(mem), toFloat(memPct)), nil
	}
	return fmt.Sprintf("No metrics found for %q. Is it running?", name), nil
}

// ScaleService changes the replica count for a service.
func (c *ShipyardClient) ScaleService(ctx context.Context, name string, replicas int) (string, error) {
	data, err := c.post(ctx, "/api/v1/services/"+name+"/scale", map[string]int{
		"instances": replicas,
	})
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("scale failed: %s", errMsg)
	}
	return fmt.Sprintf("⚡ Scaled %q to %d replica(s).", name, replicas), nil
}

// ListBlueprints returns all catalog blueprints.
func (c *ShipyardClient) ListBlueprints(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v1/catalog")
	if err != nil {
		return "", err
	}

	blueprints, _ := data["blueprints"].([]interface{})
	if len(blueprints) == 0 {
		return "No blueprints in catalog. Save a service to the catalog first.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d blueprint(s) in catalog:\n\n", len(blueprints)))
	for _, b := range blueprints {
		bp, _ := b.(map[string]interface{})
		name := strVal(bp, "name")
		desc := strVal(bp, "description")
		mode := strVal(bp, "importMode")
		sb.WriteString(fmt.Sprintf("• %s (%s)", name, mode))
		if desc != "" {
			sb.WriteString(fmt.Sprintf("\n  %s", desc))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nAvailable power profiles: eco, balanced, performance, max")
	return sb.String(), nil
}

// DeployBlueprint deploys a blueprint from the catalog with a power profile.
func (c *ShipyardClient) DeployBlueprint(ctx context.Context, name, profile string) (string, error) {
	if profile == "" {
		profile = "balanced"
	}

	data, err := c.post(ctx, "/api/v1/catalog/"+name+"/deploy", map[string]string{
		"profile": profile,
	})
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return "", fmt.Errorf("blueprint deploy failed: %s", errMsg)
	}

	return fmt.Sprintf("✅ Blueprint %q deployed with %s profile.\n%s",
		name, profile, strVal(data, "message")), nil
}

// ResolveService gets the Shiplink URL for a service.
func (c *ShipyardClient) ResolveService(ctx context.Context, name string) (string, error) {
	data, err := c.get(ctx, "/api/v1/shiplink/resolve/"+name)
	if err != nil {
		return "", err
	}
	if errMsg := strVal(data, "error"); errMsg != "" {
		return fmt.Sprintf("Service %q is not registered in Shiplink. Is it running?", name), nil
	}

	return fmt.Sprintf("🔗 Shiplink for %q:\n  URL: %s\n  DNS: %s",
		name, strVal(data, "url"), strVal(data, "dns")), nil
}

// GetNodes returns all registered nodes with resource usage.
func (c *ShipyardClient) GetNodes(ctx context.Context) (string, error) {
	data, err := c.get(ctx, "/api/v1/nodes")
	if err != nil {
		return "", err
	}

	nodes, _ := data["nodes"].([]interface{})
	if len(nodes) == 0 {
		return "No nodes registered.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d node(s):\n\n", len(nodes)))
	for _, n := range nodes {
		node, _ := n.(map[string]interface{})
		sb.WriteString(fmt.Sprintf("• %s (%s)\n  CPU: %.1f%% of %v cores\n  RAM: %v MB used / %v MB total\n  Status: %s\n",
			strVal(node, "name"),
			strVal(node, "provider"),
			toFloat(node["cpuPercent"]),
			node["cpuCores"],
			node["memUsedMB"],
			node["memTotalMB"],
			strVal(node, "status"),
		))
	}
	return sb.String(), nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		var f float64
		fmt.Sscanf(x, "%f", &f)
		return f
	}
	return 0
}
