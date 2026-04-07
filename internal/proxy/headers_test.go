package proxy

import (
	"bufio"
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

func TestCustomRequestHeaders(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()
	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "headers.test",
		Location:  "/",
		ProxyName: "web",
		Headers:   map[string]string{"X-Custom": "hello", "X-Env": "prod"},
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, "test-token")

	receivedHeaders := make(chan http.Header, 1)
	go func() {
		defer frpcConn.Close()
		msg.ReadMsg(frpcConn) // StartWorkConn
		req, _ := http.ReadRequest(bufio.NewReader(frpcConn))
		receivedHeaders <- req.Header
		req.Body.Close()
		resp := &http.Response{StatusCode: 200, ProtoMajor: 1, ProtoMinor: 1, Body: io.NopCloser(strings.NewReader("")), ContentLength: 0}
		resp.Write(frpcConn)
	}()

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "headers.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	select {
	case headers := <-receivedHeaders:
		if headers.Get("X-Custom") != "hello" {
			t.Errorf("X-Custom = %q, want %q", headers.Get("X-Custom"), "hello")
		}
		if headers.Get("X-Env") != "prod" {
			t.Errorf("X-Env = %q, want %q", headers.Get("X-Env"), "prod")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestResponseHeaders(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()
	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:          "resp.test",
		Location:        "/",
		ProxyName:       "web",
		ResponseHeaders: map[string]string{"X-Resp-Custom": "world"},
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, "test-token")

	go fakeFrpc(t, frpcConn, "ok")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "resp.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Header().Get("X-Resp-Custom") != "world" {
		t.Errorf("X-Resp-Custom = %q, want %q", w.Header().Get("X-Resp-Custom"), "world")
	}
}

func TestBasicAuthSuccess(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()
	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "auth.test",
		Location:  "/",
		ProxyName: "web",
		HTTPUser:  "admin",
		HTTPPwd:   "secret",
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, "test-token")

	go fakeFrpc(t, frpcConn, "authenticated")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "auth.test"
	req.SetBasicAuth("admin", "secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestBasicAuthFail(t *testing.T) {
	rt := router.New()

	rt.Add(&router.RouteConfig{
		Domain:    "auth.test",
		Location:  "/",
		ProxyName: "web",
		HTTPUser:  "admin",
		HTTPPwd:   "secret",
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return nil, false }, "test-token")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "auth.test"
	req.SetBasicAuth("admin", "wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestBasicAuthMissing(t *testing.T) {
	rt := router.New()

	rt.Add(&router.RouteConfig{
		Domain:    "auth.test",
		Location:  "/",
		ProxyName: "web",
		HTTPUser:  "admin",
		HTTPPwd:   "secret",
	})

	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return nil, false }, "test-token")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "auth.test"
	// No auth header
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") == "" {
		t.Error("should include WWW-Authenticate header")
	}
}
