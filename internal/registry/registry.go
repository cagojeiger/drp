package registry

import "sync"

// ServiceInfo holds metadata about a registered service.
type ServiceInfo struct {
	NodeID     string // which drps node owns this service
	ProxyAlias string // service alias (e.g. "myapp")
	Hostname   string // public hostname (e.g. "myapp.example.com")
	IsLocal    bool   // true if service is on this node
}

// Registry is a thread-safe service registry.
// It maps hostnames to ServiceInfo.
type Registry struct {
	mu       sync.RWMutex
	services map[string]ServiceInfo // key = hostname
}

func New() *Registry {
	return &Registry{services: make(map[string]ServiceInfo)}
}

func (r *Registry) Register(hostname, nodeID, proxyAlias string, isLocal bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[hostname] = ServiceInfo{
		NodeID:     nodeID,
		ProxyAlias: proxyAlias,
		Hostname:   hostname,
		IsLocal:    isLocal,
	}
}

func (r *Registry) Unregister(hostname string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, hostname)
}

func (r *Registry) Lookup(hostname string) (ServiceInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.services[hostname]
	return info, ok
}

func (r *Registry) ListByNode(nodeID string) []ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	services := make([]ServiceInfo, 0)
	for _, info := range r.services {
		if info.NodeID == nodeID {
			services = append(services, info)
		}
	}
	return services
}

func (r *Registry) RemoveByNode(nodeID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := make([]string, 0)
	for hostname, info := range r.services {
		if info.NodeID == nodeID {
			removed = append(removed, hostname)
			delete(r.services, hostname)
		}
	}
	return removed
}

func (r *Registry) LocalServices() []ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	services := make([]ServiceInfo, 0)
	for _, info := range r.services {
		if info.IsLocal {
			services = append(services, info)
		}
	}
	return services
}

func (r *Registry) Snapshot() map[string]ServiceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := make(map[string]ServiceInfo, len(r.services))
	for hostname, info := range r.services {
		snapshot[hostname] = info
	}
	return snapshot
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.services)
}
