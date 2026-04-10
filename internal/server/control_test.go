package server

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
)

// frpc 역할을 하는 헬퍼: Login → AES 래핑 → 암호화된 채널 반환
func frpcLogin(t *testing.T, conn net.Conn, token string, runID string, poolCount int) (reader io.Reader, writer io.Writer) {
	t.Helper()

	timestamp := int64(100)
	raw := fmt.Sprintf("%s%d", token, timestamp)
	sum := md5.Sum([]byte(raw))
	privKey := fmt.Sprintf("%x", sum)

	// Login (평문)
	if err := msg.WriteMsg(conn, &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: privKey,
		Timestamp:    timestamp,
		RunID:        runID,
		PoolCount:    poolCount,
	}); err != nil {
		t.Fatalf("write Login: %v", err)
	}

	// LoginResp (평문)
	resp, err := msg.ReadMsg(conn)
	if err != nil {
		t.Fatalf("read LoginResp: %v", err)
	}
	loginResp := resp.(*msg.LoginResp)
	if loginResp.Error != "" {
		t.Fatalf("login failed: %s", loginResp.Error)
	}

	// AES 래핑 (frpc도 동일한 키로 래핑)
	key := crypto.DeriveKey(token)
	reader, err = crypto.NewCryptoReader(conn, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	writer, err = crypto.NewCryptoWriter(conn, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}
	return reader, writer
}

func TestControlReqWorkConn(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}
	go h.HandleConnection(serverConn)

	// frpc: Login + AES 래핑
	reader, _ := frpcLogin(t, clientConn, "test-token", "run-1", 3)

	// Login 후 drps가 요청한 총 ReqWorkConn 수가 PoolCount(3)인지 검증.
	// sendLoop 배칭으로 메시지 개수는 1~N개가 될 수 있으므로 Count를 합산한다.
	total := 0
	for total < 3 {
		m, err := msg.ReadMsg(reader)
		if err != nil {
			t.Fatalf("read ReqWorkConn(total=%d): %v", total, err)
		}
		r, ok := m.(*msg.ReqWorkConn)
		if !ok {
			t.Fatalf("expected *ReqWorkConn, got %T", m)
		}
		c := r.Count
		if c == 0 {
			c = 1
		}
		total += c
	}
	if total != 3 {
		t.Fatalf("total ReqWorkConn count = %d, want 3", total)
	}
}

func TestControlPingPong(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// frpc → Ping (암호화)
	if err := msg.WriteMsg(writer, &msg.Ping{}); err != nil {
		t.Fatalf("write Ping: %v", err)
	}

	// drps → Pong (암호화)
	m, err := msg.ReadMsg(reader)
	if err != nil {
		t.Fatalf("read Pong: %v", err)
	}
	if _, ok := m.(*msg.Pong); !ok {
		t.Fatalf("expected *Pong, got %T", m)
	}
}

func TestControlNewProxy(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// frpc → NewProxy (암호화)
	if err := msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"app.example.com"},
	}); err != nil {
		t.Fatalf("write NewProxy: %v", err)
	}

	// drps → NewProxyResp (암호화)
	m, err := msg.ReadMsg(reader)
	if err != nil {
		t.Fatalf("read NewProxyResp: %v", err)
	}
	resp, ok := m.(*msg.NewProxyResp)
	if !ok {
		t.Fatalf("expected *NewProxyResp, got %T", m)
	}
	if resp.ProxyName != "web" {
		t.Errorf("ProxyName = %q, want %q", resp.ProxyName, "web")
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

func TestControlNewProxyRejectNonHTTP(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)

	// frpc → type=tcp (거부되어야 함)
	if err := msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName: "ssh",
		ProxyType: "tcp",
	}); err != nil {
		t.Fatalf("write NewProxy: %v", err)
	}

	m, err := msg.ReadMsg(reader)
	if err != nil {
		t.Fatalf("read NewProxyResp: %v", err)
	}
	resp := m.(*msg.NewProxyResp)
	if resp.Error == "" {
		t.Error("should reject non-http proxy type")
	}
}

func TestControlYamuxFullFlow(t *testing.T) {
	serverSession, clientSession := yamuxPair(t)
	defer serverSession.Close()
	defer clientSession.Close()

	workConns := make(chan net.Conn, 5)
	h := &Handler{
		Token: "test-token",
		OnWorkConn: func(conn net.Conn, m *msg.NewWorkConn) {
			workConns <- conn
		},
	}

	// drps: 모든 스트림 수락
	go func() {
		for {
			stream, err := serverSession.AcceptStream()
			if err != nil {
				return
			}
			go h.HandleConnection(stream)
		}
	}()

	// frpc: 제어 스트림 열기 → Login
	ctrlStream, _ := clientSession.OpenStream()
	reader, writer := frpcLogin(t, ctrlStream, "test-token", "run-1", 2)

	// ReqWorkConn 2개 수신
	for range 2 {
		m, err := msg.ReadMsg(reader)
		if err != nil {
			t.Fatalf("read ReqWorkConn: %v", err)
		}
		if _, ok := m.(*msg.ReqWorkConn); !ok {
			t.Fatalf("expected *ReqWorkConn, got %T", m)
		}
	}

	// frpc: 워크 커넥션 스트림 2개 열기
	for range 2 {
		wStream, _ := clientSession.OpenStream()
		msg.WriteMsg(wStream, &msg.NewWorkConn{RunID: "run-1"})
	}

	// drps에서 워크 커넥션 2개 수신 확인
	for range 2 {
		select {
		case <-workConns:
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for work conn")
		}
	}

	// Ping → Pong (암호화)
	msg.WriteMsg(writer, &msg.Ping{})
	m, _ := msg.ReadMsg(reader)
	if _, ok := m.(*msg.Pong); !ok {
		t.Fatalf("expected *Pong, got %T", m)
	}
}
