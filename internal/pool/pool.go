// Package pool holds the per-session work-connection pool that drps uses
// to serve HTTP requests. Each frpc session gets its own Pool; the Pool
// buffers up to Capacity idle work-conns and asks frpc for more via the
// supplied requestFn whenever Get is about to block.
package pool

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// defaultCapacity is the work-conn buffer size when callers do not
	// specify one. 64 matches the pre-refactor variadic default.
	defaultCapacity = 64

	// minMissRefillBurst is the floor for the extra refill request that
	// Get issues when the pool is empty. Always ≥ 2 so at least one spare
	// is already on its way by the time the current request returns.
	minMissRefillBurst = 2

	// maxMissRefillBurst caps the per-miss refill burst so a sudden surge
	// of misses cannot flood frpc with refill requests.
	maxMissRefillBurst = 8
)

// Pool manages a bounded channel of idle work-connections. Every Get on an
// empty pool triggers an asynchronous refill via requestFn; the refill
// worker coalesces concurrent demand into a single goroutine.
type Pool struct {
	// --- connection queue ---
	conns chan net.Conn

	// --- refill machinery ---
	requestFn      func()
	refilling      atomic.Bool
	pendingRefills atomic.Int64

	// --- statistics (lock-free, see Stats()) ---
	getHit       atomic.Int64
	getMiss      atomic.Int64
	getTimeout   atomic.Int64
	refillDemand atomic.Int64
	refillSent   atomic.Int64

	// --- teardown ---
	mu         sync.Mutex
	closed     bool
	closedOnce sync.Once
}

// New creates a Pool wired to requestFn, which is invoked each time the
// pool wants frpc to hand it another work-conn. The optional capacity
// argument sets the idle-conn buffer size; it is retained as a variadic
// parameter because the majority of tests construct pools without caring
// about capacity, and the production path (cmd/drps/main.go via
// Registry.GetOrCreate) always supplies one explicitly.
func New(requestFn func(), capacity ...int) *Pool {
	c := defaultCapacity
	if len(capacity) > 0 && capacity[0] > 0 {
		c = capacity[0]
	}
	return &Pool{
		conns:     make(chan net.Conn, c),
		requestFn: requestFn,
	}
}

// Put hands a freshly delivered work-conn to the pool. If the pool is at
// capacity or already closed, conn is closed instead of being enqueued.
func (p *Pool) Put(conn net.Conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.Close()
		return
	}
	p.mu.Unlock()

	select {
	case p.conns <- conn:
	default:
		// Pool full → drop the arriving conn to keep the buffer size
		// honest.
		conn.Close()
	}
}

// Get retrieves an idle work-conn. When the pool is empty, Get issues a
// refill burst and waits up to timeout for a conn to arrive. Every
// successful Get also triggers a one-slot refill so the buffer stays warm.
func (p *Pool) Get(timeout time.Duration) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool closed")
	}
	p.mu.Unlock()

	// Fast path: an idle conn is ready.
	select {
	case conn, ok := <-p.conns:
		if !ok {
			return nil, fmt.Errorf("pool closed")
		}
		p.getHit.Add(1)
		p.requestAsyncRefill(1)
		return conn, nil
	default:
	}

	// Empty → account the miss and ask for a burst before blocking.
	p.getMiss.Add(1)
	p.requestAsyncRefill(p.missRefillBurst())

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case conn, ok := <-p.conns:
		if !ok {
			return nil, fmt.Errorf("pool closed")
		}
		p.requestAsyncRefill(1)
		return conn, nil
	case <-timer.C:
		p.getTimeout.Add(1)
		return nil, fmt.Errorf("get work conn timeout after %s", timeout)
	}
}

// missRefillBurst picks the size of the refill burst issued on a Get miss.
// It aims for one quarter of the pool capacity, clamped to
// [minMissRefillBurst, maxMissRefillBurst]. Returning int64 matches the
// requestAsyncRefill signature so the two never need a cast at the call
// site.
func (p *Pool) missRefillBurst() int64 {
	c := cap(p.conns)
	if c <= 0 {
		return minMissRefillBurst
	}
	burst := c / 4
	if burst < minMissRefillBurst {
		burst = minMissRefillBurst
	}
	if burst > maxMissRefillBurst {
		burst = maxMissRefillBurst
	}
	return int64(burst)
}

// requestAsyncRefill accumulates refill demand and spawns (or re-uses) a
// single worker goroutine that drains it by calling requestFn.
func (p *Pool) requestAsyncRefill(n int64) {
	if n <= 0 {
		return
	}
	p.refillDemand.Add(n)
	p.pendingRefills.Add(n)
	p.startRefillWorker()
}

// startRefillWorker starts the drain goroutine if one is not already
// running. The CAS guard ensures only one worker exists at a time; the
// worker loops until pendingRefills drains, handling the small race where
// new demand arrives between Swap(0) and the worker going idle.
func (p *Pool) startRefillWorker() {
	if !p.refilling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			n := p.pendingRefills.Swap(0)
			if n == 0 {
				p.refilling.Store(false)
				// Race: new demand can arrive between Swap(0) and
				// Store(false). Re-claim the worker if so.
				if p.pendingRefills.Load() == 0 || !p.refilling.CompareAndSwap(false, true) {
					return
				}
				continue
			}

			for i := int64(0); i < n; i++ {
				p.requestFn()
				p.refillSent.Add(1)
			}
		}
	}()
}

// Stats is a JSON-serializable snapshot of the pool counters.
type Stats struct {
	GetHit       int64 `json:"get_hit"`
	GetMiss      int64 `json:"get_miss"`
	GetTimeout   int64 `json:"get_timeout"`
	RefillDemand int64 `json:"refill_demand"`
	RefillSent   int64 `json:"refill_sent"`
}

// Stats returns a point-in-time read of every counter.
func (p *Pool) Stats() Stats {
	return Stats{
		GetHit:       p.getHit.Load(),
		GetMiss:      p.getMiss.Load(),
		GetTimeout:   p.getTimeout.Load(),
		RefillDemand: p.refillDemand.Load(),
		RefillSent:   p.refillSent.Load(),
	}
}

// Close marks the pool closed and drains any remaining buffered conns.
// Safe to call multiple times.
func (p *Pool) Close() {
	p.closedOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()

		close(p.conns)
		for conn := range p.conns {
			conn.Close()
		}
	})
}
