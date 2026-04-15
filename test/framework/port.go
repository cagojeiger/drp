// Package framework provides process-based test infrastructure for the drps
// compat and regression suites. It replaces testcontainers-go with direct
// exec.Command process management, mod-partitioned port allocation, and
// in-process mock backends — patterns adopted from frp's own E2E framework.
package framework

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
)

// Allocator manages mod-partitioned port allocation for parallel test
// isolation. Ports in [from, to] where port % mod == index belong to this
// allocator's partition. This is the same scheme frp uses
// (.repos/frp/test/e2e/pkg/port/port.go:28).
type Allocator struct {
	mu       sync.Mutex
	reserved map[int]bool // in our partition, not yet handed out
	used     map[int]bool // handed out via Get()
}

// NewAllocator creates a port allocator for the partition defined by
// (from, to, mod, index). Defaults: from=10000, to=30000, mod=1, index=0
// (single-runner mode). CI can override via DRP_PORT_MOD and DRP_PORT_INDEX.
func NewAllocator(from, to, mod, index int) *Allocator {
	reserved := make(map[int]bool)
	for p := from; p <= to; p++ {
		if p%mod == index {
			reserved[p] = true
		}
	}
	return &Allocator{
		reserved: reserved,
		used:     make(map[int]bool),
	}
}

// NewAllocatorFromEnv creates an allocator using DRP_PORT_MOD and
// DRP_PORT_INDEX env vars, defaulting to mod=1, index=0.
func NewAllocatorFromEnv() *Allocator {
	mod := 1
	index := 0
	if v := os.Getenv("DRP_PORT_MOD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			mod = n
		}
	}
	if v := os.Getenv("DRP_PORT_INDEX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			index = n
		}
	}
	return NewAllocator(10000, 30000, mod, index)
}

// Get returns an available port from the partition, verifying it is actually
// free by probing TCP bind. Returns 0 if exhausted (all ports in use or
// occupied by other processes).
func (a *Allocator) Get() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	for port := range a.reserved {
		if a.used[port] {
			continue
		}
		if !portFree(port) {
			continue
		}
		a.used[port] = true
		return port
	}
	return 0
}

// Release returns a port to the available pool.
func (a *Allocator) Release(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, port)
}

// portFree checks if a TCP port is available by attempting to listen on it.
func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}
