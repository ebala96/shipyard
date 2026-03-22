package store

import (
	"context"
	"fmt"
	"time"
)

const (
	prefixNodes  = "/shipyard/nodes/"
	nodeLeaseTTL = 30 // seconds — node must heartbeat within this window
)

// NodeStatus represents the health of a node.
type NodeStatus string

const (
	NodeStatusHealthy  NodeStatus = "healthy"
	NodeStatusDegraded NodeStatus = "degraded"
	NodeStatusUnknown  NodeStatus = "unknown"
)

// NodeInfo holds runtime details about a registered node.
// Stored at /shipyard/nodes/{id} with a 30s TTL lease.
type NodeInfo struct {
	ID       string     `json:"id"`       // unique node identifier (hostname)
	Name     string     `json:"name"`     // human-readable name
	Hostname string     `json:"hostname"`
	Region   string     `json:"region"`
	Provider string     `json:"provider"` // docker, k8s, nomad, etc.

	// Resources
	CPUCores    int     `json:"cpuCores"`
	MemTotalMB  int64   `json:"memTotalMB"`
	DiskTotalGB int64   `json:"diskTotalGB"`

	// Live metrics (updated every heartbeat)
	CPUPercent  float64    `json:"cpuPercent"`
	MemUsedMB   int64      `json:"memUsedMB"`
	MemPercent  float64    `json:"memPercent"`

	// State
	Status      NodeStatus `json:"status"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	RegisteredAt time.Time `json:"registeredAt"`

	// Scheduling
	Labels      map[string]string `json:"labels,omitempty"`
	Taints      []string          `json:"taints,omitempty"`
	Allocatable NodeResources     `json:"allocatable"` // total - system reserved
}

// NodeResources represents schedulable resource capacity.
type NodeResources struct {
	CPUMillis  int64 `json:"cpuMillis"`  // 1000 = 1 core
	MemoryMB   int64 `json:"memoryMB"`
}

// RegisterNode writes a node record to etcd with a TTL lease.
// Must be called repeatedly (heartbeat) before the TTL expires.
func (s *Store) RegisterNode(ctx context.Context, node *NodeInfo) error {
	if node.RegisteredAt.IsZero() {
		node.RegisteredAt = time.Now()
	}
	node.LastSeenAt = time.Now()
	if node.Status == "" {
		node.Status = NodeStatusHealthy
	}
	key := prefixNodes + node.ID
	return s.put(ctx, key, node, nodeLeaseTTL)
}

// GetNode retrieves a node by ID.
func (s *Store) GetNode(ctx context.Context, id string) (*NodeInfo, error) {
	var node NodeInfo
	found, err := s.get(ctx, prefixNodes+id, &node)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &node, nil
}

// ListNodes returns all currently registered (alive) nodes.
// Dead nodes disappear automatically when their TTL lease expires.
func (s *Store) ListNodes(ctx context.Context) ([]*NodeInfo, error) {
	var nodes []*NodeInfo
	err := s.list(ctx, prefixNodes, func(key string, raw []byte) error {
		var node NodeInfo
		if err := unmarshalJSON(raw, &node); err != nil {
			fmt.Printf("store: skipping malformed node at %q: %v\n", key, err)
			return nil
		}
		nodes = append(nodes, &node)
		return nil
	})
	return nodes, err
}

// UpdateNodeMetrics updates just the live metric fields of a node record.
func (s *Store) UpdateNodeMetrics(ctx context.Context, id string, cpuPct float64, memUsedMB int64, memPct float64) error {
	node, err := s.GetNode(ctx, id)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("store: node %q not found", id)
	}
	node.CPUPercent = cpuPct
	node.MemUsedMB = memUsedMB
	node.MemPercent = memPct
	node.LastSeenAt = time.Now()
	return s.RegisterNode(ctx, node)
}
