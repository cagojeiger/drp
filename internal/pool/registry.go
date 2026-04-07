package pool

import (
	"sync"
)

// Registry manages pools by proxy name, keyed by runID.
type Registry struct {
	mu    sync.RWMutex
	pools map[string]*Pool // proxyName → Pool
}

func NewRegistry() *Registry {
	return &Registry{
		pools: make(map[string]*Pool),
	}
}

// GetOrCreate returns an existing pool or creates a new one.
func (r *Registry) GetOrCreate(proxyName string, requestFn func()) *Pool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.pools[proxyName]; ok {
		return p
	}
	p := New(requestFn)
	r.pools[proxyName] = p
	return p
}

// Get returns a pool by proxy name.
func (r *Registry) Get(proxyName string) (*Pool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pools[proxyName]
	return p, ok
}

// Remove closes and removes a pool.
func (r *Registry) Remove(proxyName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[proxyName]; ok {
		p.Close()
		delete(r.pools, proxyName)
	}
}

// Range iterates over all pools. Return false to stop.
func (r *Registry) Range(fn func(name string, p *Pool) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, p := range r.pools {
		if !fn(name, p) {
			return
		}
	}
}
