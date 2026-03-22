// Package scheduler implements the 4-phase placement engine.
//
//	Phase 1 — Filter   Hard constraints: resource fit, provider, node selectors
//	Phase 2 — Score    Soft preferences: bin-packing, spread, locality
//	Phase 3 — Bind     Reserve resources on selected node via etcd
//	Phase 4 — Verify   Pre-flight check that the node agent is responsive
package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/shipyard/shipyard/pkg/store"
)

// Request is a placement request for one service instance.
type Request struct {
	ServiceName string
	Mode        string
	Provider    string // docker | compose | kubernetes | nomad — empty means any

	// Resource requirements.
	CPUMillis int64 // e.g. 500 = 0.5 core
	MemoryMB  int64 // e.g. 256

	// Placement hints.
	NodeSelector map[string]string // must match node labels
	SpreadKey    string            // prefer nodes not already running this service
}

// Result is the output of a successful placement.
type Result struct {
	NodeID       string
	NodeName     string
	NodeHostname string
	Score        float64
}

// Scheduler selects a node for a deployment request.
type Scheduler struct {
	store *store.Store
}

// New creates a Scheduler.
func New(st *store.Store) *Scheduler {
	return &Scheduler{store: st}
}

// Place runs all 4 phases and returns the selected node.
func (s *Scheduler) Place(ctx context.Context, req Request) (*Result, error) {
	// ── Phase 1: Filter ───────────────────────────────────────────────────
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("scheduler: failed to list nodes: %w", err)
	}

	if len(nodes) == 0 {
		// No registered nodes — fall back to localhost.
		return localhostResult(), nil
	}

	candidates := s.phase1Filter(nodes, req)
	if len(candidates) == 0 {
		// No nodes pass the filter — fall back to localhost.
		fmt.Printf("scheduler: no nodes passed filter for %q — using localhost\n", req.ServiceName)
		return localhostResult(), nil
	}

	// ── Phase 2: Score ────────────────────────────────────────────────────
	scored := s.phase2Score(candidates, req)

	// ── Phase 3: Bind ─────────────────────────────────────────────────────
	selected := scored[0]
	if err := s.phase3Bind(ctx, selected, req); err != nil {
		return nil, fmt.Errorf("scheduler: bind failed: %w", err)
	}

	// ── Phase 4: Verify ───────────────────────────────────────────────────
	if err := s.phase4Verify(ctx, selected); err != nil {
		fmt.Printf("scheduler: node %q failed verify — falling back to localhost: %v\n", selected.ID, err)
		return localhostResult(), nil
	}

	fmt.Printf("scheduler: placed %q on node %q (score=%.2f)\n", req.ServiceName, selected.Name, scored[0].score)

	return &Result{
		NodeID:       selected.ID,
		NodeName:     selected.Name,
		NodeHostname: selected.Hostname,
		Score:        scored[0].score,
	}, nil
}

// ── Phase 1: Filter ───────────────────────────────────────────────────────

func (s *Scheduler) phase1Filter(nodes []*store.NodeInfo, req Request) []*store.NodeInfo {
	var candidates []*store.NodeInfo
	for _, node := range nodes {
		// Must be healthy.
		if node.Status != store.NodeStatusHealthy {
			continue
		}

		// Must have enough CPU.
		if req.CPUMillis > 0 {
			available := node.Allocatable.CPUMillis - cpuUsedMillis(node)
			if available < req.CPUMillis {
				continue
			}
		}

		// Must have enough memory.
		if req.MemoryMB > 0 {
			availableMem := node.Allocatable.MemoryMB - node.MemUsedMB
			if availableMem < req.MemoryMB {
				continue
			}
		}

		// Must match provider if specified.
		if req.Provider != "" && node.Provider != req.Provider && node.Provider != "" {
			continue
		}

		// Must match node selectors.
		if !matchesSelectors(node, req.NodeSelector) {
			continue
		}

		candidates = append(candidates, node)
	}
	return candidates
}

// ── Phase 2: Score ────────────────────────────────────────────────────────

type scoredNode struct {
	*store.NodeInfo
	score float64
}

func (s *Scheduler) phase2Score(nodes []*store.NodeInfo, req Request) []scoredNode {
	scored := make([]scoredNode, 0, len(nodes))

	for _, node := range nodes {
		score := 0.0

		// Bin-packing (35%): prefer nodes with higher utilisation
		// so we fill up nodes before spreading.
		cpuUtil := node.CPUPercent / 100.0
		memUtil := node.MemPercent / 100.0
		packScore := (cpuUtil + memUtil) / 2.0
		score += packScore * 0.35

		// Spread (25%): prefer nodes NOT already running this service.
		// Simple heuristic: lower CPU = less likely to be running it.
		spreadScore := 1.0 - cpuUtil
		score += spreadScore * 0.25

		// Resource availability (30%): prefer nodes with more free resources.
		freeScore := 1.0 - ((node.CPUPercent + node.MemPercent) / 200.0)
		score += freeScore * 0.30

		// Locality (10%): slight preference for nodes seen recently.
		age := time.Since(node.LastSeenAt).Seconds()
		localityScore := 1.0 - min(age/30.0, 1.0) // full score if seen < 30s ago
		score += localityScore * 0.10

		scored = append(scored, scoredNode{NodeInfo: node, score: score})
	}

	// Sort descending by score.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	return scored
}

// ── Phase 3: Bind ─────────────────────────────────────────────────────────

func (s *Scheduler) phase3Bind(ctx context.Context, node scoredNode, req Request) error {
	// Update node metrics to reflect the reserved resources.
	// This is optimistic — we trust the node agent to report actual usage.
	// A proper implementation would use etcd CAS to atomically subtract capacity.
	reservedMem := node.MemUsedMB + req.MemoryMB
	reservedPct := float64(reservedMem) / float64(node.Allocatable.MemoryMB) * 100
	return s.store.UpdateNodeMetrics(ctx, node.ID, node.CPUPercent, reservedMem, reservedPct)
}

// ── Phase 4: Verify ───────────────────────────────────────────────────────

func (s *Scheduler) phase4Verify(ctx context.Context, node scoredNode) error {
	// Ping the node agent's health endpoint.
	agentURL := fmt.Sprintf("http://%s:9091/healthz", node.Hostname)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(agentURL)
	if err != nil {
		return fmt.Errorf("node agent at %q is unreachable: %w", agentURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("node agent at %q returned %d", agentURL, resp.StatusCode)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────

func localhostResult() *Result {
	return &Result{
		NodeID:       "localhost",
		NodeName:     "localhost",
		NodeHostname: "localhost",
		Score:        1.0,
	}
}

func cpuUsedMillis(node *store.NodeInfo) int64 {
	return int64(node.CPUPercent / 100.0 * float64(node.Allocatable.CPUMillis))
}

func matchesSelectors(node *store.NodeInfo, selectors map[string]string) bool {
	for k, v := range selectors {
		if node.Labels[k] != v {
			return false
		}
	}
	return true
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
