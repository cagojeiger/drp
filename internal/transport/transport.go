package transport

import "net"

type Dialer interface {
	Dial(addr string) (net.Conn, error)
}

type TCP struct{}

func (TCP) Dial(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }

func Listen(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
