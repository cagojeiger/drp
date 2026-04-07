package proxy

import (
	"bufio"
	"fmt"
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

// fakeFrpcWebSocket: StartWorkConn → Upgrade 요청 읽기 → 101 응답 → 양방향 relay
func fakeFrpcWebSocket(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()

	// StartWorkConn
	m, err := msg.ReadMsg(conn)
	if err != nil {
		t.Logf("fakeFrpcWS ReadMsg: %v", err)
		return
	}
	if _, ok := m.(*msg.StartWorkConn); !ok {
		t.Logf("expected StartWorkConn, got %T", m)
		return
	}

	// HTTP Upgrade 요청 읽기
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		t.Logf("fakeFrpcWS ReadRequest: %v", err)
		return
	}
	req.Body.Close()

	// 101 Switching Protocols 응답
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"\r\n"
	conn.Write([]byte(resp))

	// 양방향 echo: 받은 데이터를 대문자로 변환해서 돌려보냄
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		upper := strings.ToUpper(string(buf[:n]))
		if _, err := conn.Write([]byte(upper)); err != nil {
			return
		}
	}
}

func TestWebSocketRelay(t *testing.T) {
	rt := router.New()
	drpsConn, frpcConn := net.Pipe()

	p := pool.New(func() {})
	p.Put(drpsConn)

	rt.Add(&router.RouteConfig{
		Domain:    "ws.test",
		Location:  "/",
		ProxyName: "ws-proxy",
	})

	h := NewHandler(rt, func(name string) (*pool.Pool, bool) {
		return p, true
	}, "test-token")

	// 실제 HTTP 서버로 프록시 구동 (Hijack 지원 필요)
	server := httptest.NewServer(h)
	defer server.Close()

	go fakeFrpcWebSocket(t, frpcConn)

	// WebSocket 핸드셰이크 (raw TCP)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Upgrade 요청
	upgradeReq := "GET / HTTP/1.1\r\n" +
		"Host: ws.test\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"\r\n"
	conn.Write([]byte(upgradeReq))

	// 101 응답 확인
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	// 양방향 데이터 교환
	conn.Write([]byte("hello websocket"))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read echo: %v", err)
	}
	got := string(buf[:n])
	if got != "HELLO WEBSOCKET" {
		t.Errorf("echo = %q, want %q", got, "HELLO WEBSOCKET")
	}

	// 두 번째 메시지
	conn.Write([]byte("test 2"))
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read echo 2: %v", err)
	}
	got = string(buf[:n])
	if got != "TEST 2" {
		t.Errorf("echo2 = %q, want %q", got, "TEST 2")
	}
}

func init() {
	_ = fmt.Sprintf("")
}
