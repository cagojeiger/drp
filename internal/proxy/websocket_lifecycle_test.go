package proxy

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

func fakeFrpcWebSocketWithDone(t *testing.T, conn net.Conn, done chan<- struct{}) {
	t.Helper()
	defer close(done)
	defer conn.Close()

	if _, err := msg.ReadMsg(conn); err != nil {
		return
	}
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	req.Body.Close()
	_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))

	buf := make([]byte, 1024)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

func TestWebSocketBothGoroutinesComplete(t *testing.T) {
	rt := router.New()
	_ = rt.Add(&router.RouteConfig{Domain: "ws.done", Location: "/", ProxyName: "ws"})
	drpsConn, frpcConn := net.Pipe()
	p := pool.New(func() {})
	p.Put(drpsConn)
	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, testAESKey)

	server := httptest.NewServer(h)
	defer server.Close()

	done := make(chan struct{})
	go fakeFrpcWebSocketWithDone(t, frpcConn, done)

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: ws.done\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
	_, _ = http.ReadResponse(bufio.NewReader(conn), nil)
	_ = conn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay goroutine did not complete after client close")
	}
}

func TestWebSocketServerClose(t *testing.T) {
	rt := router.New()
	_ = rt.Add(&router.RouteConfig{Domain: "ws.close", Location: "/", ProxyName: "ws"})
	drpsConn, frpcConn := net.Pipe()
	p := pool.New(func() {})
	p.Put(drpsConn)
	h := NewHandler(rt, func(string) (*pool.Pool, bool) { return p, true }, testAESKey)

	server := httptest.NewServer(h)
	defer server.Close()

	go func() {
		defer frpcConn.Close()
		_, _ = msg.ReadMsg(frpcConn)
		req, err := http.ReadRequest(bufio.NewReader(frpcConn))
		if err != nil {
			return
		}
		req.Body.Close()
		_, _ = frpcConn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
		// server side closes immediately
	}()

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: ws.close\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"))
	_, _ = http.ReadResponse(bufio.NewReader(conn), nil)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected read error after server-side close")
	}
}
