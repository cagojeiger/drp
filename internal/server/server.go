// Package server implements the drps server.
// It integrates mesh, QUIC relay, control protocol and HTTP/HTTPS routing
// into a single runnable component.
package server

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	drppb "github.com/cagojeiger/drp/proto/drp"

	"github.com/cagojeiger/drp/internal/mesh"
	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/registry"
	"github.com/cagojeiger/drp/internal/relay"
	"github.com/cagojeiger/drp/internal/transport"
)

// Authenticator decides whether a Login request is accepted.
// Return ok=true to allow, or ok=false with a reason string to reject.
// nil means allow all.
type Authenticator func(login *drppb.Login) (ok bool, reason string)

// ProxyAuthorizer decides whether a NewProxy request is accepted.
// nil means allow all.
type ProxyAuthorizer func(proxy *drppb.NewProxy) (ok bool, reason string)

// ServerConfig holds all server startup parameters.
type ServerConfig struct {
	NodeID       string
	HTTPAddr     string // default ":80", use ":0" in tests
	HTTPSAddr    string // default ":443", use ":0" in tests
	ControlAddr  string // default ":9000", use ":0" in tests
	QuicAddr     string // default ":9001", use ":0" in tests
	MeshBindAddr string // default "0.0.0.0"
	MeshBindPort int    // default 7946, use 0 in tests
	JoinPeers    []string

	Authenticate   Authenticator
	AuthorizeProxy ProxyAuthorizer
}

// ServerAddrs holds the resolved listen addresses after startup.
type ServerAddrs struct {
	HTTP    string
	HTTPS   string
	Control string
	QUIC    string
	Mesh    string
}

// serviceEntry tracks a connected drpc client session.
type serviceEntry struct {
	alias     string
	hostname  string
	ctrlConn  net.Conn
	workQueue chan net.Conn // buffered channel for incoming work connections
}

// Server is the central drps component.
type Server struct {
	cfg      ServerConfig
	registry *registry.Registry
	mesh     *mesh.Mesh
	relay    *relay.RelayManager

	mu       sync.RWMutex
	services map[string]*serviceEntry // key = proxy_alias

	httpLn  net.Listener
	httpsLn net.Listener
	ctrlLn  net.Listener

	ready chan struct{}
}

// New creates a Server with sensible defaults filled in.
func New(cfg ServerConfig) *Server {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":80"
	}
	if cfg.HTTPSAddr == "" {
		cfg.HTTPSAddr = ":443"
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = ":9000"
	}
	if cfg.QuicAddr == "" {
		cfg.QuicAddr = ":9001"
	}
	if cfg.MeshBindAddr == "" {
		cfg.MeshBindAddr = "0.0.0.0"
	}
	// MeshBindPort 0 means "pick a random port" (for tests).
	// Production default (7946) is set in cmd/drps/main.go flags.
	return &Server{
		cfg:      cfg,
		services: make(map[string]*serviceEntry),
		ready:    make(chan struct{}),
	}
}

// Run starts the server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	// 1. Registry
	s.registry = registry.New()

	// 2. Mesh
	s.mesh = mesh.New(mesh.MeshConfig{
		NodeID:   s.cfg.NodeID,
		BindAddr: s.cfg.MeshBindAddr,
		BindPort: s.cfg.MeshBindPort,
	}, s.registry)
	if err := s.mesh.Create(); err != nil {
		return fmt.Errorf("mesh create: %w", err)
	}

	// 3. QUIC relay
	cert, err := relay.GenerateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("generate cert: %w", err)
	}
	s.relay = relay.NewRelayManager(cert)
	if err := s.relay.Listen(s.cfg.QuicAddr); err != nil {
		return fmt.Errorf("quic listen: %w", err)
	}

	_, quicPort, _ := net.SplitHostPort(s.relay.Addr().String())
	quicAdvertise := net.JoinHostPort(s.cfg.MeshBindAddr, quicPort)
	s.mesh.SetQuicAddr(quicAdvertise)

	// 4. TCP listeners
	s.httpLn, err = transport.Listen(s.cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("http listen: %w", err)
	}
	s.httpsLn, err = transport.Listen(s.cfg.HTTPSAddr)
	if err != nil {
		return fmt.Errorf("https listen: %w", err)
	}
	s.ctrlLn, err = transport.Listen(s.cfg.ControlAddr)
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}

	// 5. Join mesh peers
	if len(s.cfg.JoinPeers) > 0 {
		if _, err := s.mesh.Join(s.cfg.JoinPeers); err != nil {
			log.Printf("mesh join warning: %v", err)
		}
	}

	// 6. Ready
	close(s.ready)

	// 7. Accept loops
	go s.acceptLoop(ctx, s.httpLn, s.handleHTTP)
	go s.acceptLoop(ctx, s.httpsLn, s.handleHTTPS)
	go s.acceptLoop(ctx, s.ctrlLn, s.handleControl)
	go s.relayAcceptLoop(ctx)

	// 8. Block until context done
	<-ctx.Done()

	// 9. Cleanup
	_ = s.mesh.Leave(3 * time.Second)
	_ = s.relay.Close()
	_ = s.httpLn.Close()
	_ = s.httpsLn.Close()
	_ = s.ctrlLn.Close()
	return nil
}

// Ready returns a channel that is closed once all listeners are up.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Addr returns all resolved listen addresses. Only valid after Ready fires.
func (s *Server) Addr() ServerAddrs {
	return ServerAddrs{
		HTTP:    s.httpLn.Addr().String(),
		HTTPS:   s.httpsLn.Addr().String(),
		Control: s.ctrlLn.Addr().String(),
		QUIC:    s.relay.Addr().String(),
		Mesh:    s.mesh.LocalAddr(),
	}
}

