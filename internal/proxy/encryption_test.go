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

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

// fakeFrpc with encryption: reads StartWorkConn (plain), then decrypts AES, reads HTTP, sends response
func fakeFrpcEncrypted(t *testing.T, conn net.Conn, token, responseBody string, enc, comp bool) {
	t.Helper()
	defer conn.Close()

	// StartWorkConn (항상 평문)
	m, err := msg.ReadMsg(conn)
	if err != nil {
		t.Logf("fakeFrpcEncrypted ReadMsg: %v", err)
		return
	}
	if _, ok := m.(*msg.StartWorkConn); !ok {
		t.Logf("expected StartWorkConn, got %T", m)
		return
	}

	var r io.Reader = conn
	var w io.Writer = conn

	// AES 래핑 (frpc 측: Reader 먼저 → Writer)
	if enc {
		key := crypto.DeriveKey(token)
		r, err = crypto.NewCryptoReader(conn, key)
		if err != nil {
			t.Logf("fakeFrpcEncrypted CryptoReader: %v", err)
			return
		}
		w, err = crypto.NewCryptoWriter(conn, key)
		if err != nil {
			t.Logf("fakeFrpcEncrypted CryptoWriter: %v", err)
			return
		}
	}

	// snappy 래핑
	if comp {
		r = crypto.NewSnappyReader(r)
		w = crypto.NewSnappyWriter(w)
	}

	// HTTP 요청 읽기
	req, err := http.ReadRequest(bufio.NewReader(r))
	if err != nil {
		t.Logf("fakeFrpcEncrypted ReadRequest: %v", err)
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
	resp.Write(w)

	// snappy flush
	if comp {
		if wc, ok := w.(io.WriteCloser); ok {
			wc.Close()
		}
	}
}

func TestProxyWithEncryption(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:        "enc.test",
		Location:      "/",
		ProxyName:     "web-enc",
		RunID:         "run-enc",
		UseEncryption: true,
	})

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		return p, true
	}, testAESKey)

	go fakeFrpcEncrypted(t, frpcConn, "test-token", "encrypted response", true, false)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "enc.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "encrypted response" {
		t.Errorf("body = %q, want %q", w.Body.String(), "encrypted response")
	}
}

func TestProxyWithCompression(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:         "comp.test",
		Location:       "/",
		ProxyName:      "web-comp",
		RunID:          "run-comp",
		UseCompression: true,
	})

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		return p, true
	}, testAESKey)

	go fakeFrpcEncrypted(t, frpcConn, "test-token", "compressed response", false, true)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "comp.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "compressed response" {
		t.Errorf("body = %q, want %q", w.Body.String(), "compressed response")
	}
}

func TestProxyWithEncAndComp(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:         "both.test",
		Location:       "/",
		ProxyName:      "web-both",
		RunID:          "run-both",
		UseEncryption:  true,
		UseCompression: true,
	})

	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		return p, true
	}, testAESKey)

	go fakeFrpcEncrypted(t, frpcConn, "test-token", "enc+comp response", true, true)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "both.test"
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}

	if w.Code != 200 {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "enc+comp response" {
		t.Errorf("body = %q, want %q", w.Body.String(), "enc+comp response")
	}
}
