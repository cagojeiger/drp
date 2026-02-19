package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/cagojeiger/drp/internal/mesh"
	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
)

type serviceEntry struct {
	alias     string
	ctrlConn  net.Conn
	workQueue chan net.Conn
}

type Config struct {
	NodeID      string
	HTTPPort    int
	ControlPort int
	Peers       string
	Verbose     bool
}

type Server struct {
	cfg      Config
	localMap map[string]*serviceEntry
	mapMu    sync.RWMutex
	mesh     *mesh.MeshManager
	ready    chan struct{}
	httpAddr string
	ctrlAddr string
}

func New(cfg Config) *Server {
	s := &Server{
		cfg:      cfg,
		localMap: make(map[string]*serviceEntry),
		ready:    make(chan struct{}),
	}
	s.mesh = mesh.New(cfg.NodeID, cfg.ControlPort, s.hasHostname, s.getWorkConn, transport.TCP{})
	return s
}

func (s *Server) Run(ctx context.Context) error {
	httpLn, err := transport.Listen(fmt.Sprintf(":%d", s.cfg.HTTPPort))
	if err != nil {
		return fmt.Errorf("http listen: %w", err)
	}
	defer func() { _ = httpLn.Close() }()

	ctrlLn, err := transport.Listen(fmt.Sprintf(":%d", s.cfg.ControlPort))
	if err != nil {
		return fmt.Errorf("control listen: %w", err)
	}
	defer func() { _ = ctrlLn.Close() }()

	s.httpAddr = httpLn.Addr().String()
	s.ctrlAddr = ctrlLn.Addr().String()
	log.Printf("[drps-%s] HTTP on %s, Control on %s", s.cfg.NodeID, s.httpAddr, s.ctrlAddr)

	if s.cfg.Peers != "" {
		peers := strings.Split(s.cfg.Peers, ",")
		for i := range peers {
			peers[i] = strings.TrimSpace(peers[i])
		}
		s.mesh.ConnectToPeers(peers)
	}

	close(s.ready)
	log.Printf("[drps-%s] ready", s.cfg.NodeID)

	go s.acceptLoop(httpLn, s.handleHTTP)
	go s.acceptLoop(ctrlLn, s.handleControl)

	<-ctx.Done()
	return ctx.Err()
}

func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

func (s *Server) Addr() (httpAddr, ctrlAddr string) {
	return s.httpAddr, s.ctrlAddr
}

func (s *Server) acceptLoop(ln net.Listener, handler func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handler(conn)
	}
}

