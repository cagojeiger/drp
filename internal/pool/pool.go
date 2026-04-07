package pool

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// Pool manages a pool of work connections.
// RequestConn is called when more connections are needed (sends ReqWorkConn to frpc).
type Pool struct {
	conns      chan net.Conn
	requestFn  func()
	closed     bool
	closedOnce sync.Once
	mu         sync.Mutex
}

// New creates a pool. requestFn is called to request a new work connection from frpc.
func New(requestFn func()) *Pool {
	return &Pool{
		conns:     make(chan net.Conn, 64),
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
// After successful Get, triggers eager refill.
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
		go p.requestFn() // eager refill
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
		go p.requestFn() // eager refill
		return conn, nil
	case <-timer.C:
		return nil, fmt.Errorf("get work conn timeout after %s", timeout)
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
