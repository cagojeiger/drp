package mesh

import (
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/registry"
)

func createMesh(t *testing.T, nodeID string) (*Mesh, *registry.Registry) {
	t.Helper()

	reg := registry.New()
	m := New(MeshConfig{
		NodeID:   nodeID,
		BindAddr: "127.0.0.1",
		BindPort: 0,
	}, reg)
	if err := m.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Leave(1 * time.Second) })

	return m, reg
}

func waitForCondition(t *testing.T, timeout time.Duration, interval time.Duration, condition func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(interval)
	}

	t.Fatalf("timeout waiting for: %s", msg)
}

func TestMeshThreeNodeCluster(t *testing.T) {
	const (
		nodeAID = "node-a"
		nodeBID = "node-b"
		nodeCID = "node-c"
	)

	a, _ := createMesh(t, nodeAID)
	b, _ := createMesh(t, nodeBID)
	c, _ := createMesh(t, nodeCID)

	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatalf("B join failed: %v", err)
	}
	if _, err := c.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatalf("C join failed: %v", err)
	}

	waitForCondition(t, 3*time.Second, 100*time.Millisecond, func() bool {
		return len(a.Members()) == 3 && len(b.Members()) == 3 && len(c.Members()) == 3
	}, "all nodes see 3 cluster members")

	a.RegisterService("myapp", "myapp.example.com")

	waitForCondition(t, 5*time.Second, 100*time.Millisecond, func() bool {
		_, found := b.Lookup("myapp.example.com")
		return found
	}, "B receives myapp service")

	waitForCondition(t, 5*time.Second, 100*time.Millisecond, func() bool {
		_, found := c.Lookup("myapp.example.com")
		return found
	}, "C receives myapp service")

	infoB, found := b.Lookup("myapp.example.com")
	if !found {
		t.Fatal("service missing on B after propagation")
	}
	if infoB.NodeID != nodeAID {
		t.Fatalf("B expected NodeID %q, got %q", nodeAID, infoB.NodeID)
	}

	infoC, found := c.Lookup("myapp.example.com")
	if !found {
		t.Fatal("service missing on C after propagation")
	}
	if infoC.NodeID != nodeAID {
		t.Fatalf("C expected NodeID %q, got %q", nodeAID, infoC.NodeID)
	}
}

func TestMeshNodeLeave(t *testing.T) {
	a, _ := createMesh(t, "node-a")
	b, _ := createMesh(t, "node-b")
	c, _ := createMesh(t, "node-c")

	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatalf("B join failed: %v", err)
	}
	if _, err := c.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatalf("C join failed: %v", err)
	}

	waitForCondition(t, 3*time.Second, 100*time.Millisecond, func() bool {
		return len(a.Members()) == 3 && len(b.Members()) == 3 && len(c.Members()) == 3
	}, "all nodes see 3 cluster members")

	a.RegisterService("svc1", "svc1.example.com")

	waitForCondition(t, 5*time.Second, 100*time.Millisecond, func() bool {
		_, found := b.Lookup("svc1.example.com")
		return found
	}, "B receives svc1 service")

	if err := a.Leave(1 * time.Second); err != nil {
		t.Fatalf("A leave failed: %v", err)
	}

	waitForCondition(t, 10*time.Second, 200*time.Millisecond, func() bool {
		_, found := b.Lookup("svc1.example.com")
		return !found
	}, "B removes svc1 after A leaves")
}

func TestMeshServiceUnregister(t *testing.T) {
	a, _ := createMesh(t, "node-a")
	b, _ := createMesh(t, "node-b")

	if _, err := b.Join([]string{a.LocalAddr()}); err != nil {
		t.Fatalf("B join failed: %v", err)
	}

	waitForCondition(t, 3*time.Second, 100*time.Millisecond, func() bool {
		return len(a.Members()) == 2 && len(b.Members()) == 2
	}, "both nodes see 2 cluster members")

	a.RegisterService("svc1", "svc1.example.com")

	waitForCondition(t, 5*time.Second, 100*time.Millisecond, func() bool {
		_, found := b.Lookup("svc1.example.com")
		return found
	}, "B receives svc1 service")

	a.UnregisterService("svc1.example.com")

	waitForCondition(t, 5*time.Second, 100*time.Millisecond, func() bool {
		_, found := b.Lookup("svc1.example.com")
		return !found
	}, "B removes svc1 after unregister")
}
