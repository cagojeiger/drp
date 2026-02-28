package server

import (
	"bufio"
	"log"
	"net"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

func (s *Server) handleControl(conn net.Conn) {
	r := bufio.NewReader(conn)
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		_ = conn.Close()
		return
	}

	switch p := env.Payload.(type) {
	case *drppb.Envelope_Login:
		s.clientSession(conn, r, p.Login)
	case *drppb.Envelope_NewWorkConn:
		s.acceptWorkConn(conn, p.NewWorkConn)
	default:
		log.Printf("unexpected control message: %T", p)
		_ = conn.Close()
	}
}

func (s *Server) clientSession(conn net.Conn, r *bufio.Reader, login *drppb.Login) {
	loginOk, loginErr := true, ""
	if s.cfg.Authenticate != nil {
		loginOk, loginErr = s.cfg.Authenticate(login)
	}
	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: loginOk, Error: loginErr}},
	}); err != nil {
		_ = conn.Close()
		return
	}
	if !loginOk {
		_ = conn.Close()
		return
	}

	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		_ = conn.Close()
		return
	}
	proxy := env.GetNewProxy()
	if proxy == nil {
		_ = conn.Close()
		return
	}

	proxyOk, proxyErr := true, ""
	if s.cfg.AuthorizeProxy != nil {
		proxyOk, proxyErr = s.cfg.AuthorizeProxy(proxy)
	}
	if !proxyOk {
		_ = protocol.WriteEnvelope(conn, &drppb.Envelope{
			Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: false, Error: proxyErr}},
		})
		_ = conn.Close()
		return
	}

	entry := &serviceEntry{
		alias:     proxy.Alias,
		hostname:  proxy.Hostname,
		ctrlConn:  conn,
		workQueue: make(chan net.Conn, 10),
	}

	s.mu.Lock()
	s.services[proxy.Alias] = entry
	s.mu.Unlock()

	s.registrar.RegisterService(proxy.Alias, proxy.Hostname)

	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: true}},
	}); err != nil {
		s.mu.Lock()
		delete(s.services, proxy.Alias)
		s.mu.Unlock()
		s.registrar.UnregisterService(proxy.Hostname)
		_ = conn.Close()
		return
	}

	defer func() {
		s.mu.Lock()
		delete(s.services, proxy.Alias)
		s.mu.Unlock()
		s.registrar.UnregisterService(proxy.Hostname)
		_ = conn.Close()
	}()

	for {
		env, err := protocol.ReadEnvelope(r)
		if err != nil {
			return
		}
		switch env.Payload.(type) {
		case *drppb.Envelope_Ping:
			_ = protocol.WriteEnvelope(conn, &drppb.Envelope{
				Payload: &drppb.Envelope_Pong{Pong: &drppb.Pong{}},
			})
		default:
			log.Printf("unexpected message in client session: %T", env.Payload)
		}
	}
}

func (s *Server) acceptWorkConn(conn net.Conn, nwc *drppb.NewWorkConn) {
	s.mu.RLock()
	entry, ok := s.services[nwc.ProxyAlias]
	s.mu.RUnlock()
	if !ok {
		_ = conn.Close()
		return
	}

	select {
	case entry.workQueue <- conn:
	case <-time.After(10 * time.Second):
		_ = conn.Close()
	}
}
