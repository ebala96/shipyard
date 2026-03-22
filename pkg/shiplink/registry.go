// Package shiplink is Shipyard's internal service mesh layer.
// It provides service discovery, DNS resolution, smart routing,
// and optional mTLS for container-to-container communication.
//
// Services find each other by name, not by hardcoded port:
//
//	http://prometheus.shipyard.local  →  localhost:39321  (resolved at request time)
//	http://grafana.shipyard.local     →  localhost:41205
package shiplink

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	prefixRegistry = "/shipyard/shiplink/services/"
	endpointTTL    = 60 // seconds — endpoint must re-register within this window
	dnsSuffix      = ".shipyard.local"
)

// Endpoint represents one running instance of a service.
type Endpoint struct {
	ServiceName string            `json:"serviceName"`
	ContainerID string            `json:"containerID"`
	Host        string            `json:"host"`
	Port        int               `json:"port"`
	Mode        string            `json:"mode"`
	Weight      int               `json:"weight"`  // 0-100 for canary routing
	Healthy     bool              `json:"healthy"`
	Labels      map[string]string `json:"labels,omitempty"`
	RegisteredAt time.Time        `json:"registeredAt"`
}

// URL returns the full upstream URL for this endpoint.
func (e *Endpoint) URL() string {
	return fmt.Sprintf("http://%s:%d", e.Host, e.Port)
}

// DNSName returns the service's Shiplink DNS name.
func DNSName(serviceName string) string {
	return strings.ToLower(serviceName) + dnsSuffix
}

// Registry manages service endpoints in etcd.
type Registry struct {
	client *clientv3.Client
}

// NewRegistry creates a Registry.
func NewRegistry(client *clientv3.Client) *Registry {
	return &Registry{client: client}
}

// Register adds or updates an endpoint with a TTL lease.
// Must be called periodically (heartbeat) to keep the endpoint alive.
func (r *Registry) Register(ctx context.Context, ep *Endpoint) error {
	if ep.RegisteredAt.IsZero() {
		ep.RegisteredAt = time.Now()
	}
	if ep.Weight == 0 {
		ep.Weight = 100
	}
	ep.Healthy = true

	data, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("shiplink: marshal failed: %w", err)
	}

	// Grant a TTL lease.
	lease, err := r.client.Grant(ctx, endpointTTL)
	if err != nil {
		return fmt.Errorf("shiplink: lease grant failed: %w", err)
	}

	key := prefixRegistry + ep.ServiceName + "/" + ep.ContainerID
	_, err = r.client.Put(ctx, key, string(data), clientv3.WithLease(lease.ID))
	return err
}

// Deregister removes an endpoint immediately.
func (r *Registry) Deregister(ctx context.Context, serviceName, containerID string) error {
	key := prefixRegistry + serviceName + "/" + containerID
	_, err := r.client.Delete(ctx, key)
	return err
}

// Resolve returns all healthy endpoints for a service.
func (r *Registry) Resolve(ctx context.Context, serviceName string) ([]*Endpoint, error) {
	prefix := prefixRegistry + serviceName + "/"
	resp, err := r.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("shiplink: resolve failed for %q: %w", serviceName, err)
	}

	var endpoints []*Endpoint
	for _, kv := range resp.Kvs {
		var ep Endpoint
		if err := json.Unmarshal(kv.Value, &ep); err != nil {
			continue
		}
		if ep.Healthy {
			endpoints = append(endpoints, &ep)
		}
	}
	return endpoints, nil
}

// ResolveAll returns all registered services and their endpoints.
func (r *Registry) ResolveAll(ctx context.Context) (map[string][]*Endpoint, error) {
	resp, err := r.client.Get(ctx, prefixRegistry, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}

	services := make(map[string][]*Endpoint)
	for _, kv := range resp.Kvs {
		var ep Endpoint
		if err := json.Unmarshal(kv.Value, &ep); err != nil {
			continue
		}
		services[ep.ServiceName] = append(services[ep.ServiceName], &ep)
	}
	return services, nil
}

// HealthCheck pings an endpoint and marks it healthy or unhealthy.
func (r *Registry) HealthCheck(ctx context.Context, ep *Endpoint, path string) bool {
	if path == "" {
		path = "/healthz"
	}
	url := fmt.Sprintf("%s%s", ep.URL(), path)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// Watch returns a channel that fires when any service endpoint changes.
func (r *Registry) Watch(ctx context.Context) clientv3.WatchChan {
	return r.client.Watch(ctx, prefixRegistry, clientv3.WithPrefix())
}
