package mesh

import (
	"net"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/transport"
)

func wirePeers(t *testing.T, a, b *MeshManager) func() {
	t.Helper()
	cA, cB := net.Pipe()

	a.mu.Lock()
	a.peers[b.nodeID] = cA
	a.peerAddresses[b.nodeID] = "pipe:" + b.nodeID
	a.mu.Unlock()

	b.mu.Lock()
	b.peers[a.nodeID] = cB
	b.peerAddresses[a.nodeID] = "pipe:" + a.nodeID
	b.mu.Unlock()

	go a.peerLoop(b.nodeID, cA)
	go b.peerLoop(a.nodeID, cB)

	return func() {
		cA.Close()
		cB.Close()
	}
}

func TestFindService_OneHop(t *testing.T) {
	a := New("A", 9000, func(h string) bool { return h == "myapp.example.com" }, nil, transport.TCP{})
	b := New("B", 9001, func(string) bool { return false }, nil, transport.TCP{})

	cleanup := wirePeers(t, a, b)
	defer cleanup()

	result, err := b.FindService("myapp.example.com")
	if err != nil {
		t.Fatalf("FindService failed: %v", err)
	}
	if result.NodeID != "A" {
		t.Fatalf("expected node A, got %s", result.NodeID)
	}
	if result.Hostname != "myapp.example.com" {
		t.Fatalf("expected hostname myapp.example.com, got %s", result.Hostname)
	}
}

func TestFindService_Timeout(t *testing.T) {
	a := New("A", 9000, func(string) bool { return false }, nil, transport.TCP{})
	b := New("B", 9001, func(string) bool { return false }, nil, transport.TCP{})

	cleanup := wirePeers(t, a, b)
	defer cleanup()

	_, err := b.FindService("unknown.example.com")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestFindService_NoPeers(t *testing.T) {
	m := New("A", 9000, func(string) bool { return false }, nil, transport.TCP{})

	_, err := m.FindService("any.example.com")
	if err == nil {
		t.Fatal("expected no peers error, got nil")
	}
}

func TestHandleWhoHas_SeenDuplicate(t *testing.T) {
	a := New("A", 9000, func(h string) bool { return h == "svc.example.com" }, nil, transport.TCP{})
	b := New("B", 9001, func(string) bool { return false }, nil, transport.TCP{})

	cleanup := wirePeers(t, a, b)
	defer cleanup()

	result1, err := b.FindService("svc.example.com")
	if err != nil {
		t.Fatalf("first FindService failed: %v", err)
	}
	if result1.NodeID != "A" {
		t.Fatalf("expected node A, got %s", result1.NodeID)
	}

	result2, err := b.FindService("svc.example.com")
	if err != nil {
		t.Fatalf("second FindService failed: %v", err)
	}
	if result2.NodeID != "A" {
		t.Fatalf("expected node A again, got %s", result2.NodeID)
	}
}

func TestMarkSeen_Eviction(t *testing.T) {
	m := New("A", 9000, nil, nil, transport.TCP{})

	m.mu.Lock()
	old := time.Now().Add(-60 * time.Second)
	for i := 0; i < 1001; i++ {
		m.seenMessages[string(rune(i))] = old
	}
	m.markSeenLocked("trigger-eviction")
	remaining := len(m.seenMessages)
	m.mu.Unlock()

	if remaining > 100 {
		t.Fatalf("expected eviction to clean old entries, got %d remaining", remaining)
	}
}
