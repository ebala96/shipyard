// Package agent runs the local node agent within the Shipyard server process.
// It registers this machine in etcd and sends heartbeats with live CPU/mem metrics
// every 15 seconds so the scheduler can see available resources.
//
// For multi-node setups, run cmd/shipyard-agent on each worker machine separately.
package agent

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

const heartbeatInterval = 15 * time.Second

// Agent runs the local node heartbeat loop.
type Agent struct {
	st   *store.Store
	bus  *telemetry.Bus
	node *store.NodeInfo
}

// New creates an Agent using already-connected store and bus.
// This avoids opening duplicate connections when running embedded in the server.
func New(st *store.Store, bus *telemetry.Bus) *Agent {
	nodeID := envOr("AGENT_NODE_ID", hostname())
	nodeName := envOr("AGENT_NODE_NAME", hostname())
	region := envOr("AGENT_REGION", "local")
	provider := envOr("AGENT_PROVIDER", "docker")

	node := &store.NodeInfo{
		ID:       nodeID,
		Name:     nodeName,
		Hostname: hostname(),
		Region:   region,
		Provider: provider,
		CPUCores: runtime.NumCPU(),
		MemTotalMB: totalMemoryMB(),
		Labels: map[string]string{
			"region":   region,
			"provider": provider,
		},
		Allocatable: store.NodeResources{
			CPUMillis: int64(runtime.NumCPU() * 1000),
			MemoryMB:  totalMemoryMB(),
		},
	}

	return &Agent{st: st, bus: bus, node: node}
}

// Start begins the heartbeat loop in a background goroutine.
// It returns immediately. Cancel ctx to stop the agent.
func (a *Agent) Start(ctx context.Context) {
	log.Printf("agent: starting — node=%q cpus=%d mem=%dMB",
		a.node.Name, a.node.CPUCores, a.node.MemTotalMB)

	// Start metrics HTTP endpoint.
	go a.serveMetrics(envOr("METRICS_PORT", "9091"))

	// Register immediately.
	if err := a.heartbeat(ctx); err != nil {
		log.Printf("agent: initial heartbeat failed: %v", err)
	}

	// Heartbeat loop.
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Mark node as unknown on shutdown.
				a.node.Status = store.NodeStatusUnknown
				shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = a.st.RegisterNode(shutCtx, a.node)
				cancel()
				log.Printf("agent: stopped")
				return
			case <-ticker.C:
				if err := a.heartbeat(ctx); err != nil {
					log.Printf("agent: heartbeat error: %v", err)
				}
			}
		}
	}()
}

// heartbeat samples live metrics, writes to etcd, and publishes to NATS.
func (a *Agent) heartbeat(ctx context.Context) error {
	cpuPct := sampleCPU()
	memUsed, memPct := sampleMemory(a.node.MemTotalMB)

	a.node.CPUPercent = cpuPct
	a.node.MemUsedMB = memUsed
	a.node.MemPercent = memPct
	a.node.Status = store.NodeStatusHealthy

	if err := a.st.RegisterNode(ctx, a.node); err != nil {
		return fmt.Errorf("etcd register: %w", err)
	}

	if a.bus != nil {
		sample := telemetry.MetricSample{
			ContainerID:   a.node.ID,
			ContainerName: a.node.Name,
			ServiceName:   "node:" + a.node.Name,
			CPUPercent:    cpuPct,
			MemUsageMB:    float64(memUsed),
			MemPercent:    memPct,
		}
		if err := a.bus.PublishMetric(ctx, sample); err != nil {
			log.Printf("agent: NATS metric publish failed: %v", err)
		}
	}

	log.Printf("agent: heartbeat — cpu=%.1f%% mem=%.1f%% (%dMB)", cpuPct, memPct, memUsed)
	return nil
}

// serveMetrics exposes a Prometheus-compatible /metrics endpoint.
func (a *Agent) serveMetrics(port string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		cpuPct := sampleCPU()
		_, memPct := sampleMemory(a.node.MemTotalMB)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "# HELP shipyard_node_cpu_percent CPU utilisation\n")
		fmt.Fprintf(w, "# TYPE shipyard_node_cpu_percent gauge\n")
		fmt.Fprintf(w, "shipyard_node_cpu_percent{node=%q} %.2f\n", a.node.Name, cpuPct)
		fmt.Fprintf(w, "# HELP shipyard_node_memory_percent Memory utilisation\n")
		fmt.Fprintf(w, "# TYPE shipyard_node_memory_percent gauge\n")
		fmt.Fprintf(w, "shipyard_node_memory_percent{node=%q} %.2f\n", a.node.Name, memPct)
	})
	addr := ":" + port
	log.Printf("agent: metrics on http://localhost%s/metrics", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("agent: metrics server error: %v", err)
	}
}

// ── System metrics (Linux /proc) ──────────────────────────────────────────

func sampleCPU() float64 {
	idle1, total1 := readCPUStat()
	time.Sleep(200 * time.Millisecond)
	idle2, total2 := readCPUStat()
	idleDelta := idle2 - idle1
	totalDelta := total2 - total1
	if totalDelta == 0 {
		return 0
	}
	return (1.0 - float64(idleDelta)/float64(totalDelta)) * 100.0
}

func readCPUStat() (idle, total uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 1
	}
	var user, nice, system, idleVal, iowait, irq, softirq uint64
	fmt.Sscanf(string(data), "cpu  %d %d %d %d %d %d %d",
		&user, &nice, &system, &idleVal, &iowait, &irq, &softirq)
	idle = idleVal + iowait
	total = user + nice + system + idleVal + iowait + irq + softirq
	return
}

func sampleMemory(totalMB int64) (int64, float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var totalKB, freeKB, buffersKB, cachedKB uint64
	for _, line := range splitLines(string(data)) {
		fmt.Sscanf(line, "MemTotal: %d kB", &totalKB)
		fmt.Sscanf(line, "MemFree: %d kB", &freeKB)
		fmt.Sscanf(line, "Buffers: %d kB", &buffersKB)
		fmt.Sscanf(line, "Cached: %d kB", &cachedKB)
	}
	usedKB := totalKB - freeKB - buffersKB - cachedKB
	usedMB := int64(usedKB / 1024)
	pct := 0.0
	if totalMB > 0 {
		pct = float64(usedMB) / float64(totalMB) * 100.0
	}
	return usedMB, pct
}

func totalMemoryMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 8192
	}
	var totalKB uint64
	for _, line := range splitLines(string(data)) {
		if n, _ := fmt.Sscanf(line, "MemTotal: %d kB", &totalKB); n == 1 {
			return int64(totalKB / 1024)
		}
	}
	return 8192
}

// ── Helpers ───────────────────────────────────────────────────────────────

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	return lines
}
