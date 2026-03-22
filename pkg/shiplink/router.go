package shiplink

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// CanaryRule defines how traffic is split between service versions.
type CanaryRule struct {
	ServiceName   string // e.g. "prometheus"
	StableWeight  int    // 0-100 percent to stable version
	CanaryWeight  int    // 0-100 percent to canary version
	CanaryHeader  string // if set, requests with X-Canary: true always go to canary
	CanaryContainerID string
}

// Router is the Shiplink smart proxy.
// It sits in front of all services and handles:
//   - Name-based routing (prometheus.shipyard.local → upstream)
//   - Canary traffic splits
//   - Health check loop (removes unhealthy backends)
type Router struct {
	mu       sync.RWMutex
	resolver *Resolver
	registry *Registry
	canary   map[string]*CanaryRule // serviceName → canary rule
	proxies  map[string]*httputil.ReverseProxy // url string → proxy
}

// NewRouter creates a Router.
func NewRouter(registry *Registry) *Router {
	return &Router{
		resolver: NewResolver(registry),
		registry: registry,
		canary:   make(map[string]*CanaryRule),
		proxies:  make(map[string]*httputil.ReverseProxy),
	}
}

// SetCanary configures a canary traffic split for a service.
func (r *Router) SetCanary(rule CanaryRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canary[rule.ServiceName] = &rule
}

// RemoveCanary removes a canary rule for a service.
func (r *Router) RemoveCanary(serviceName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.canary, serviceName)
}

// GetCanary returns the canary rule for a service, or nil if none.
func (r *Router) GetCanary(serviceName string) *CanaryRule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.canary[serviceName]
}

// Route resolves a service name to an upstream URL, applying canary logic.
func (r *Router) Route(ctx context.Context, req *http.Request, serviceName string) (string, error) {
	// Check for canary rule.
	r.mu.RLock()
	rule := r.canary[serviceName]
	r.mu.RUnlock()

	if rule != nil {
		return r.routeWithCanary(ctx, req, serviceName, rule)
	}

	// Standard resolution.
	return r.resolver.Resolve(ctx, serviceName)
}

// ServeHTTP implements http.Handler — proxies a request to the resolved upstream.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Extract service name from Host header: "prometheus.shipyard.local" → "prometheus"
	host := req.Host
	if host == "" {
		http.Error(w, "shiplink: missing Host header", http.StatusBadRequest)
		return
	}

	serviceName := r.resolver.normalise(host)
	upstreamURL, err := r.Route(req.Context(), req, serviceName)
	if err != nil {
		http.Error(w, fmt.Sprintf("shiplink: %v", err), http.StatusServiceUnavailable)
		return
	}

	proxy := r.getOrCreateProxy(upstreamURL)
	req.Host = req.URL.Host
	proxy.ServeHTTP(w, req)
}

// StartHealthChecker runs a background loop checking all registered endpoints.
// Unhealthy endpoints are marked and removed from routing.
func (r *Router) StartHealthChecker(ctx context.Context, interval time.Duration) {
	if interval == 0 {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.runHealthChecks(ctx)
			}
		}
	}()
	log.Printf("shiplink: health checker started (every %s)", interval)
}

// Routes returns all currently resolved service routes.
func (r *Router) Routes(ctx context.Context) (map[string]string, error) {
	return r.resolver.ResolveAll(ctx)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (r *Router) routeWithCanary(ctx context.Context, req *http.Request, serviceName string, rule *CanaryRule) (string, error) {
	// Header-forced canary.
	if rule.CanaryHeader != "" && req.Header.Get("X-Canary") == "true" {
		endpoints, err := r.registry.Resolve(ctx, serviceName)
		if err == nil {
			for _, ep := range endpoints {
				if ep.ContainerID == rule.CanaryContainerID {
					return ep.URL(), nil
				}
			}
		}
	}

	// Weight-based split.
	roll := rand.Intn(100)
	if roll < rule.CanaryWeight {
		// Send to canary.
		endpoints, err := r.registry.Resolve(ctx, serviceName)
		if err == nil {
			for _, ep := range endpoints {
				if ep.ContainerID == rule.CanaryContainerID {
					return ep.URL(), nil
				}
			}
		}
	}

	// Default to stable.
	return r.resolver.Resolve(ctx, serviceName)
}

func (r *Router) getOrCreateProxy(upstreamURL string) *httputil.ReverseProxy {
	r.mu.RLock()
	if p, ok := r.proxies[upstreamURL]; ok {
		r.mu.RUnlock()
		return p
	}
	r.mu.RUnlock()

	target, _ := url.Parse(upstreamURL)
	p := httputil.NewSingleHostReverseProxy(target)

	r.mu.Lock()
	r.proxies[upstreamURL] = p
	r.mu.Unlock()

	return p
}

func (r *Router) runHealthChecks(ctx context.Context) {
	all, err := r.registry.ResolveAll(ctx)
	if err != nil {
		return
	}
	for _, endpoints := range all {
		for _, ep := range endpoints {
			healthy := r.registry.HealthCheck(ctx, ep, "")
			if !healthy {
				log.Printf("shiplink: endpoint %s:%d for %q is unhealthy",
					ep.Host, ep.Port, ep.ServiceName)
				// Re-register with healthy=false to remove from routing.
				ep.Healthy = false
				_ = r.registry.Register(ctx, ep)
			}
		}
	}
}
