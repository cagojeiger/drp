// Package transport provides network transport abstractions.
package transport

import "net"

// Dialer abstracts connection establishment.
type Dialer interface {
	Dial(addr string) (net.Conn, error)
}

// TCPDialer implements Dialer using plain TCP.
type TCPDialer struct{}

// Dial establishes a TCP connection to the given address.
func (TCPDialer) Dial(addr string) (net.Conn, error) {
	return net.Dial("tcp", addr)
}

// Listen creates a TCP listener on the given address.
// Use ":0" for a random available port.
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
