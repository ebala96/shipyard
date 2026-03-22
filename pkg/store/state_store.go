package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ── Stack lifecycle states ────────────────────────────────────────────────

// StackLifecycle represents the lifecycle state of a deployed stack.
type StackLifecycle string

const (
	StatePending      StackLifecycle = "pending"
	StateDeploying    StackLifecycle = "deploying"
	StateRunning      StackLifecycle = "running"
	StateStopping     StackLifecycle = "stopping"
	StateStopped      StackLifecycle = "stopped"
	StateDown         StackLifecycle = "down"      // containers removed, volumes + record kept
	StateRestarting   StackLifecycle = "restarting"
	StateFailed       StackLifecycle = "failed"
	StateRollingBack  StackLifecycle = "rolling-back"
	StateDestroyed    StackLifecycle = "destroyed"
)

// IsTerminal returns true for states where no further reconciliation should occur.
func (s StackLifecycle) IsTerminal() bool {
	return s == StateDestroyed
}

// IsPauseable returns true for states where Down/Stop make sense.
func (s StackLifecycle) IsPauseable() bool {
	return s == StateRunning
}

// ── Stack state ───────────────────────────────────────────────────────────

// StackState holds the full lifecycle state of a deployed stack.
// Stored at /shipyard/stacks/{name}/state.
type StackState struct {
	Name        string         `json:"name"`
	ServiceName string         `json:"serviceName"`
	Platform    string         `json:"platform"` // docker, compose, kubernetes, etc.
	Mode        string         `json:"mode"`
	StackName   string         `json:"stackName"`
	Node        string         `json:"node,omitempty"` // node this service runs on

	// Lifecycle
	State         StackLifecycle `json:"state"`
	StateAt       time.Time      `json:"stateAt"`
	RetryCount    int            `json:"retryCount"`
	FailReason    string         `json:"failReason,omitempty"`
	LastOperation string         `json:"lastOperation,omitempty"`

	// Runtime info
	Containers  []ContainerRecord `json:"containers"`

	// Timestamps
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ContainerRecord holds the runtime details of a single container instance.
type ContainerRecord struct {
	ContainerID   string         `json:"containerID"`
	ContainerName string         `json:"containerName"`
	ServiceName   string         `json:"serviceName"`
	Mode          string         `json:"mode"`
	Status        string         `json:"status"` // running, exited, etc.
	Image         string         `json:"image"`
	Ports         map[string]int `json:"ports"`
	IDEInstance   *IDERecord     `json:"ide,omitempty"`
	VNCInstance   *VNCRecord     `json:"vnc,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
}

// IDERecord holds details of a running code-server sidecar.
type IDERecord struct {
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	HostPort      int    `json:"hostPort"`
	DirectURL     string `json:"directURL"`
}

// VNCRecord holds details of a running noVNC sidecar.
type VNCRecord struct {
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	HostPort      int    `json:"hostPort"`
	URL           string `json:"url"`
}

// ── Stack state operations ─────────────────────────────────────────────────

// PutStackState writes a StackState to etcd.
func (s *Store) PutStackState(ctx context.Context, state *StackState) error {
	state.UpdatedAt = time.Now()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now()
	}
	return s.put(ctx, stackStateKey(state.Name), state, 0)
}

// GetStackState retrieves the state for a named stack.
// Returns (nil, nil) if not found.
func (s *Store) GetStackState(ctx context.Context, name string) (*StackState, error) {
	var state StackState
	found, err := s.get(ctx, stackStateKey(name), &state)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &state, nil
}

// ListStackStates returns all known stack states.
func (s *Store) ListStackStates(ctx context.Context) ([]*StackState, error) {
	var states []*StackState
	err := s.list(ctx, prefixStacks, func(key string, raw []byte) error {
		// Only process /state keys, not /ledger/* keys.
		if !hasStateSuffix(key) {
			return nil
		}
		var st StackState
		if err := unmarshalJSON(raw, &st); err != nil {
			fmt.Printf("store: skipping malformed stack state at %q: %v\n", key, err)
			return nil
		}
		states = append(states, &st)
		return nil
	})
	return states, err
}

// TransitionState updates the lifecycle state of a stack.
func (s *Store) TransitionState(ctx context.Context, name string, newState StackLifecycle, reason string) error {
	state, err := s.GetStackState(ctx, name)
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("store: stack %q not found", name)
	}

	old := state.State
	state.State = newState
	state.StateAt = time.Now()
	if reason != "" {
		state.FailReason = reason
	}

	if err := s.PutStackState(ctx, state); err != nil {
		return err
	}

	fmt.Printf("store: stack %q: %s → %s\n", name, old, newState)
	return nil
}

// DeleteStackState removes the state for a stack (used on Destroy).
func (s *Store) DeleteStackState(ctx context.Context, name string) error {
	// Delete state + all ledger entries.
	return s.delPrefix(ctx, prefixStacks+name+"/")
}

// ── Version ledger ────────────────────────────────────────────────────────

// LedgerEntry records a snapshot of a stack state at a point in time.
// Used for rollback.
type LedgerEntry struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"` // Unix nano timestamp as string
	State       *StackState    `json:"state"`
	Containers  []ContainerRecord `json:"containers"`
	RecordedAt  time.Time      `json:"recordedAt"`
	Operation   string         `json:"operation"` // deploy, scale, restart, etc.
	Operator    string         `json:"operator"`  // "user" or "reconciler"
}

// WriteLedgerEntry appends a version entry for a stack.
func (s *Store) WriteLedgerEntry(ctx context.Context, entry *LedgerEntry) error {
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = time.Now()
	}
	entry.Version = fmt.Sprintf("%d", entry.RecordedAt.UnixNano())
	key := ledgerKey(entry.Name, entry.RecordedAt)
	return s.put(ctx, key, entry, 0)
}

// ListLedgerEntries returns all version entries for a stack, newest first.
func (s *Store) ListLedgerEntries(ctx context.Context, stackName string) ([]*LedgerEntry, error) {
	prefix := fmt.Sprintf("%s%s/ledger/", prefixStacks, stackName)
	var entries []*LedgerEntry
	err := s.list(ctx, prefix, func(key string, raw []byte) error {
		var entry LedgerEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil
		}
		entries = append(entries, &entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Reverse so newest is first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// GetLedgerEntry retrieves a specific version by timestamp string.
func (s *Store) GetLedgerEntry(ctx context.Context, stackName, version string) (*LedgerEntry, error) {
	prefix := fmt.Sprintf("%s%s/ledger/", prefixStacks, stackName)
	var found *LedgerEntry
	err := s.list(ctx, prefix, func(key string, raw []byte) error {
		var entry LedgerEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil
		}
		if entry.Version == version {
			found = &entry
		}
		return nil
	})
	return found, err
}

// ── Helpers ───────────────────────────────────────────────────────────────

func unmarshalJSON(raw []byte, v interface{}) error {
	return json.Unmarshal(raw, v)
}

func hasStateSuffix(key string) bool {
	return len(key) > 6 && key[len(key)-6:] == "/state"
}
