package registry

import (
	"fmt"
	"sync"
	"testing"
)

func TestRegisterAndLookup(t *testing.T) {
	r := New()
	r.Register("myapp.example.com", "node-A", "myapp", true)
	got, ok := r.Lookup("myapp.example.com")
	if !ok {
		t.Fatal("expected service to exist")
	}
	want := ServiceInfo{NodeID: "node-A", ProxyAlias: "myapp", Hostname: "myapp.example.com", IsLocal: true}
	if got != want {
		t.Fatalf("unexpected service info: got=%+v want=%+v", got, want)
	}
}

func TestUnregister(t *testing.T) {
	r := New()
	r.Register("x.example.com", "node-A", "x", true)
	r.Unregister("x.example.com")
	if _, ok := r.Lookup("x.example.com"); ok {
		t.Fatal("expected service to be removed")
	}
}

func TestRemoveByNode(t *testing.T) {
	r := New()
	r.Register("a.example.com", "node-A", "a", true)
	r.Register("b.example.com", "node-A", "b", false)
	r.Register("c.example.com", "node-B", "c", false)
	removed := r.RemoveByNode("node-A")
	if len(removed) != 2 || r.Count() != 1 {
		t.Fatalf("unexpected result: removed=%d count=%d", len(removed), r.Count())
	}
}

func TestLocalServices(t *testing.T) {
	r := New()
	r.Register("a.example.com", "node-A", "a", true)
	r.Register("b.example.com", "node-B", "b", false)
	r.Register("c.example.com", "node-C", "c", true)
	local := r.LocalServices()
	if len(local) != 2 {
		t.Fatalf("expected 2 local services, got %d", len(local))
	}
	for _, s := range local {
		if !s.IsLocal || s.Hostname == "b.example.com" {
			t.Fatalf("expected only local services, got %+v", s)
		}
	}
}

func TestSnapshot(t *testing.T) {
	r := New()
	r.Register("a.example.com", "node-A", "a", true)
	r.Register("b.example.com", "node-B", "b", false)
	snapshot := r.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected snapshot size 2, got %d", len(snapshot))
	}
	delete(snapshot, "a.example.com")
	if r.Count() != 2 {
		t.Fatalf("expected registry unchanged, got %d", r.Count())
	}
}

func TestOverwrite(t *testing.T) {
	r := New()
	r.Register("x.example.com", "node-A", "x", true)
	r.Register("x.example.com", "node-B", "x", false)
	got, ok := r.Lookup("x.example.com")
	if !ok || got.NodeID != "node-B" {
		t.Fatalf("expected overwritten node-B, got %+v", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				host := fmt.Sprintf("svc-%d-%d.example.com", worker, j)
				r.Register(host, fmt.Sprintf("node-%d", worker), "svc", worker%2 == 0)
			}
		}(i)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = r.Lookup(fmt.Sprintf("svc-%d-%d.example.com", worker, j))
			}
		}(i)
	}
	wg.Wait()
}
