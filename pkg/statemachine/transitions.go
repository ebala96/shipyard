// Package statemachine defines the valid lifecycle state transitions for
// Shipyard stacks and enforces guards so invalid transitions are rejected.
//
// State diagram:
//
//	pending → deploying → running ──┬── stopping → stopped
//	    ↑         ↓                 ├── restarting → running
//	    │       failed              ├── down        (containers removed, record kept)
//	    │         ↓                 └── destroyed   (everything removed)
//	    └── (retry ≤3) ─────────────
package statemachine

import (
	"fmt"

	"github.com/shipyard/shipyard/pkg/store"
)

// Transition validates and returns the next state for an operation.
// Returns an error if the transition is not allowed from the current state.
func Transition(current store.StackLifecycle, op Operation) (store.StackLifecycle, error) {
	allowed, next, ok := rule(current, op)
	if !ok {
		return current, fmt.Errorf("statemachine: cannot %q a stack in state %q (allowed: %v)",
			op, current, allowedOps(current))
	}
	_ = allowed
	return next, nil
}

// CanTransition returns true if the operation is valid from the current state.
func CanTransition(current store.StackLifecycle, op Operation) bool {
	_, _, ok := rule(current, op)
	return ok
}

// Operation is a lifecycle operation requested by the user or reconciler.
type Operation string

const (
	OpDeploy   Operation = "deploy"
	OpStop     Operation = "stop"
	OpStart    Operation = "start"
	OpRestart  Operation = "restart"
	OpDown     Operation = "down"
	OpDestroy  Operation = "destroy"
	OpRollback Operation = "rollback"
	OpFail     Operation = "fail"     // internal — marks deploy as failed
	OpRecover  Operation = "recover"  // internal — retry after failure
)

// transitionTable maps (currentState, operation) → nextState.
var transitionTable = map[store.StackLifecycle]map[Operation]store.StackLifecycle{
	store.StatePending: {
		OpDeploy:  store.StateDeploying,
		OpDestroy: store.StateDestroyed,
	},
	store.StateDeploying: {
		OpFail:    store.StateFailed,
		OpDestroy: store.StateDestroyed,
	},
	store.StateRunning: {
		OpStop:     store.StateStopped,   // intermediate=stopping, final=stopped
		OpRestart:  store.StateRunning,   // intermediate=restarting, final=running
		OpDown:     store.StateDown,
		OpDestroy:  store.StateDestroyed,
		OpRollback: store.StateDeploying,
		OpDeploy:   store.StateDeploying,
	},
	store.StateStopping: {
		OpFail: store.StateFailed,
	},
	store.StateStopped: {
		OpStart:   store.StateRunning,
		OpDeploy:  store.StateDeploying,
		OpDestroy: store.StateDestroyed,
	},
	store.StateRestarting: {
		OpFail: store.StateFailed,
	},
	store.StateDown: {
		OpStart:   store.StateDeploying, // re-deploy from scratch
		OpDeploy:  store.StateDeploying,
		OpDestroy: store.StateDestroyed,
	},
	store.StateFailed: {
		OpRecover: store.StateDeploying, // retry
		OpDestroy: store.StateDestroyed,
		OpRollback: store.StateDeploying,
	},
	store.StateRollingBack: {
		OpFail: store.StateFailed,
	},
}

func rule(current store.StackLifecycle, op Operation) (store.StackLifecycle, store.StackLifecycle, bool) {
	ops, ok := transitionTable[current]
	if !ok {
		return current, current, false
	}
	next, ok := ops[op]
	return current, next, ok
}

func allowedOps(current store.StackLifecycle) []Operation {
	ops, ok := transitionTable[current]
	if !ok {
		return nil
	}
	result := make([]Operation, 0, len(ops))
	for op := range ops {
		result = append(result, op)
	}
	return result
}