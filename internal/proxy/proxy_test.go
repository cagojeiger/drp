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

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

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
	})

	h := NewHandler(rt, func(proxyName string) (*pool.Pool, bool) {
		if proxyName == "web" {
			return p, true
		}
		return nil, false
	}, "test-token")

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
	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return nil, false }, "test-token")

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
	}, "test-token")
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

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, "test-token")

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
	rt.Add(&router.RouteConfig{Domain: "a.com", Location: "/", ProxyName: "proxyA"})
	rt.Add(&router.RouteConfig{Domain: "b.com", Location: "/", ProxyName: "proxyB"})

	poolA := pool.New(func() {})
	poolB := pool.New(func() {})

	connA1, frpcA := net.Pipe()
	connB1, frpcB := net.Pipe()
	poolA.Put(connA1)
	poolB.Put(connB1)

	h := NewHandler(rt, func(name string) (*pool.Pool, bool) {
		switch name {
		case "proxyA":
			return poolA, true
		case "proxyB":
			return poolB, true
		}
		return nil, false
	}, "test-token")

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

func init() {
	// suppress log noise in tests
	_ = fmt.Sprintf("")
}
