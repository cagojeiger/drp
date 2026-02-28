// Package server implements the drps server.
package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	drppb "github.com/cagojeiger/drp/proto/drp"

	"github.com/cagojeiger/drp/internal/mesh"
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
	cfg       ServerConfig
	lookup    ServiceLookup
	broker    WorkConnBroker
	relayer   RelayStreamer
	registrar ServiceRegistrar

	registry *registry.Registry
	mesh     *mesh.Mesh
	relay    *relay.RelayManager

	mu       sync.RWMutex
	services map[string]*serviceEntry // key = proxy_alias

	httpLn, httpsLn, ctrlLn net.Listener

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
	if err := s.init(); err != nil {
		return err
	}
	close(s.ready)
	s.serve(ctx)
	<-ctx.Done()
	s.shutdown()
	return nil
}

func (s *Server) init() error {
	s.registry = registry.New()
	s.mesh = mesh.New(mesh.MeshConfig{
		NodeID:   s.cfg.NodeID,
		BindAddr: s.cfg.MeshBindAddr,
		BindPort: s.cfg.MeshBindPort,
	}, s.registry)
	if err := s.mesh.Create(); err != nil {
		return fmt.Errorf("mesh create: %w", err)
	}
	s.lookup = s.mesh
	s.registrar = s.mesh

	cert, err := relay.GenerateSelfSignedCert()
	if err != nil {
		return fmt.Errorf("generate cert: %w", err)
	}
	s.relay = relay.NewRelayManager(cert)
	if err := s.relay.Listen(s.cfg.QuicAddr); err != nil {
		return fmt.Errorf("quic listen: %w", err)
	}
	s.relayer = &realRelayer{members: s.mesh.Members, relay: s.relay}
	s.broker = &realBroker{mu: &s.mu, services: s.services}

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

	if len(s.cfg.JoinPeers) > 0 {
		if _, err := s.mesh.Join(s.cfg.JoinPeers); err != nil {
			log.Printf("mesh join warning: %v", err)
		}
	}

	_, quicPort, _ := net.SplitHostPort(s.relay.Addr().String())
	quicAdvertise := net.JoinHostPort(s.cfg.MeshBindAddr, quicPort)
	s.mesh.SetQuicAddr(quicAdvertise)

	return nil
}

func (s *Server) serve(ctx context.Context) {
	go s.acceptLoop(ctx, s.httpLn, s.handleHTTP)
	go s.acceptLoop(ctx, s.httpsLn, s.handleHTTPS)
	go s.acceptLoop(ctx, s.ctrlLn, s.handleControl)
	go s.relayAcceptLoop(ctx)
}

func (s *Server) shutdown() {
	_ = s.mesh.Leave(3 * time.Second)
	_ = s.relay.Close()
	_ = s.httpLn.Close()
	_ = s.httpsLn.Close()
	_ = s.ctrlLn.Close()
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
	return s.lookup.Lookup(hostname)
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
