package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/registry"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

func (s *Server) routeRequest(hostname string, userConn net.Conn, buffered []byte) {
	info, found := s.lookup.Lookup(hostname)
	if !found {
		_, _ = userConn.Write(httpBadGateway)
		return
	}
	if info.IsLocal {
		s.localRoute(info, userConn, buffered)
	} else {
		s.remoteRelay(info, userConn, buffered)
	}
}

func (s *Server) localRoute(info registry.ServiceInfo, userConn net.Conn, buf []byte) {
	workConn, err := s.broker.RequestAndWait(info.ProxyAlias, 10*time.Second)
	if err != nil {
		if errors.Is(err, ErrWorkConnTimeout) {
			_, _ = userConn.Write(httpGatewayTimeout)
		} else {
			log.Printf("work conn error: %v", err)
			_, _ = userConn.Write(httpBadGateway)
		}
		return
	}
	defer func() { _ = workConn.Close() }()
	serveConn(workConn, userConn, info.ProxyAlias, buf)
}

func (s *Server) remoteRelay(info registry.ServiceInfo, userConn net.Conn, buf []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := s.relayer.DialStream(ctx, info.NodeID)
	if err != nil {
		log.Printf("relay dial to %s failed: %v", info.NodeID, err)
		_, _ = userConn.Write(httpBadGateway)
		return
	}
	defer func() { _ = stream.Close() }()

	_ = protocol.WriteEnvelope(stream, &drppb.Envelope{
		Payload: &drppb.Envelope_RelayOpen{RelayOpen: &drppb.RelayOpen{
			ProxyAlias: info.ProxyAlias,
			RequestId:  fmt.Sprintf("%d", time.Now().UnixNano()),
		}},
	})
	_, _ = stream.Write(buf)
	go func() { _ = protocol.Pipe(userConn, stream) }()
	_ = protocol.Pipe(stream, userConn)
}

func serveConn(workConn, userConn net.Conn, proxyAlias string, buf []byte) {
	_ = protocol.WriteEnvelope(workConn, &drppb.Envelope{
		Payload: &drppb.Envelope_StartWorkConn{StartWorkConn: &drppb.StartWorkConn{
			ProxyAlias: proxyAlias,
		}},
	})
	_, _ = workConn.Write(buf)
	go func() { _ = protocol.Pipe(userConn, workConn) }()
	_ = protocol.Pipe(workConn, userConn)
}
