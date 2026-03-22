package lb

import (
	"fmt"
	"sync"
)

// Pool manages one LoadBalancer per service.
// The orchestrator calls Pool to register and deregister backends
// as instances are created and destroyed.
type Pool struct {
	mu  sync.RWMutex
	lbs map[string]*LoadBalancer // serviceKey -> LoadBalancer
}

// NewPool creates an empty Pool.
func NewPool() *Pool {
	return &Pool{lbs: make(map[string]*LoadBalancer)}
}

// Register creates a load balancer for a service if one doesn't exist,
// then adds the given backend to it.
// serviceKey is typically "stackName/serviceName" or just "serviceName".
// backendName is the container/instance name.
// backendURL is the full URL e.g. "http://localhost:3001".
func (p *Pool) Register(serviceKey, backendName, backendURL, strategy string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.lbs[serviceKey]; !ok {
		p.lbs[serviceKey] = New(Strategy(strategy))
	}

	return p.lbs[serviceKey].AddBackend(backendName, backendURL)
}

// Deregister removes a backend from the load balancer for a service.
func (p *Pool) Deregister(serviceKey, backendName string) {
	p.mu.RLock()
	lb, ok := p.lbs[serviceKey]
	p.mu.RUnlock()

	if ok {
		lb.RemoveBackend(backendName)
	}
}

// Get returns the LoadBalancer for a service.
// Returns an error if no load balancer exists for that service key.
func (p *Pool) Get(serviceKey string) (*LoadBalancer, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	lb, ok := p.lbs[serviceKey]
	if !ok {
		return nil, fmt.Errorf("lb pool: no load balancer registered for service %q", serviceKey)
	}
	return lb, nil
}

// Remove removes the entire load balancer for a service (all backends).
func (p *Pool) Remove(serviceKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.lbs, serviceKey)
}

// SetHealthy marks a specific backend as healthy or unhealthy.
func (p *Pool) SetHealthy(serviceKey, backendName string, healthy bool) {
	p.mu.RLock()
	lb, ok := p.lbs[serviceKey]
	p.mu.RUnlock()

	if ok {
		lb.SetHealthy(backendName, healthy)
	}
}

// Status returns a snapshot of all load balancers and their backends.
func (p *Pool) Status() map[string][]BackendInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string][]BackendInfo, len(p.lbs))
	for key, lb := range p.lbs {
		result[key] = lb.Backends()
	}
	return result
}

// ServiceKey returns a consistent key for a service.
func ServiceKey(stackName, serviceName string) string {
	if stackName != "" {
		return stackName + "/" + serviceName
	}
	return serviceName
}
