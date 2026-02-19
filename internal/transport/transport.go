// Package transport defines the Transport interface for peer-to-peer
// connections. TCP is implemented first; QUIC will be added in Phase 2.
//
// In Phase 1 (TCP), each relay opens a NEW TCP connection to the peer
// (matching the Python POC behavior). In Phase 2 (QUIC), relays will
// open streams on a single QUIC connection — no code changes needed
// above the transport layer.
package transport

import (
	"net"
)

// Conn wraps a network connection used for both control and relay streams.
// In TCP mode this is a plain net.Conn. In QUIC mode it will be a quic.Stream.
type Conn = net.Conn

// Listener wraps a network listener.
// In TCP mode this is a plain net.Listener.
type Listener = net.Listener

// Dial opens a new TCP connection to addr (host:port).
// This is a new connection each time — in Phase 2 (QUIC) this will
// open a new stream on an existing QUIC connection instead.
func Dial(addr string) (Conn, error) {
	return net.Dial("tcp", addr)
}

// Listen creates a TCP listener on addr (host:port or :port).
func Listen(addr string) (Listener, error) {
	return net.Listen("tcp", addr)
}
