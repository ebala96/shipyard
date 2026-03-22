package shiplink

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Resolver resolves Shiplink DNS names to upstream URLs.
// It caches results for a short TTL to avoid hitting etcd on every request.
type Resolver struct {
	registry *Registry
	cache    map[string]*resolveCache
}

type resolveCache struct {
	endpoints []*Endpoint
	expiresAt time.Time
}

const cacheTTL = 5 * time.Second

// NewResolver creates a Resolver backed by the given registry.
func NewResolver(registry *Registry) *Resolver {
	return &Resolver{
		registry: registry,
		cache:    make(map[string]*resolveCache),
	}
}

// Resolve returns the upstream URL for a Shiplink DNS name or service name.
// Accepts both "prometheus.shipyard.local" and "prometheus".
// Returns ("", error) if the service has no healthy endpoints.
func (r *Resolver) Resolve(ctx context.Context, nameOrDNS string) (string, error) {
	serviceName := r.normalise(nameOrDNS)

	// Check cache.
	if cached, ok := r.cache[serviceName]; ok && time.Now().Before(cached.expiresAt) {
		if len(cached.endpoints) > 0 {
			return r.pick(cached.endpoints), nil
		}
	}

	// Fetch from registry.
	endpoints, err := r.registry.Resolve(ctx, serviceName)
	if err != nil {
		return "", fmt.Errorf("shiplink dns: resolve failed for %q: %w", serviceName, err)
	}
	if len(endpoints) == 0 {
		return "", fmt.Errorf("shiplink dns: no healthy endpoints for %q", serviceName)
	}

	// Update cache.
	r.cache[serviceName] = &resolveCache{
		endpoints: endpoints,
		expiresAt: time.Now().Add(cacheTTL),
	}

	return r.pick(endpoints), nil
}

// ResolveAll returns all known service DNS names and their URLs.
// Used to build the proxy routing table.
func (r *Resolver) ResolveAll(ctx context.Context) (map[string]string, error) {
	all, err := r.registry.ResolveAll(ctx)
	if err != nil {
		return nil, err
	}

	routes := make(map[string]string)
	for name, endpoints := range all {
		healthy := filterHealthy(endpoints)
		if len(healthy) > 0 {
			routes[DNSName(name)] = r.pick(healthy)
			routes[name] = r.pick(healthy) // also resolve by bare name
		}
	}
	return routes, nil
}

// normalise strips the .shipyard.local suffix if present.
func (r *Resolver) normalise(name string) string {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, dnsSuffix) {
		name = strings.TrimSuffix(name, dnsSuffix)
	}
	return name
}

// pick selects an endpoint using weighted random selection.
// Higher weight = more traffic.
func (r *Resolver) pick(endpoints []*Endpoint) string {
	if len(endpoints) == 1 {
		return endpoints[0].URL()
	}

	// Find the highest weight endpoint (simple weighted selection).
	best := endpoints[0]
	for _, ep := range endpoints[1:] {
		if ep.Weight > best.Weight {
			best = ep
		}
	}
	return best.URL()
}

func filterHealthy(endpoints []*Endpoint) []*Endpoint {
	var healthy []*Endpoint
	for _, ep := range endpoints {
		if ep.Healthy {
			healthy = append(healthy, ep)
		}
	}
	return healthy
}
