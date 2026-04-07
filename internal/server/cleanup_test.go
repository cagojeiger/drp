package server

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

func TestCleanupOnDisconnect(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	rt := router.New()
	var cleanupCalled atomic.Bool
	var cleanupRunID string

	h := &Handler{
		Token:  "test-token",
		Router: rt,
		OnControlClose: func(runID string) {
			cleanupRunID = runID
			cleanupCalled.Store(true)
		},
	}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-cleanup", 0)

	// 프록시 등록
	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"cleanup.test"},
	})
	msg.ReadMsg(reader) // NewProxyResp

	// 라우트 있는지 확인
	if _, ok := rt.Lookup("cleanup.test", "/"); !ok {
		t.Fatal("route should exist before disconnect")
	}

	// 연결 끊기
	clientConn.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleConnection should return")
	}

	// 라우트 정리됨
	if _, ok := rt.Lookup("cleanup.test", "/"); ok {
		t.Error("route should be removed after disconnect")
	}

	// OnControlClose 콜백 호출됨
	if !cleanupCalled.Load() {
		t.Error("OnControlClose should be called")
	}
	if cleanupRunID != "run-cleanup" {
		t.Errorf("runID = %q, want %q", cleanupRunID, "run-cleanup")
	}
}

func TestCleanupMultipleProxies(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	rt := router.New()
	h := &Handler{
		Token:          "test-token",
		Router:         rt,
		OnControlClose: func(string) {},
	}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-multi", 0)

	// 프록시 2개 등록
	msg.WriteMsg(writer, &msg.NewProxy{ProxyName: "web1", ProxyType: "http", CustomDomains: []string{"a.test"}})
	msg.ReadMsg(reader)
	msg.WriteMsg(writer, &msg.NewProxy{ProxyName: "web2", ProxyType: "http", CustomDomains: []string{"b.test"}})
	msg.ReadMsg(reader)

	// 연결 끊기
	clientConn.Close()
	<-done

	// 둘 다 정리됨
	if _, ok := rt.Lookup("a.test", "/"); ok {
		t.Error("a.test should be removed")
	}
	if _, ok := rt.Lookup("b.test", "/"); ok {
		t.Error("b.test should be removed")
	}
}
