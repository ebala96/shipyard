package shiplink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/shipyard/shipyard/pkg/store"
)

// AutoRegistrar watches etcd stack states and automatically registers/deregisters
// service endpoints in the Shiplink registry as containers start and stop.
type AutoRegistrar struct {
	registry *Registry
	store    *store.Store
}

// NewAutoRegistrar creates an AutoRegistrar.
func NewAutoRegistrar(registry *Registry, st *store.Store) *AutoRegistrar {
	return &AutoRegistrar{registry: registry, store: st}
}

// Start begins watching etcd for stack state changes and syncs the registry.
// It also does an initial sync of all currently running stacks.
func (a *AutoRegistrar) Start(ctx context.Context) {
	// Initial sync.
	if err := a.syncAll(ctx); err != nil {
		log.Printf("shiplink: initial sync failed: %v", err)
	}

	// Watch for changes.
	go a.watchLoop(ctx)
	log.Printf("shiplink: auto-registrar started")
}

// RegisterFromState registers all running containers from a StackState.
func (a *AutoRegistrar) RegisterFromState(ctx context.Context, state *store.StackState) error {
	if state.State != store.StateRunning {
		// Deregister all containers for this stack.
		for _, ctr := range state.Containers {
			_ = a.registry.Deregister(ctx, state.ServiceName, ctr.ContainerID)
		}
		return nil
	}

	for _, ctr := range state.Containers {
		if ctr.ContainerID == "" || ctr.Status != "running" {
			continue
		}

		// Pick the first exposed port as the service port.
		port := 0
		for _, p := range ctr.Ports {
			if p > 0 {
				port = p
				break
			}
		}
		if port == 0 {
			continue // no exposed port — skip
		}

		ep := &Endpoint{
			ServiceName: state.ServiceName,
			ContainerID: ctr.ContainerID,
			Host:        "localhost",
			Port:        port,
			Mode:        ctr.Mode,
			Weight:      100,
		}

		if err := a.registry.Register(ctx, ep); err != nil {
			log.Printf("shiplink: failed to register %q: %v", state.ServiceName, err)
			continue
		}

		log.Printf("shiplink: registered %s → %s:%d (%s)",
			DNSName(state.ServiceName), ep.Host, ep.Port, ctr.ContainerID[:12])
	}
	return nil
}

// syncAll registers all currently running stacks.
func (a *AutoRegistrar) syncAll(ctx context.Context) error {
	states, err := a.store.ListStackStates(ctx)
	if err != nil {
		return err
	}

	registered := 0
	for _, state := range states {
		if err := a.RegisterFromState(ctx, state); err != nil {
			log.Printf("shiplink: sync error for %q: %v", state.Name, err)
			continue
		}
		if state.State == store.StateRunning {
			registered++
		}
	}

	log.Printf("shiplink: synced %d running services into registry", registered)
	return nil
}

// watchLoop watches etcd and updates the registry on every stack state change.
func (a *AutoRegistrar) watchLoop(ctx context.Context) {
	ch := a.store.Watch(ctx, "/shipyard/stacks/")
	for {
		select {
		case <-ctx.Done():
			return
		case resp, ok := <-ch:
			if !ok {
				return
			}
			for _, event := range resp.Events {
				key := string(event.Kv.Key)
				if !strings.HasSuffix(key, "/state") {
					continue
				}

				var state store.StackState
				if err := json.Unmarshal(event.Kv.Value, &state); err != nil {
					continue
				}

				rCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := a.RegisterFromState(rCtx, &state); err != nil {
					fmt.Printf("shiplink: watch update failed for %q: %v\n", state.Name, err)
				}
				cancel()
			}
		}
	}
}
