package server

import (
	"net"

	"github.com/cagojeiger/drp/internal/protocol"
)

const initialReadBufSize = 4096

func (s *Server) handleHTTP(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, initialReadBufSize)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	buf = buf[:n]

	hostname := protocol.ExtractHost(buf)
	if hostname == "" {
		_, _ = conn.Write(httpBadRequest)
		return
	}

	s.routeRequest(hostname, conn, buf)
}

func (s *Server) handleHTTPS(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, initialReadBufSize)
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
