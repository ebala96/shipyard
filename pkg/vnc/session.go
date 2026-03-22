package vnc

import "sync"

// Registry is an in-memory store of active VNC sessions keyed by service name.
// Sessions are also persisted to etcd via ContainerRecord.VNCInstance in the
// deploy handler, but this registry provides fast in-process lookups.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Instance // serviceName → instance
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{sessions: make(map[string]*Instance)}
}

// Put stores or replaces the VNC instance for a service.
func (r *Registry) Put(serviceName string, inst *Instance) {
	r.mu.Lock()
	r.sessions[serviceName] = inst
	r.mu.Unlock()
}

// Get returns the VNC instance for a service, or (nil, false) if not found.
func (r *Registry) Get(serviceName string) (*Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst, ok := r.sessions[serviceName]
	return inst, ok
}

// Delete removes the VNC instance for a service.
func (r *Registry) Delete(serviceName string) {
	r.mu.Lock()
	delete(r.sessions, serviceName)
	r.mu.Unlock()
}

// List returns all active VNC instances.
func (r *Registry) List() []*Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Instance, 0, len(r.sessions))
	for _, inst := range r.sessions {
		result = append(result, inst)
	}
	return result
}
