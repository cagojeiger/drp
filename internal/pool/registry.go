package pool

import (
	"sync"
)

// Registry manages pools keyed by runID.
type Registry struct {
	mu    sync.RWMutex
	pools map[string]*Pool // proxyName → Pool
}

type AggregateStats struct {
	ActivePools  int   `json:"active_pools"`
	GetHit       int64 `json:"get_hit"`
	GetMiss      int64 `json:"get_miss"`
	GetTimeout   int64 `json:"get_timeout"`
	RefillDemand int64 `json:"refill_demand"`
	RefillSent   int64 `json:"refill_sent"`
}

func NewRegistry() *Registry {
	return &Registry{
		pools: make(map[string]*Pool),
	}
}

// GetOrCreate returns an existing pool or creates a new one.
func (r *Registry) GetOrCreate(runID string, requestFn func(), capacity ...int) *Pool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.pools[runID]; ok {
		return p
	}
	p := New(requestFn, capacity...)
	r.pools[runID] = p
	return p
}

// Get returns a pool by run ID.
func (r *Registry) Get(runID string) (*Pool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.pools[runID]
	return p, ok
}

// Remove closes and removes a pool.
func (r *Registry) Remove(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.pools[runID]; ok {
		p.Close()
		delete(r.pools, runID)
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

func (r *Registry) AggregateStats() AggregateStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out AggregateStats
	out.ActivePools = len(r.pools)
	for _, p := range r.pools {
		s := p.Stats()
		out.GetHit += s.GetHit
		out.GetMiss += s.GetMiss
		out.GetTimeout += s.GetTimeout
		out.RefillDemand += s.RefillDemand
		out.RefillSent += s.RefillSent
	}
	return out
}
