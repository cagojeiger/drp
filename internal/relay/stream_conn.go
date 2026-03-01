package relay

import (
	"net"

	"github.com/quic-go/quic-go"
)

// quicStreamConn wraps a quic.Stream as a net.Conn.
// It delegates LocalAddr/RemoteAddr to the parent QUIC connection.
type quicStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func QuicStreamToNetConn(s *quic.Stream, c *quic.Conn) net.Conn {
	return &quicStreamConn{Stream: s, conn: c}
}

func (c *quicStreamConn) LocalAddr() net.Addr {
	if c.conn != nil {
		return c.conn.LocalAddr()
	}
	return &net.UDPAddr{}
}

func (c *quicStreamConn) RemoteAddr() net.Addr {
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return &net.UDPAddr{}
}

func (c *quicStreamConn) Close() error {
	c.CancelRead(0)
	return c.Stream.Close()
}
