package proxy

import (
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

func TestProxyTimeout(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "slow.test",
		Location:  "/",
		ProxyName: "slow",
	})

	h := NewHandler(rt, func(name string) (*pool.Pool, bool) {
		return p, true
	}, testAESKey)
	h.ResponseTimeout = 200 * time.Millisecond

	// frpc: StartWorkConn 읽고 → 요청 읽고 → 응답 안 보냄 (슬로우 백엔드)
	go func() {
		defer frpcConn.Close()
		msg.ReadMsg(frpcConn) // StartWorkConn
		// HTTP 요청은 읽지만 응답을 안 보냄
		buf := make([]byte, 4096)
		frpcConn.Read(buf)
		// 무한 대기
		time.Sleep(10 * time.Second)
	}()

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "slow.test"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 504 {
		t.Errorf("status = %d, want 504", w.Code)
	}
}

func TestProxyNoTimeout(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "fast.test",
		Location:  "/",
		ProxyName: "fast",
	})

	h := NewHandler(rt, func(name string) (*pool.Pool, bool) {
		return p, true
	}, testAESKey)
	h.ResponseTimeout = 5 * time.Second

	go fakeFrpc(t, frpcConn, "fast response")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "fast.test"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "fast response" {
		t.Errorf("body = %q, want %q", w.Body.String(), "fast response")
	}
}
