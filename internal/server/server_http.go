package server

import (
	"net"

	"github.com/cagojeiger/drp/internal/protocol"
)

func (s *Server) handleHTTP(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	buf = buf[:n]

	hostname := protocol.ExtractHost(buf)
	if hostname == "" {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 15\r\n\r\n400 Bad Request"))
		return
	}

	s.routeRequest(hostname, conn, buf)
}

func (s *Server) handleHTTPS(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	buf = buf[:n]

	hostname := protocol.ExtractSNI(buf)
	if hostname == "" {
		return
	}

	s.routeRequest(hostname, conn, buf)
}
