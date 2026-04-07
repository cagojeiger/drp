package server

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

func TestReconnectSameRunID(t *testing.T) {
	rt := router.New()
	var closedCount atomic.Int32

	h := &Handler{
		Token:  "test-token",
		Router: rt,
		OnControlClose: func(runID string) {
			closedCount.Add(1)
		},
	}

	// 첫 번째 연결
	s1, c1 := net.Pipe()
	done1 := make(chan struct{})
	go func() {
		h.HandleConnection(s1)
		close(done1)
	}()

	reader1, writer1 := frpcLogin(t, c1, "test-token", "same-run-id", 0)

	// 프록시 등록
	msg.WriteMsg(writer1, &msg.NewProxy{
		ProxyName: "web", ProxyType: "http", CustomDomains: []string{"app.test"},
	})
	msg.ReadMsg(reader1)

	if _, ok := rt.Lookup("app.test", "/"); !ok {
		t.Fatal("route should exist after first login")
	}

	// 두 번째 연결 (같은 RunID)
	s2, c2 := net.Pipe()
	done2 := make(chan struct{})
	go func() {
		h.HandleConnection(s2)
		close(done2)
	}()

	reader2, writer2 := frpcLogin(t, c2, "test-token", "same-run-id", 0)

	// 첫 번째 연결이 종료되어야 함
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("old control should be closed on reconnect")
	}

	// old 라우트가 정리됨
	time.Sleep(50 * time.Millisecond)
	if _, ok := rt.Lookup("app.test", "/"); ok {
		t.Error("old route should be removed")
	}

	// 새 연결로 프록시 재등록 가능
	msg.WriteMsg(writer2, &msg.NewProxy{
		ProxyName: "web", ProxyType: "http", CustomDomains: []string{"app.test"},
	})
	resp, _ := msg.ReadMsg(reader2)
	if resp.(*msg.NewProxyResp).Error != "" {
		t.Errorf("re-register should succeed: %s", resp.(*msg.NewProxyResp).Error)
	}

	// 정리
	c2.Close()
	<-done2

	// OnControlClose가 2번 호출됨 (old + new)
	if closedCount.Load() != 2 {
		t.Errorf("OnControlClose called %d times, want 2", closedCount.Load())
	}
}

func TestReconnectDifferentRunID(t *testing.T) {
	rt := router.New()
	h := &Handler{
		Token:          "test-token",
		Router:         rt,
		OnControlClose: func(string) {},
	}

	// 첫 번째 연결
	s1, c1 := net.Pipe()
	defer c1.Close()
	go h.HandleConnection(s1)

	reader1, writer1 := frpcLogin(t, c1, "test-token", "run-a", 0)
	msg.WriteMsg(writer1, &msg.NewProxy{
		ProxyName: "webA", ProxyType: "http", CustomDomains: []string{"a.test"},
	})
	msg.ReadMsg(reader1)

	// 두 번째 연결 (다른 RunID) — 독립 공존
	s2, c2 := net.Pipe()
	defer c2.Close()
	go h.HandleConnection(s2)

	reader2, writer2 := frpcLogin(t, c2, "test-token", "run-b", 0)
	msg.WriteMsg(writer2, &msg.NewProxy{
		ProxyName: "webB", ProxyType: "http", CustomDomains: []string{"b.test"},
	})
	msg.ReadMsg(reader2)

	// 둘 다 존재
	if _, ok := rt.Lookup("a.test", "/"); !ok {
		t.Error("a.test should exist")
	}
	if _, ok := rt.Lookup("b.test", "/"); !ok {
		t.Error("b.test should exist")
	}
}
