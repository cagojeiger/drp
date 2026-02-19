package mesh

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
)

type GetWorkConnFunc func(hostname string) (net.Conn, error)

type MeshManager struct {
	nodeID      string
	controlPort int

	hasHostname func(string) bool
	getWorkConn GetWorkConnFunc

	peers         map[string]net.Conn
	peerAddresses map[string]string
	seenMessages  map[string]time.Time
	pending       map[string]chan *protocol.IHaveBody
	inflight      map[string]chan *protocol.IHaveBody
	mu            sync.RWMutex
}

func New(nodeID string, controlPort int, hasHostname func(string) bool, getWorkConn GetWorkConnFunc) *MeshManager {
	return &MeshManager{
		nodeID:        nodeID,
		controlPort:   controlPort,
		hasHostname:   hasHostname,
		getWorkConn:   getWorkConn,
		peers:         make(map[string]net.Conn),
		peerAddresses: make(map[string]string),
		seenMessages:  make(map[string]time.Time),
		pending:       make(map[string]chan *protocol.IHaveBody),
		inflight:      make(map[string]chan *protocol.IHaveBody),
	}
}

func (m *MeshManager) ConnectToPeers(specs []string) {
	for _, spec := range specs {
		if spec == "" {
			continue
		}
		conn, err := transport.Dial(spec)
		if err != nil {
			log.Printf("[mesh-%s] failed connecting to peer %s: %v", m.nodeID, spec, err)
			continue
		}

		err = protocol.WriteMsg(conn, protocol.MsgMeshHello, &protocol.MeshHelloBody{
			NodeID:      m.nodeID,
			Peers:       []string{},
			ControlPort: m.controlPort,
		})
		if err != nil {
			_ = conn.Close()
			log.Printf("[mesh-%s] failed sending hello to %s: %v", m.nodeID, spec, err)
			continue
		}

		msgType, body, err := protocol.ReadMsg(conn)
		if err != nil || msgType != protocol.MsgMeshHello {
			_ = conn.Close()
			log.Printf("[mesh-%s] invalid hello response from %s", m.nodeID, spec)
			continue
		}

		var hello protocol.MeshHelloBody
		if len(body) > 0 {
			if err := json.Unmarshal(body, &hello); err != nil {
				_ = conn.Close()
				continue
			}
		}

		if hello.NodeID == "" {
			_ = conn.Close()
			continue
		}

		m.mu.Lock()
		m.peers[hello.NodeID] = conn
		m.peerAddresses[hello.NodeID] = spec
		m.mu.Unlock()

		go m.peerLoop(hello.NodeID, conn)
		log.Printf("[mesh-%s] connected to peer %s at %s", m.nodeID, hello.NodeID, spec)
	}
}

func (m *MeshManager) HandlePeer(conn net.Conn, hello *protocol.MeshHelloBody) {
	if hello == nil || hello.NodeID == "" {
		_ = conn.Close()
		return
	}

	err := protocol.WriteMsg(conn, protocol.MsgMeshHello, &protocol.MeshHelloBody{
		NodeID:      m.nodeID,
		Peers:       []string{},
		ControlPort: m.controlPort,
	})
	if err != nil {
		_ = conn.Close()
		return
	}

	addr := conn.RemoteAddr().String()
	if hello.ControlPort > 0 {
		host, _, err := net.SplitHostPort(addr)
		if err == nil {
			addr = net.JoinHostPort(host, fmt.Sprintf("%d", hello.ControlPort))
		}
	}

	m.mu.Lock()
	m.peers[hello.NodeID] = conn
	m.peerAddresses[hello.NodeID] = addr
	m.mu.Unlock()

	go m.peerLoop(hello.NodeID, conn)
	log.Printf("[mesh-%s] accepted peer %s", m.nodeID, hello.NodeID)
}

func (m *MeshManager) peerLoop(peerID string, conn net.Conn) {
	defer func() {
		m.mu.Lock()
		delete(m.peers, peerID)
		m.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		msgType, body, err := protocol.ReadMsg(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[mesh-%s] peer loop error for %s: %v", m.nodeID, peerID, err)
			}
			return
		}

		switch msgType {
		case protocol.MsgWhoHas:
			var wb protocol.WhoHasBody
			if len(body) > 0 {
				if err := json.Unmarshal(body, &wb); err != nil {
					continue
				}
			}
			m.handleWhoHas(peerID, &wb)
		case protocol.MsgIHave:
			var ib protocol.IHaveBody
			if len(body) > 0 {
				if err := json.Unmarshal(body, &ib); err != nil {
					continue
				}
			}
			m.handleIHave(&ib)
		}
	}
}

func (m *MeshManager) handleWhoHas(senderID string, body *protocol.WhoHasBody) {
	if body.MsgID == "" || body.Hostname == "" {
		return
	}

	m.mu.Lock()
	if _, seen := m.seenMessages[body.MsgID]; seen {
		m.mu.Unlock()
		return
	}
	m.markSeenLocked(body.MsgID)
	m.mu.Unlock()

	if body.TTL <= 0 {
		return
	}

	if m.hasHostname(body.Hostname) {
		resp := &protocol.IHaveBody{
			MsgID:    body.MsgID,
			Hostname: body.Hostname,
			NodeID:   m.nodeID,
			Path:     body.Path,
		}
		m.mu.RLock()
		conn, ok := m.peers[senderID]
		m.mu.RUnlock()
		if ok {
			_ = protocol.WriteMsg(conn, protocol.MsgIHave, resp)
		}
		return
	}

	forward := &protocol.WhoHasBody{
		MsgID:    body.MsgID,
		Hostname: body.Hostname,
		TTL:      body.TTL - 1,
		Path:     append(body.Path, m.nodeID),
	}

	m.mu.RLock()
	for pid, conn := range m.peers {
		if pid != senderID {
			_ = protocol.WriteMsg(conn, protocol.MsgWhoHas, forward)
		}
	}
	m.mu.RUnlock()
}

