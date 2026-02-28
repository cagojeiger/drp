package server

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cagojeiger/drp/internal/registry"
)

type fakeLookup struct {
	services map[string]registry.ServiceInfo
}

func (f *fakeLookup) Lookup(hostname string) (registry.ServiceInfo, bool) {
	info, ok := f.services[hostname]
	return info, ok
}

type fakeBroker struct {
	conn net.Conn
	err  error
}

func (f *fakeBroker) RequestAndWait(proxyAlias string, timeout time.Duration) (net.Conn, error) {
	return f.conn, f.err
}

type fakeRelayer struct {
	conn net.Conn
	err  error
}

func (f *fakeRelayer) DialStream(ctx context.Context, nodeID string) (net.Conn, error) {
	return f.conn, f.err
}

type fakeRegistrar struct {
	mu           sync.Mutex
	registered   []string
	unregistered []string
}

func (f *fakeRegistrar) RegisterService(alias, hostname string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, hostname)
}

func (f *fakeRegistrar) UnregisterService(hostname string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unregistered = append(f.unregistered, hostname)
}

func (f *fakeRegistrar) getRegistered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.registered))
	copy(cp, f.registered)
	return cp
}

func (f *fakeRegistrar) getUnregistered() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]string, len(f.unregistered))
	copy(cp, f.unregistered)
	return cp
}

// faultConn wraps a net.Conn and allows injecting read/write errors at test time.
type faultConn struct {
	net.Conn
	readErr  atomic.Pointer[error]
	writeErr atomic.Pointer[error]
}

func newFaultConn(c net.Conn) *faultConn {
	return &faultConn{Conn: c}
}

func (c *faultConn) Read(b []byte) (int, error) {
	if p := c.readErr.Load(); p != nil {
		return 0, *p
	}
	return c.Conn.Read(b)
}

func (c *faultConn) Write(b []byte) (int, error) {
	if p := c.writeErr.Load(); p != nil {
		return 0, *p
	}
	return c.Conn.Write(b)
}

func (c *faultConn) InjectReadError(err error)  { c.readErr.Store(&err) }
func (c *faultConn) InjectWriteError(err error) { c.writeErr.Store(&err) }
func (c *faultConn) ClearFaults()               { c.readErr.Store(nil); c.writeErr.Store(nil) }