// Lookup delegates to the mesh for external callers (e.g. e2e tests).
func (s *Server) Lookup(hostname string) (registry.ServiceInfo, bool) {
	return s.mesh.Lookup(hostname)
}

// ---------- accept loops ----------

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener, handler func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Listener was closed during shutdown.
			return
		}
		go handler(conn)
	}
}

func (s *Server) relayAcceptLoop(ctx context.Context) {
	for {
		conn, err := s.relay.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("relay accept error: %v", err)
			return
		}
		go s.handleRelayConn(conn)
	}
}

// ---------- HTTP/HTTPS ----------

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
		return // Can't route without SNI
	}

	s.routeRequest(hostname, conn, buf)
}

// ---------- routing ----------

func (s *Server) routeRequest(hostname string, userConn net.Conn, buffered []byte) {
	info, found := s.mesh.Lookup(hostname)
	if !found {
		_, _ = userConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 15\r\n\r\n502 Bad Gateway"))
		return
	}

	if info.IsLocal {
		s.localRoute(info, userConn, buffered)
	} else {
		s.remoteRelay(info, userConn, buffered)
	}
}

func (s *Server) localRoute(info registry.ServiceInfo, userConn net.Conn, buf []byte) {
	s.mu.RLock()
	entry, ok := s.services[info.ProxyAlias]
	s.mu.RUnlock()
	if !ok {
		_, _ = userConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 15\r\n\r\n502 Bad Gateway"))
		return
	}

	// Request a work connection from the client.
	if err := protocol.WriteEnvelope(entry.ctrlConn, &drppb.Envelope{
		Payload: &drppb.Envelope_ReqWorkConn{ReqWorkConn: &drppb.ReqWorkConn{
			ProxyAlias: info.ProxyAlias,
		}},
	}); err != nil {
		log.Printf("req work conn write error: %v", err)
		_, _ = userConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 15\r\n\r\n502 Bad Gateway"))
		return
	}

	// Wait for the work connection to arrive.
	var workConn net.Conn
	select {
	case workConn = <-entry.workQueue:
	case <-time.After(10 * time.Second):
		_, _ = userConn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\nContent-Length: 19\r\n\r\n504 Gateway Timeout"))
		return
	}
	defer func() { _ = workConn.Close() }()

	// Tell the client which proxy this work connection is for.
	_ = protocol.WriteEnvelope(workConn, &drppb.Envelope{
		Payload: &drppb.Envelope_StartWorkConn{StartWorkConn: &drppb.StartWorkConn{
			ProxyAlias: info.ProxyAlias,
		}},
	})

	// Flush the buffered initial bytes.
	_, _ = workConn.Write(buf)

	// Bidirectional pipe.
	go func() { _ = protocol.Pipe(userConn, workConn) }()
	_ = protocol.Pipe(workConn, userConn)
}

func (s *Server) remoteRelay(info registry.ServiceInfo, userConn net.Conn, buf []byte) {
	peerAddr := s.resolvePeerQuicAddr(info.NodeID)
	if peerAddr == "" {
		_, _ = userConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 15\r\n\r\n502 Bad Gateway"))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := s.relay.DialStream(ctx, peerAddr)
	if err != nil {
		log.Printf("relay dial to %s (%s) failed: %v", info.NodeID, peerAddr, err)
		_, _ = userConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 15\r\n\r\n502 Bad Gateway"))
		return
	}
	defer func() { _ = stream.Close() }()

	// Send RelayOpen header so the receiving node knows which proxy to invoke.
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

func (s *Server) resolvePeerQuicAddr(nodeID string) string {
	for _, member := range s.mesh.Members() {
		if member.Name != nodeID {
			continue
		}
		meta := make([]byte, len(member.Meta))
		copy(meta, member.Meta)
		var ns drppb.NodeServices
		if err := proto.Unmarshal(meta, &ns); err != nil || ns.QuicAddr == "" {
			_, quicPort, _ := net.SplitHostPort(s.relay.Addr().String())
			return fmt.Sprintf("%s:%s", member.Addr, quicPort)
		}
		return ns.QuicAddr
	}
	return ""
}

// ---------- control protocol ----------

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

	s.mesh.RegisterService(proxy.Alias, proxy.Hostname)

	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: true}},
	}); err != nil {
		s.mu.Lock()
		delete(s.services, proxy.Alias)
		s.mu.Unlock()
		s.mesh.UnregisterService(proxy.Hostname)
		_ = conn.Close()
		return
	}

	// Control loop — handle heartbeats and detect disconnection.
	defer func() {
		s.mu.Lock()
		delete(s.services, proxy.Alias)
		s.mu.Unlock()
		s.mesh.UnregisterService(proxy.Hostname)
		_ = conn.Close()
	}()

	for {
		env, err := protocol.ReadEnvelope(r)
		if err != nil {
			return // client disconnected
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

// ---------- relay accept ----------

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

	s.mu.RLock()
	entry, ok := s.services[ro.ProxyAlias]
	s.mu.RUnlock()
	if !ok {
		log.Printf("relay: service %s not found locally", ro.ProxyAlias)
		return
	}

	// Request a work connection from the client.
	if err := protocol.WriteEnvelope(entry.ctrlConn, &drppb.Envelope{
		Payload: &drppb.Envelope_ReqWorkConn{ReqWorkConn: &drppb.ReqWorkConn{
			ProxyAlias: ro.ProxyAlias,
		}},
	}); err != nil {
		return
	}

	var workConn net.Conn
	select {
	case workConn = <-entry.workQueue:
	case <-time.After(10 * time.Second):
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
