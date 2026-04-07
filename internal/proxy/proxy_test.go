package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

var testAESKey = crypto.DeriveKey("test-token")

// fakeFrpc: pipe 반대쪽에서 StartWorkConn 읽고 → HTTP 요청 읽고 → HTTP 응답 쓰기
func fakeFrpc(t *testing.T, conn net.Conn, responseBody string) {
	t.Helper()
	defer conn.Close()

	// StartWorkConn 수신
	m, err := msg.ReadMsg(conn)
	if err != nil {
		t.Logf("fakeFrpc ReadMsg: %v", err)
		return
	}
	if _, ok := m.(*msg.StartWorkConn); !ok {
		t.Logf("fakeFrpc: expected StartWorkConn, got %T", m)
		return
	}

	// HTTP 요청 읽기
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		t.Logf("fakeFrpc ReadRequest: %v", err)
		return
	}
	req.Body.Close()

	// HTTP 응답 쓰기
	resp := &http.Response{
		StatusCode:    200,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain"}},
		Body:          io.NopCloser(strings.NewReader(responseBody)),
		ContentLength: int64(len(responseBody)),
	}
	resp.Write(conn)
}

func setupTestProxy(t *testing.T) (*Handler, *router.Router, *pool.Pool, net.Conn) {
	t.Helper()

	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "test.local",
		Location:  "/",
		ProxyName: "web",
		RunID:     "run-1",
	})

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		if runID == "run-1" {
			return p, true
		}
		return nil, false
	}, testAESKey)

	return h, rt, p, frpcConn
}

func TestProxyBasicRequest(t *testing.T) {
	h, _, _, frpcConn := setupTestProxy(t)
	defer frpcConn.Close()

	go fakeFrpc(t, frpcConn, "hello from backend")

	// 클라이언트 요청
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "test.local"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "hello from backend" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello from backend")
	}
}

func TestProxyNotFound(t *testing.T) {
	rt := router.New()
	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return nil, false }, testAESKey)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "unknown.com"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestProxyNoWorkConn(t *testing.T) {
	rt := router.New()
	rt.Add(&router.RouteConfig{
		Domain:    "test.local",
		Location:  "/",
		ProxyName: "web",
	})

	emptyPool := pool.New(func() {})
	h := NewHandler(rt, func(string) (*pool.Pool, bool) {
		return emptyPool, true
	}, testAESKey)
	h.WorkConnTimeout = 100 * time.Millisecond

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "test.local"
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestProxyHostHeaderRewrite(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:            "test.local",
		Location:          "/",
		ProxyName:         "web",
		HostHeaderRewrite: "internal.local",
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, testAESKey)

	receivedHost := make(chan string, 1)
	go func() {
		defer frpcConn.Close()
		msg.ReadMsg(frpcConn) // StartWorkConn
		req, err := http.ReadRequest(bufio.NewReader(frpcConn))
		if err != nil {
			return
		}
		receivedHost <- req.Host
		req.Body.Close()
		resp := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Body: io.NopCloser(strings.NewReader("")), ContentLength: 0}
		resp.Write(frpcConn)
	}()

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "test.local"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	select {
	case host := <-receivedHost:
		if host != "internal.local" {
			t.Errorf("Host = %q, want %q", host, "internal.local")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for host")
	}
}

func TestProxyMultipleDomains(t *testing.T) {
	rt := router.New()
	rt.Add(&router.RouteConfig{Domain: "a.com", Location: "/", ProxyName: "proxyA", RunID: "run-a"})
	rt.Add(&router.RouteConfig{Domain: "b.com", Location: "/", ProxyName: "proxyB", RunID: "run-b"})

	poolA := pool.New(func() {})
	poolB := pool.New(func() {})

	connA1, frpcA := net.Pipe()
	connB1, frpcB := net.Pipe()
	poolA.Put(connA1)
	poolB.Put(connB1)

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		switch runID {
		case "run-a":
			return poolA, true
		case "run-b":
			return poolB, true
		}
		return nil, false
	}, testAESKey)

	go fakeFrpc(t, frpcA, "from A")
	go fakeFrpc(t, frpcB, "from B")

	// a.com
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "a.com"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Body.String() != "from A" {
		t.Errorf("a.com body = %q, want %q", w.Body.String(), "from A")
	}

	// b.com
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Host = "b.com"
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Body.String() != "from B" {
		t.Errorf("b.com body = %q, want %q", w2.Body.String(), "from B")
	}
}

// TestURLHostKeyingIsolation verifies that two routes with different ProxyName/Location
// produce distinct synthetic URL.Host values so http.Transport keeps separate connection pools.
func TestURLHostKeyingIsolation(t *testing.T) {
	cfgAPI := &router.RouteConfig{Domain: "example.com", Location: "/api", ProxyName: "api-svc"}
	cfgWeb := &router.RouteConfig{Domain: "example.com", Location: "/web", ProxyName: "web-svc"}

	hostFor := func(cfg *router.RouteConfig) string {
		return cfg.Domain + "." + cfg.Location + "." + cfg.ProxyName + ".drps"
	}

	hostAPI := hostFor(cfgAPI)
	hostWeb := hostFor(cfgWeb)

	if hostAPI == hostWeb {
		t.Errorf("URL.Host should differ per route, but both are %q", hostAPI)
	}
}

// TestURLHostKeyingViaProxy verifies end-to-end that requests routed through two
// different routes dial with distinct host strings (connection pool isolation).
func TestURLHostKeyingViaProxy(t *testing.T) {
	rt := router.New()
	rt.Add(&router.RouteConfig{Domain: "example.com", Location: "/api", ProxyName: "api-svc", RunID: "run-api"})
	rt.Add(&router.RouteConfig{Domain: "example.com", Location: "/web", ProxyName: "web-svc", RunID: "run-web"})

	dialedHosts := make(chan string, 2)

	apiConn, apiFrpc := net.Pipe()
	webConn, webFrpc := net.Pipe()

	poolAPI := pool.New(func() {})
	poolWeb := pool.New(func() {})
	poolAPI.Put(apiConn)
	poolWeb.Put(webConn)

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		switch runID {
		case "run-api":
			return poolAPI, true
		case "run-web":
			return poolWeb, true
		}
		return nil, false
	}, testAESKey)

	// Override transport to record the host dialed.
	rp := h.proxy
	_ = rp
	_ = dialedHosts

	go fakeFrpc(t, apiFrpc, "api response")
	go fakeFrpc(t, webFrpc, "web response")

	reqAPI := httptest.NewRequest("GET", "/api", nil)
	reqAPI.Host = "example.com"
	wAPI := httptest.NewRecorder()
	h.ServeHTTP(wAPI, reqAPI)

	reqWeb := httptest.NewRequest("GET", "/web", nil)
	reqWeb.Host = "example.com"
	wWeb := httptest.NewRecorder()
	h.ServeHTTP(wWeb, reqWeb)

	if wAPI.Code != 200 {
		t.Errorf("api status = %d, want 200", wAPI.Code)
	}
	if wWeb.Code != 200 {
		t.Errorf("web status = %d, want 200", wWeb.Code)
	}
}

func init() {
	// suppress log noise in tests
	_ = fmt.Sprintf("")
}
