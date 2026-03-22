package scaler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/shipyard/shipyard/pkg/lb"
	"github.com/shipyard/shipyard/pkg/shipfile"
)

// ScaleEvent describes a single scale-up or scale-down decision.
type ScaleEvent struct {
	ServiceKey string
	Direction  string // "up" or "down"
	From       int
	To         int
	Reason     string
	At         time.Time
}

// ServiceScaleConfig is registered per service when it is deployed.
type ServiceScaleConfig struct {
	ServiceKey  string
	StackName   string
	ServiceName string
	Mode        string
	Config      shipfile.ScaleConfig
	// InstanceIDs holds the Docker container IDs currently running.
	InstanceIDs []string
}

// ScaleFunc is called by the autoscaler when it decides to scale.
// The caller (orchestrator) implements this to add or remove instances.
type ScaleFunc func(ctx context.Context, cfg *ServiceScaleConfig, delta int) error

// Autoscaler watches registered services and scales them based on CPU/RAM thresholds.
type Autoscaler struct {
	mu       sync.RWMutex
	services map[string]*ServiceScaleConfig // serviceKey -> config

	collector *StatsCollector
	pool      *lb.Pool
	scaleFunc ScaleFunc

	interval time.Duration
	events   []ScaleEvent

	// cooldowns tracks when each service last scaled to prevent thrashing.
	cooldowns map[string]time.Time

	cancel context.CancelFunc
}

// New creates an Autoscaler.
// interval is how often it polls stats (e.g. 30 * time.Second).
// scaleFunc is called when a scale decision is made.
func New(pool *lb.Pool, scaleFunc ScaleFunc, interval time.Duration) (*Autoscaler, error) {
	collector, err := NewStatsCollector()
	if err != nil {
		return nil, err
	}

	return &Autoscaler{
		services:  make(map[string]*ServiceScaleConfig),
		collector: collector,
		pool:      pool,
		scaleFunc: scaleFunc,
		interval:  interval,
		cooldowns: make(map[string]time.Time),
	}, nil
}

// Register adds a service to the autoscaler's watch list.
func (a *Autoscaler) Register(cfg *ServiceScaleConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.services[cfg.ServiceKey] = cfg
}

// Deregister removes a service from the watch list.
func (a *Autoscaler) Deregister(serviceKey string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.services, serviceKey)
	delete(a.cooldowns, serviceKey)
}

// UpdateInstances updates the list of running container IDs for a service.
// Called by the orchestrator when instances are added or removed.
func (a *Autoscaler) UpdateInstances(serviceKey string, containerIDs []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cfg, ok := a.services[serviceKey]; ok {
		cfg.InstanceIDs = containerIDs
	}
}

// Start begins the autoscaler polling loop in a background goroutine.
func (a *Autoscaler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	go a.loop(ctx)
	log.Printf("autoscaler: started, polling every %s", a.interval)
}

// Stop shuts down the autoscaler polling loop.
func (a *Autoscaler) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
}

// Events returns a copy of all scale events that have occurred.
func (a *Autoscaler) Events() []ScaleEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]ScaleEvent, len(a.events))
	copy(result, a.events)
	return result
}

// loop is the main polling goroutine.
func (a *Autoscaler) loop(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.evaluate(ctx)
		}
	}
}

// evaluate checks every registered service and scales if needed.
func (a *Autoscaler) evaluate(ctx context.Context) {
	a.mu.RLock()
	services := make([]*ServiceScaleConfig, 0, len(a.services))
	for _, cfg := range a.services {
		services = append(services, cfg)
	}
	a.mu.RUnlock()

	for _, cfg := range services {
		if !cfg.Config.Autoscale.Enabled {
			continue
		}
		a.evaluateService(ctx, cfg)
	}
}

// evaluateService checks a single service and scales it if thresholds are exceeded.
func (a *Autoscaler) evaluateService(ctx context.Context, cfg *ServiceScaleConfig) {
	// Respect cooldown — don't scale within cooldownSecs of the last event.
	cooldown := time.Duration(cfg.Config.Autoscale.CooldownSecs) * time.Second
	if cooldown == 0 {
		cooldown = 60 * time.Second // default
	}

	a.mu.RLock()
	lastScale, hasCooldown := a.cooldowns[cfg.ServiceKey]
	a.mu.RUnlock()

	if hasCooldown && time.Since(lastScale) < cooldown {
		return // still in cooldown
	}

	// Sample stats across all running instances.
	avgCPU, avgMem, _ := a.collector.SampleAll(ctx, cfg.InstanceIDs)

	current := len(cfg.InstanceIDs)
	as := cfg.Config.Autoscale

	// Determine if we need to scale.
	var delta int
	var reason string

	switch {
	case avgCPU > float64(as.TargetCPU) && current < as.Max:
		delta = 1
		reason = fmt.Sprintf("CPU %.1f%% > target %d%%", avgCPU, as.TargetCPU)

	case avgMem > float64(as.TargetMemory) && current < as.Max:
		delta = 1
		reason = fmt.Sprintf("memory %.1f%% > target %d%%", avgMem, as.TargetMemory)

	case avgCPU < float64(as.TargetCPU)/2 &&
		avgMem < float64(as.TargetMemory)/2 &&
		current > as.Min:
		delta = -1
		reason = fmt.Sprintf("CPU %.1f%% and mem %.1f%% well below targets — scaling down", avgCPU, avgMem)
	}

	if delta == 0 {
		return // no action needed
	}

	log.Printf("autoscaler: scaling %s %+d — %s (current: %d instances)",
		cfg.ServiceKey, delta, reason, current)

	if err := a.scaleFunc(ctx, cfg, delta); err != nil {
		log.Printf("autoscaler: scale error for %s: %v", cfg.ServiceKey, err)
		return
	}

	// Record the event and set cooldown.
	event := ScaleEvent{
		ServiceKey: cfg.ServiceKey,
		Direction:  "up",
		From:       current,
		To:         current + delta,
		Reason:     reason,
		At:         time.Now(),
	}
	if delta < 0 {
		event.Direction = "down"
	}

	a.mu.Lock()
	a.events = append(a.events, event)
	a.cooldowns[cfg.ServiceKey] = time.Now()
	a.mu.Unlock()
}
