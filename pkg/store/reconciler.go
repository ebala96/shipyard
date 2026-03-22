package store

import (
	"context"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ReconcileFunc is called when a stack's desired state changes.
// The implementation should converge actual state toward desired.
type ReconcileFunc func(ctx context.Context, state *StackState) error

// Watcher watches etcd for state changes and triggers reconciliation.
type Watcher struct {
	store       *Store
	reconcile   ReconcileFunc
	interval    time.Duration
}

// NewWatcher creates a Watcher.
// interval is how often to do a full reconciliation sweep (e.g. 30s).
// reconcile is called on every change + on every periodic sweep.
func NewWatcher(store *Store, reconcile ReconcileFunc, interval time.Duration) *Watcher {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &Watcher{
		store:     store,
		reconcile: reconcile,
		interval:  interval,
	}
}

// Start begins the reconciliation loop.
// It returns immediately and runs in background goroutines.
// Call the returned cancel func to stop.
func (w *Watcher) Start(ctx context.Context) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)

	// Watch loop — fires immediately on any stack state change.
	go w.watchLoop(ctx)

	// Periodic sweep — reconciles all stacks every interval.
	go w.periodicSweep(ctx)

	fmt.Printf("store: reconciler started (sweep every %s)\n", w.interval)
	return cancel
}

// watchLoop watches etcd for changes to any stack state key.
func (w *Watcher) watchLoop(ctx context.Context) {
	ch := w.store.Watch(ctx, prefixStacks)
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-ch:
			if !ok {
				return
			}
			for _, event := range resp.Events {
				if event.Type != clientv3.EventTypePut {
					continue
				}
				key := string(event.Kv.Key)
				if !hasStateSuffix(key) {
					continue // skip ledger entries
				}

				var state StackState
				if err := unmarshalJSON(event.Kv.Value, &state); err != nil {
					fmt.Printf("store: watcher: malformed state at %q: %v\n", key, err)
					continue
				}

				if state.State.IsTerminal() {
					continue // don't reconcile destroyed stacks
				}

				go func(st StackState) {
					rCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					if err := w.reconcile(rCtx, &st); err != nil {
						fmt.Printf("store: reconcile error for %q: %v\n", st.Name, err)
					}
				}(state)
			}
		}
	}
}

// periodicSweep reconciles all non-terminal stacks every interval.
func (w *Watcher) periodicSweep(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			states, err := w.store.ListStackStates(ctx)
			if err != nil {
				fmt.Printf("store: periodic sweep list error: %v\n", err)
				continue
			}
			for _, state := range states {
				if state.State.IsTerminal() {
					continue
				}
				rCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				if err := w.reconcile(rCtx, state); err != nil {
					fmt.Printf("store: periodic reconcile error for %q: %v\n", state.Name, err)
				}
				cancel()
			}
		}
	}
}
