package server

import (
	"bufio"
	"log"
	"net"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

func (s *Server) handleRelayConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := bufio.NewReader(conn)
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		return
	}
	ro := env.GetRelayOpen()
	if ro == nil {
		return
	}

	workConn, err := s.broker.RequestAndWait(ro.ProxyAlias, 10*time.Second)
	if err != nil {
		log.Printf("relay: work conn for %s: %v", ro.ProxyAlias, err)
		return
	}
	defer func() { _ = workConn.Close() }()

	_ = protocol.WriteEnvelope(workConn, &drppb.Envelope{
		Payload: &drppb.Envelope_StartWorkConn{StartWorkConn: &drppb.StartWorkConn{
			ProxyAlias: ro.ProxyAlias,
		}},
	})

	go func() { _ = protocol.Pipe(conn, workConn) }()
	_ = protocol.Pipe(workConn, r)
}