func (m *MeshManager) handleIHave(body *protocol.IHaveBody) {
	if body.MsgID == "" {
		return
	}

	m.mu.Lock()
	ch, ok := m.pending[body.MsgID]
	if ok {
		delete(m.pending, body.MsgID)
	}
	m.mu.Unlock()

	if ok {
		select {
		case ch <- body:
		default:
		}
		return
	}

	if len(body.Path) == 0 {
		return
	}

	myIdx := -1
	for i, node := range body.Path {
		if node == m.nodeID {
			myIdx = i
			break
		}
	}

	if myIdx > 0 {
		prevNode := body.Path[myIdx-1]
		m.mu.RLock()
		conn, ok := m.peers[prevNode]
		m.mu.RUnlock()
		if ok {
			_ = protocol.WriteMsg(conn, protocol.MsgIHave, body)
		}
	}
}

func (m *MeshManager) FindService(hostname string) (*protocol.IHaveBody, error) {
	m.mu.RLock()
	peerCount := len(m.peers)
	m.mu.RUnlock()

	if peerCount == 0 {
		return nil, fmt.Errorf("no peers")
	}

	m.mu.Lock()
	existing, ok := m.inflight[hostname]
	if ok {
		m.mu.Unlock()
		select {
		case result := <-existing:
			return result, nil
		case <-time.After(3 * time.Second):
			return nil, fmt.Errorf("timeout finding %s", hostname)
		}
	}

	shared := make(chan *protocol.IHaveBody, 1)
	m.inflight[hostname] = shared
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.inflight, hostname)
		m.mu.Unlock()
	}()

	result, err := m.broadcast(hostname)
	if result != nil {
		select {
		case shared <- result:
		default:
		}
	}
	return result, err
}

func (m *MeshManager) broadcast(hostname string) (*protocol.IHaveBody, error) {
	msgID := protocol.GenerateID()
	ch := make(chan *protocol.IHaveBody, 1)

	m.mu.Lock()
	m.pending[msgID] = ch
	m.markSeenLocked(msgID)
	m.mu.Unlock()

	body := &protocol.WhoHasBody{
		MsgID:    msgID,
		Hostname: hostname,
		TTL:      5,
		Path:     []string{m.nodeID},
	}

	m.mu.RLock()
	for _, conn := range m.peers {
		_ = protocol.WriteMsg(conn, protocol.MsgWhoHas, body)
	}
	m.mu.RUnlock()

	select {
	case result := <-ch:
		return result, nil
	case <-time.After(3 * time.Second):
		m.mu.Lock()
		delete(m.pending, msgID)
		m.mu.Unlock()
		return nil, fmt.Errorf("no service found for %s (timeout)", hostname)
	}
}

func (m *MeshManager) OpenRelay(hostname, targetNodeID string, path []string) (net.Conn, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("relay path is empty")
	}

	nextHop := path[0]
	remaining := path[1:]

	m.mu.RLock()
	addr, ok := m.peerAddresses[nextHop]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown next hop: %s", nextHop)
	}

	conn, err := transport.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial next hop %s (%s): %w", nextHop, addr, err)
	}

	err = protocol.WriteMsg(conn, protocol.MsgRelayOpen, &protocol.RelayOpenBody{
		RelayID:  protocol.GenerateID(),
		Hostname: hostname,
		NextHops: remaining,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}

	return conn, nil
}

func (m *MeshManager) HandleRelayOpen(conn net.Conn, body *protocol.RelayOpenBody) {
	if body.Hostname == "" || body.RelayID == "" {
		_ = conn.Close()
		return
	}

	if len(body.NextHops) == 0 {
		workConn, err := m.getWorkConn(body.Hostname)
		if err != nil {
			log.Printf("[mesh-%s] relay %s: no work conn for %s: %v", m.nodeID, body.RelayID, body.Hostname, err)
			_ = conn.Close()
			return
		}
		go func() { _ = protocol.Pipe(conn, workConn) }()
		_ = protocol.Pipe(workConn, conn)
		return
	}

	nextHop := body.NextHops[0]
	remaining := body.NextHops[1:]

	m.mu.RLock()
	addr, ok := m.peerAddresses[nextHop]
	m.mu.RUnlock()
	if !ok {
		log.Printf("[mesh-%s] relay %s: unknown next hop %s", m.nodeID, body.RelayID, nextHop)
		_ = conn.Close()
		return
	}

	nextConn, err := transport.Dial(addr)
	if err != nil {
		log.Printf("[mesh-%s] relay %s: dial %s failed: %v", m.nodeID, body.RelayID, nextHop, err)
		_ = conn.Close()
		return
	}

	err = protocol.WriteMsg(nextConn, protocol.MsgRelayOpen, &protocol.RelayOpenBody{
		RelayID:  body.RelayID,
		Hostname: body.Hostname,
		NextHops: remaining,
	})
	if err != nil {
		_ = conn.Close()
		_ = nextConn.Close()
		return
	}

	go func() { _ = protocol.Pipe(conn, nextConn) }()
	_ = protocol.Pipe(nextConn, conn)
}

func (m *MeshManager) HasPeers() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.peers) > 0
}

func (m *MeshManager) markSeenLocked(msgID string) {
	now := time.Now()
	if len(m.seenMessages) > 1000 {
		cutoff := now.Add(-30 * time.Second)
		for k, v := range m.seenMessages {
			if v.Before(cutoff) {
				delete(m.seenMessages, k)
			}
		}
	}
	m.seenMessages[msgID] = now
}
