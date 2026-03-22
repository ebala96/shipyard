package statemachine

import (
	"context"
	"fmt"
	"time"

	"github.com/shipyard/shipyard/pkg/orchestrator"
	"github.com/shipyard/shipyard/pkg/store"
	"github.com/shipyard/shipyard/pkg/telemetry"
)

const maxRetries = 3

// Executor applies lifecycle operations to a stack —
// validates the transition, executes Docker ops, writes state back to etcd,
// and publishes NATS events.
type Executor struct {
	store *store.Store
	orch  *orchestrator.Orchestrator
	bus   *telemetry.Bus
}

// NewExecutor creates an Executor.
func NewExecutor(st *store.Store, orch *orchestrator.Orchestrator, bus *telemetry.Bus) *Executor {
	return &Executor{store: st, orch: orch, bus: bus}
}

// Apply validates the operation against the current state, executes it,
// and persists the new state to etcd.
func (e *Executor) Apply(ctx context.Context, stackName string, op Operation) error {
	// Load current state.
	state, err := e.store.GetStackState(ctx, stackName)
	if err != nil {
		return fmt.Errorf("executor: failed to load state for %q: %w", stackName, err)
	}
	if state == nil {
		return fmt.Errorf("executor: stack %q not found", stackName)
	}

	// Validate transition.
	nextState, err := Transition(state.State, op)
	if err != nil {
		return err
	}

	// Transition to intermediate state immediately (e.g. stopping, restarting).
	intermediateState := intermediateFor(op)
	if intermediateState != "" {
		if err := e.store.TransitionState(ctx, stackName, intermediateState, ""); err != nil {
			return fmt.Errorf("executor: failed to set intermediate state: %w", err)
		}
	}

	// Record the operation before executing so the reconciler knows what happened.
	state.LastOperation = string(op)
	_ = e.store.PutStackState(ctx, state)

	// Execute the Docker operation.
	containerIDs := extractContainerIDs(state.Containers)
	execErr := e.execute(ctx, state, op, containerIDs)

	if execErr != nil {
		// Transition to failed on error.
		reason := execErr.Error()
		_ = e.store.TransitionState(ctx, stackName, store.StateFailed, reason)
		e.publishEvent(ctx, state, op, "failed", reason)
		return fmt.Errorf("executor: %s failed for %q: %w", op, stackName, execErr)
	}

	// Transition to final state.
	if err := e.store.TransitionState(ctx, stackName, nextState, ""); err != nil {
		return fmt.Errorf("executor: failed to persist final state: %w", err)
	}

	// Write ledger entry.
	e.writeLedger(ctx, stackName, state, op)

	// Publish success event.
	e.publishEvent(ctx, state, op, "success", "")

	return nil
}

// RetryFailed retries a failed stack up to maxRetries times.
// Returns true if the retry was attempted.
func (e *Executor) RetryFailed(ctx context.Context, state *store.StackState, deployReq orchestrator.DeployRequest) (bool, error) {
	if state.State != store.StateFailed {
		return false, nil
	}
	if state.RetryCount >= maxRetries {
		fmt.Printf("statemachine: stack %q has failed %d times — parking in failed state\n",
			state.Name, state.RetryCount)
		return false, nil
	}

	// Increment retry count.
	state.RetryCount++
	state.State = store.StateDeploying
	state.StateAt = time.Now()
	state.FailReason = ""
	if err := e.store.PutStackState(ctx, state); err != nil {
		return false, err
	}

	fmt.Printf("statemachine: retrying %q (attempt %d/%d)\n", state.Name, state.RetryCount, maxRetries)

	deployed, err := e.orch.Deploy(ctx, deployReq)
	if err != nil {
		state.State = store.StateFailed
		state.FailReason = err.Error()
		state.StateAt = time.Now()
		_ = e.store.PutStackState(ctx, state)
		return true, err
	}

	// Update state with new container info.
	state.State = store.StateRunning
	state.StateAt = time.Now()
	state.Containers = []store.ContainerRecord{{
		ContainerID:   deployed.ContainerID,
		ContainerName: deployed.ContainerName,
		ServiceName:   deployed.ServiceName,
		Mode:          deployed.Mode,
		Status:        "running",
		Image:         deployed.ImageTag,
		Ports:         deployed.Ports,
		CreatedAt:     time.Now(),
	}}
	return true, e.store.PutStackState(ctx, state)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (e *Executor) execute(ctx context.Context, state *store.StackState, op Operation, containerIDs []string) error {
	switch op {
	case OpStop:
		for _, id := range containerIDs {
			if err := e.orch.Stop(ctx, id); err != nil {
				return err
			}
		}
		// Update container statuses.
		for i := range state.Containers {
			state.Containers[i].Status = "stopped"
		}
		return e.store.PutStackState(ctx, state)

	case OpStart:
		for _, id := range containerIDs {
			if err := e.orch.Start(ctx, id); err != nil {
				return err
			}
		}
		for i := range state.Containers {
			state.Containers[i].Status = "running"
		}
		return e.store.PutStackState(ctx, state)

	case OpRestart:
		for _, id := range containerIDs {
			if err := e.orch.Restart(ctx, id); err != nil {
				return err
			}
		}
		return nil

	case OpDown:
		return e.orch.Down(ctx, state.Name, containerIDs)

	case OpDestroy:
		if err := e.orch.Destroy(ctx, state.Name, containerIDs); err != nil {
			return err
		}
		return e.store.DeleteStackState(ctx, state.Name)

	default:
		return fmt.Errorf("executor: unhandled operation %q", op)
	}
}

func (e *Executor) writeLedger(ctx context.Context, stackName string, state *store.StackState, op Operation) {
	entry := &store.LedgerEntry{
		Name:       stackName,
		State:      state,
		Containers: state.Containers,
		Operation:  string(op),
		Operator:   "user",
	}
	if err := e.store.WriteLedgerEntry(ctx, entry); err != nil {
		fmt.Printf("statemachine: ledger write failed for %q: %v\n", stackName, err)
	}
}

func (e *Executor) publishEvent(ctx context.Context, state *store.StackState, op Operation, status, errMsg string) {
	if e.bus == nil {
		return
	}
	event := telemetry.Event{
		Type:        string(op),
		ServiceName: state.ServiceName,
		StackName:   state.StackName,
		Status:      status,
		Operator:    "user",
	}
	if errMsg != "" {
		event.Error = errMsg
	}
	if len(state.Containers) > 0 {
		event.ContainerID = state.Containers[0].ContainerID
	}
	if err := e.bus.PublishEvent(ctx, event); err != nil {
		fmt.Printf("statemachine: NATS publish failed: %v\n", err)
	}
}

func extractContainerIDs(containers []store.ContainerRecord) []string {
	ids := make([]string, 0, len(containers))
	for _, c := range containers {
		if c.ContainerID != "" {
			ids = append(ids, c.ContainerID)
		}
	}
	return ids
}

func intermediateFor(op Operation) store.StackLifecycle {
	switch op {
	case OpStop:
		return store.StateStopping
	case OpRestart:
		return store.StateRestarting
	case OpRollback:
		return store.StateRollingBack
	default:
		return ""
	}
}
