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
	conns      chan net.Conn
	requestFn  func()
	refilling  atomic.Bool // prevents goroutine explosion on eager refill
	closed     bool
	closedOnce sync.Once
	mu         sync.Mutex
}

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
// After successful Get, triggers eager refill (at most 1 in-flight).
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
		p.tryRefill()
		return conn, nil
	default:
	}

	// 비어있음 → 요청 후 대기
	p.requestFn()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case conn, ok := <-p.conns:
		if !ok {
			return nil, fmt.Errorf("pool closed")
		}
		p.tryRefill()
		return conn, nil
	case <-timer.C:
		return nil, fmt.Errorf("get work conn timeout after %s", timeout)
	}
}

// tryRefill triggers an async refill if one is not already in-flight.
func (p *Pool) tryRefill() {
	if p.refilling.CompareAndSwap(false, true) {
		go func() {
			p.requestFn()
			p.refilling.Store(false)
		}()
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
