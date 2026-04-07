package server

import (
	"crypto/md5"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

func frpcLoginHelper(t *testing.T, conn net.Conn, token, runID string, poolCount int) (reader, writer interface{ Read([]byte) (int, error) }) {
	t.Helper()
	timestamp := int64(100)
	raw := fmt.Sprintf("%s%d", token, timestamp)
	sum := md5.Sum([]byte(raw))
	privKey := fmt.Sprintf("%x", sum)

	msg.WriteMsg(conn, &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: privKey,
		Timestamp:    timestamp,
		RunID:        runID,
		PoolCount:    poolCount,
	})

	resp, _ := msg.ReadMsg(conn)
	if resp.(*msg.LoginResp).Error != "" {
		t.Fatalf("login failed: %s", resp.(*msg.LoginResp).Error)
	}

	key := crypto.DeriveKey(token)
	r, _ := crypto.NewCryptoReader(conn, key)
	w, _ := crypto.NewCryptoWriter(conn, key)
	return r, w.(interface{ Read([]byte) (int, error) })
}

func TestRouterIntegrationNewProxy(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	h := &Handler{Token: "test-token", Router: rt}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// NewProxy → 라우터에 등록되어야 함
	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"app.example.com"},
	})

	resp, _ := msg.ReadMsg(reader)
	proxyResp := resp.(*msg.NewProxyResp)
	if proxyResp.Error != "" {
		t.Fatalf("NewProxy failed: %s", proxyResp.Error)
	}

	// 라우터에서 찾을 수 있어야 함
	cfg, ok := rt.Lookup("app.example.com", "/")
	if !ok {
		t.Fatal("route should be registered")
	}
	if cfg.ProxyName != "web" {
		t.Errorf("ProxyName = %q, want %q", cfg.ProxyName, "web")
	}
}

func TestRouterIntegrationMultipleDomains(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	h := &Handler{Token: "test-token", Router: rt}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "multi",
		ProxyType:     "http",
		CustomDomains: []string{"a.com", "b.com"},
	})

	resp, _ := msg.ReadMsg(reader)
	if resp.(*msg.NewProxyResp).Error != "" {
		t.Fatalf("NewProxy failed: %s", resp.(*msg.NewProxyResp).Error)
	}

	for _, domain := range []string{"a.com", "b.com"} {
		if _, ok := rt.Lookup(domain, "/"); !ok {
			t.Errorf("%s should be registered", domain)
		}
	}
}

func TestRouterIntegrationCloseProxy(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	h := &Handler{Token: "test-token", Router: rt}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// 등록
	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"app.com"},
	})
	msg.ReadMsg(reader) // NewProxyResp

	// 해제
	msg.WriteMsg(writer, &msg.CloseProxy{ProxyName: "web"})

	// 약간 대기 (서버가 처리할 시간)
	time.Sleep(50 * time.Millisecond)

	if _, ok := rt.Lookup("app.com", "/"); ok {
		t.Error("route should be removed after CloseProxy")
	}
}

func TestRouterIntegrationDisconnectCleanup(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	rt := router.New()
	h := &Handler{Token: "test-token", Router: rt}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// 등록
	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"app.com", "api.com"},
	})
	msg.ReadMsg(reader) // NewProxyResp

	// 연결 끊기
	clientConn.Close()

	// controlLoop이 종료될 때까지 대기
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleConnection should return after disconnect")
	}

	// 모든 라우트가 정리되어야 함
	if _, ok := rt.Lookup("app.com", "/"); ok {
		t.Error("app.com should be removed after disconnect")
	}
	if _, ok := rt.Lookup("api.com", "/"); ok {
		t.Error("api.com should be removed after disconnect")
	}
}

func TestRouterIntegrationDuplicateDomain(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	// 미리 등록
	rt.Add(&router.RouteConfig{Domain: "app.com", Location: "/", ProxyName: "existing"})

	h := &Handler{Token: "test-token", Router: rt}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"app.com"},
	})

	resp, _ := msg.ReadMsg(reader)
	proxyResp := resp.(*msg.NewProxyResp)
	if proxyResp.Error == "" {
		t.Error("should reject duplicate domain")
	}
}