func (s *Server) handleHTTP(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for !bytes.Contains(buf, []byte("\r\n\r\n")) {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(tmp)
		if err != nil {
			return
		}
		buf = append(buf, tmp[:n]...)
		if len(buf) > 65536 {
			_, _ = conn.Write([]byte("HTTP/1.1 431 Request Header Fields Too Large\r\n\r\n"))
			return
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	hostname := protocol.ExtractHost(buf)
	if hostname == "" {
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"))
		return
	}

	s.mapMu.RLock()
	entry, local := s.localMap[hostname]
	s.mapMu.RUnlock()

	if local {
		s.handleLocalHit(conn, buf, hostname, entry)
		return
	}

	s.handleMeshRelay(conn, buf, hostname)
}

func (s *Server) handleLocalHit(conn net.Conn, buf []byte, hostname string, entry *serviceEntry) {
	if err := protocol.WriteMsg(entry.ctrlConn, protocol.MsgReqWorkConn, &protocol.ReqWorkConnBody{}); err != nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	var workConn net.Conn
	select {
	case workConn = <-entry.workQueue:
	case <-time.After(10 * time.Second):
		_, _ = conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\n\r\n"))
		return
	}

	if err := protocol.WriteMsg(workConn, protocol.MsgStartWorkConn, &protocol.StartWorkConnBody{Hostname: hostname}); err != nil {
		_ = workConn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	if _, err := workConn.Write(buf); err != nil {
		_ = workConn.Close()
		return
	}

	go func() { _ = protocol.Pipe(conn, workConn) }()
	_ = protocol.Pipe(workConn, conn)
}

func (s *Server) handleMeshRelay(conn net.Conn, buf []byte, hostname string) {
	result, err := s.mesh.FindService(hostname)
	if err != nil || result == nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}

	targetNode := result.NodeID
	whoPath := result.Path

	var relayPath []string
	if len(whoPath) > 1 {
		relayPath = append(relayPath, whoPath[1:]...)
	}
	relayPath = append(relayPath, targetNode)

	relayConn, err := s.mesh.OpenRelay(hostname, targetNode, relayPath)
	if err != nil {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	if _, err := relayConn.Write(buf); err != nil {
		_ = relayConn.Close()
		return
	}

	go func() { _ = protocol.Pipe(conn, relayConn) }()
	_ = protocol.Pipe(relayConn, conn)
}

func (s *Server) handleControl(conn net.Conn) {
	msgType, body, err := protocol.ReadMsg(conn)
	if err != nil {
		_ = conn.Close()
		return
	}

	switch msgType {
	case protocol.MsgLogin:
		var lb protocol.LoginBody
		if len(body) > 0 {
			_ = json.Unmarshal(body, &lb)
		}
		s.clientSession(conn, &lb)

	case protocol.MsgMeshHello:
		var hb protocol.MeshHelloBody
		if len(body) > 0 {
			_ = json.Unmarshal(body, &hb)
		}
		s.mesh.HandlePeer(conn, &hb)

	case protocol.MsgNewWorkConn:
		var wb protocol.NewWorkConnBody
		if len(body) > 0 {
			_ = json.Unmarshal(body, &wb)
		}
		s.acceptWorkConn(conn, &wb)

	case protocol.MsgRelayOpen:
		var rb protocol.RelayOpenBody
		if len(body) > 0 {
			_ = json.Unmarshal(body, &rb)
		}
		s.mesh.HandleRelayOpen(conn, &rb)

	default:
		_ = conn.Close()
	}
}

func (s *Server) clientSession(conn net.Conn, login *protocol.LoginBody) {
	alias := login.Alias
	if alias == "" {
		alias = "unknown"
	}

	if err := protocol.WriteMsg(conn, protocol.MsgLoginResp, &protocol.LoginRespBody{OK: true, Message: "ok"}); err != nil {
		_ = conn.Close()
		return
	}
	log.Printf("[drps-%s] client %s logged in", s.cfg.NodeID, alias)

	msgType, body, err := protocol.ReadMsg(conn)
	if err != nil || msgType != protocol.MsgNewProxy {
		_ = conn.Close()
		return
	}

	var np protocol.NewProxyBody
	if len(body) > 0 {
		_ = json.Unmarshal(body, &np)
	}

	if np.Hostname == "" {
		_ = protocol.WriteMsg(conn, protocol.MsgNewProxyResp, &protocol.NewProxyRespBody{OK: false, Message: "missing hostname"})
		_ = conn.Close()
		return
	}

	entry := &serviceEntry{
		alias:     np.Alias,
		ctrlConn:  conn,
		workQueue: make(chan net.Conn, 64),
	}

	s.mapMu.Lock()
	s.localMap[np.Hostname] = entry
	s.mapMu.Unlock()

	if err := protocol.WriteMsg(conn, protocol.MsgNewProxyResp, &protocol.NewProxyRespBody{OK: true, Message: "ok"}); err != nil {
		s.mapMu.Lock()
		delete(s.localMap, np.Hostname)
		s.mapMu.Unlock()
		_ = conn.Close()
		return
	}
	log.Printf("[drps-%s] registered %s -> %s", s.cfg.NodeID, alias, np.Hostname)

	defer func() {
		s.mapMu.Lock()
		delete(s.localMap, np.Hostname)
		s.mapMu.Unlock()
		_ = conn.Close()
		log.Printf("[drps-%s] client %s (%s) disconnected", s.cfg.NodeID, alias, np.Hostname)
	}()

	for {
		_, _, err := protocol.ReadMsg(conn)
		if err != nil {
			return
		}
	}
}

func (s *Server) acceptWorkConn(conn net.Conn, body *protocol.NewWorkConnBody) {
	alias := body.Alias

	s.mapMu.RLock()
	for _, entry := range s.localMap {
		if entry.alias == alias {
			s.mapMu.RUnlock()
			select {
			case entry.workQueue <- conn:
			default:
				_ = conn.Close()
			}
			return
		}
	}
	s.mapMu.RUnlock()
	_ = conn.Close()
}

func (s *Server) hasHostname(hostname string) bool {
	s.mapMu.RLock()
	defer s.mapMu.RUnlock()
	_, ok := s.localMap[hostname]
	return ok
}

func (s *Server) getWorkConn(hostname string) (net.Conn, error) {
	s.mapMu.RLock()
	entry, ok := s.localMap[hostname]
	s.mapMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no local service for %s", hostname)
	}

	if err := protocol.WriteMsg(entry.ctrlConn, protocol.MsgReqWorkConn, &protocol.ReqWorkConnBody{}); err != nil {
		return nil, err
	}

	select {
	case workConn := <-entry.workQueue:
		if err := protocol.WriteMsg(workConn, protocol.MsgStartWorkConn, &protocol.StartWorkConnBody{Hostname: hostname}); err != nil {
			_ = workConn.Close()
			return nil, err
		}
		return workConn, nil
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("work conn timeout for %s", hostname)
	}
}
