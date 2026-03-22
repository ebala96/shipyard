package lb

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
)

// Strategy defines the load balancing algorithm.
type Strategy string

const (
	RoundRobin Strategy = "round-robin"
	LeastConn  Strategy = "least-conn"
	IPHash     Strategy = "ip-hash"
)

// Backend represents a single upstream instance.
type Backend struct {
	URL          *url.URL
	Name         string // e.g. "shipyard_myapi_production_1"
	Healthy      bool
	ActiveConns  int64  // used by least-conn strategy
	proxy        *httputil.ReverseProxy
}

// LoadBalancer distributes HTTP traffic across a pool of backends.
// It implements http.Handler so it can be mounted directly in Gin.
type LoadBalancer struct {
	mu       sync.RWMutex
	backends []*Backend
	strategy Strategy
	counter  uint64 // atomic counter for round-robin
}

// New creates a LoadBalancer with the given strategy.
func New(strategy Strategy) *LoadBalancer {
	if strategy == "" {
		strategy = RoundRobin
	}
	return &LoadBalancer{strategy: strategy}
}

// AddBackend adds a new upstream instance to the pool.
// Safe to call while the load balancer is serving traffic.
func (lb *LoadBalancer) AddBackend(name, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("lb: invalid backend URL %q: %w", rawURL, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)

	// Custom error handler so a dead backend returns 502 instead of panic.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, fmt.Sprintf("backend %q unavailable: %v", name, err), http.StatusBadGateway)
	}

	backend := &Backend{
		URL:     parsed,
		Name:    name,
		Healthy: true,
		proxy:   proxy,
	}

	lb.mu.Lock()
	lb.backends = append(lb.backends, backend)
	lb.mu.Unlock()

	return nil
}

// RemoveBackend removes a backend from the pool by name.
// Safe to call while the load balancer is serving traffic.
func (lb *LoadBalancer) RemoveBackend(name string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	filtered := make([]*Backend, 0, len(lb.backends))
	for _, b := range lb.backends {
		if b.Name != name {
			filtered = append(filtered, b)
		}
	}
	lb.backends = filtered
}

// SetHealthy marks a backend as healthy or unhealthy.
// Unhealthy backends are skipped during routing.
func (lb *LoadBalancer) SetHealthy(name string, healthy bool) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	for _, b := range lb.backends {
		if b.Name == name {
			b.Healthy = healthy
			return
		}
	}
}

// Len returns the current number of backends in the pool.
func (lb *LoadBalancer) Len() int {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return len(lb.backends)
}

// Backends returns a snapshot of the current backend list.
func (lb *LoadBalancer) Backends() []BackendInfo {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	info := make([]BackendInfo, len(lb.backends))
	for i, b := range lb.backends {
		info[i] = BackendInfo{
			Name:        b.Name,
			URL:         b.URL.String(),
			Healthy:     b.Healthy,
			ActiveConns: atomic.LoadInt64(&b.ActiveConns),
		}
	}
	return info
}

// ServeHTTP implements http.Handler — routes the request to a backend.
func (lb *LoadBalancer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend := lb.pick(r)
	if backend == nil {
		http.Error(w, "no healthy backends available", http.StatusServiceUnavailable)
		return
	}

	atomic.AddInt64(&backend.ActiveConns, 1)
	defer atomic.AddInt64(&backend.ActiveConns, -1)

	backend.proxy.ServeHTTP(w, r)
}

// pick selects the next backend based on the configured strategy.
func (lb *LoadBalancer) pick(r *http.Request) *Backend {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	healthy := make([]*Backend, 0, len(lb.backends))
	for _, b := range lb.backends {
		if b.Healthy {
			healthy = append(healthy, b)
		}
	}

	if len(healthy) == 0 {
		return nil
	}

	switch lb.strategy {
	case LeastConn:
		return leastConnBackend(healthy)
	case IPHash:
		return ipHashBackend(healthy, r.RemoteAddr)
	default:
		// Round-robin using atomic counter.
		idx := atomic.AddUint64(&lb.counter, 1)
		return healthy[idx%uint64(len(healthy))]
	}
}

// leastConnBackend returns the backend with the fewest active connections.
func leastConnBackend(backends []*Backend) *Backend {
	best := backends[0]
	bestConns := atomic.LoadInt64(&best.ActiveConns)

	for _, b := range backends[1:] {
		conns := atomic.LoadInt64(&b.ActiveConns)
		if conns < bestConns {
			best = b
			bestConns = conns
		}
	}
	return best
}

// ipHashBackend routes a client to a consistent backend based on IP.
// Same IP always goes to the same backend (sticky-ish without cookies).
func ipHashBackend(backends []*Backend, remoteAddr string) *Backend {
	// Simple hash — sum of IP bytes mod pool size.
	var hash uint64
	for _, c := range remoteAddr {
		hash = hash*31 + uint64(c)
	}
	return backends[hash%uint64(len(backends))]
}

// BackendInfo is a JSON-serialisable snapshot of a backend's state.
type BackendInfo struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Healthy     bool   `json:"healthy"`
	ActiveConns int64  `json:"activeConns"`
}
