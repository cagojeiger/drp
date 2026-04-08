package pool

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Pool manages a pool of work connections.
// RequestConn is called when more connections are needed (sends ReqWorkConn to frpc).
type Pool struct {
	conns          chan net.Conn
	requestFn      func()
	refilling      atomic.Bool
	pendingRefills atomic.Int64
	getHit         atomic.Int64
	getMiss        atomic.Int64
	getTimeout     atomic.Int64
	refillDemand   atomic.Int64
	refillSent     atomic.Int64
	closed         bool
	closedOnce     sync.Once
	mu             sync.Mutex
}

const (
	// miss 시 한 번에 추가 요청할 최소 개수.
	minMissRefillBurst = 2
	// miss 시 과도한 폭주를 막기 위한 상한.
	maxMissRefillBurst = 8
)

// New creates a pool with the given capacity.
// requestFn is called to request a new work connection from frpc.
func New(requestFn func(), capacity ...int) *Pool {
	cap := 64
	if len(capacity) > 0 && capacity[0] > 0 {
		cap = capacity[0]
	}
	return &Pool{
		conns:     make(chan net.Conn, cap),
		requestFn: requestFn,
	}
}

// Put adds a work connection to the pool.
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
		// 풀 가득 참 → 버림
		conn.Close()
	}
}

// Get retrieves a work connection from the pool.
// If empty, requests a new one and waits up to timeout.
// After successful Get, triggers eager refill without dropping refill demand.
func (p *Pool) Get(timeout time.Duration) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("pool closed")
	}
	p.mu.Unlock()

	// 풀에서 즉시 시도
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

	// 비어있음 → 요청 후 대기
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

func (p *Pool) missRefillBurst() int64 {
	c := cap(p.conns)
	if c <= 0 {
		return minMissRefillBurst
	}
	// 용량의 1/4를 기본 버스트로 잡고 상/하한을 둔다.
	burst := c / 4
	if burst < minMissRefillBurst {
		burst = minMissRefillBurst
	}
	if burst > maxMissRefillBurst {
		burst = maxMissRefillBurst
	}
	return int64(burst)
}

// requestAsyncRefill accumulates refill demand and drains it with one worker.
func (p *Pool) requestAsyncRefill(n int64) {
	if n <= 0 {
		return
	}
	p.refillDemand.Add(n)
	p.pendingRefills.Add(n)
	p.startRefillWorker()
}

func (p *Pool) startRefillWorker() {
	if !p.refilling.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			n := p.pendingRefills.Swap(0)
			if n == 0 {
				p.refilling.Store(false)
				// Handle race: new demand can arrive between Swap(0) and Store(false).
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

type Stats struct {
	GetHit       int64 `json:"get_hit"`
	GetMiss      int64 `json:"get_miss"`
	GetTimeout   int64 `json:"get_timeout"`
	RefillDemand int64 `json:"refill_demand"`
	RefillSent   int64 `json:"refill_sent"`
}

func (p *Pool) Stats() Stats {
	return Stats{
		GetHit:       p.getHit.Load(),
		GetMiss:      p.getMiss.Load(),
		GetTimeout:   p.getTimeout.Load(),
		RefillDemand: p.refillDemand.Load(),
		RefillSent:   p.refillSent.Load(),
	}
}

// Close closes the pool and all remaining connections.
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
