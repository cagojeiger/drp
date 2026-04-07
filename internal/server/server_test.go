package server

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/kangheeyong/drp/internal/msg"
)

func yamuxPair(t *testing.T) (*yamux.Session, *yamux.Session) {
	t.Helper()
	serverConn, clientConn := net.Pipe()

	serverCfg := yamux.DefaultConfig()
	serverCfg.LogOutput = io.Discard
	clientCfg := yamux.DefaultConfig()
	clientCfg.LogOutput = io.Discard

	serverSession, err := yamux.Server(serverConn, serverCfg)
	if err != nil {
		t.Fatalf("yamux.Server: %v", err)
	}
	clientSession, err := yamux.Client(clientConn, clientCfg)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	return serverSession, clientSession
}

func testAuthKey(token string, timestamp int64) string {
	raw := fmt.Sprintf("%s%d", token, timestamp)
	sum := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

func TestYamuxLoginSuccess(t *testing.T) {
	serverSession, clientSession := yamuxPair(t)
	defer serverSession.Close()
	defer clientSession.Close()

	h := &Handler{Token: "test-token"}

	// drps: 스트림 수락 → HandleConnection
	go func() {
		stream, err := serverSession.AcceptStream()
		if err != nil {
			return
		}
		h.HandleConnection(stream)
	}()

	// frpc: 스트림 열기 → Login
	stream, err := clientSession.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	writeMsg(t, stream, &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: testAuthKey("test-token", 100),
		Timestamp:    100,
		RunID:        "run-1",
		PoolCount:    1,
	})

	resp := readMsg(t, stream)
	loginResp, ok := resp.(*msg.LoginResp)
	if !ok {
		t.Fatalf("expected *LoginResp, got %T", resp)
	}
	if loginResp.Error != "" {
		t.Errorf("login should succeed, got: %s", loginResp.Error)
	}
	if loginResp.RunID == "" {
		t.Error("RunID should not be empty")
	}
}

func TestYamuxLoginFailure(t *testing.T) {
	serverSession, clientSession := yamuxPair(t)
	defer serverSession.Close()
	defer clientSession.Close()

	h := &Handler{Token: "correct-token"}

	go func() {
		stream, err := serverSession.AcceptStream()
		if err != nil {
			return
		}
		h.HandleConnection(stream)
	}()

	stream, err := clientSession.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer stream.Close()

	writeMsg(t, stream, &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: testAuthKey("wrong-token", 100),
		Timestamp:    100,
		RunID:        "run-1",
	})

	resp := readMsg(t, stream)
	loginResp := resp.(*msg.LoginResp)
	if loginResp.Error == "" {
		t.Error("login should fail with wrong token")
	}
}

func TestYamuxMultipleStreams(t *testing.T) {
	serverSession, clientSession := yamuxPair(t)
	defer serverSession.Close()
	defer clientSession.Close()

	workConnReceived := make(chan *msg.NewWorkConn, 5)
	h := &Handler{
		Token: "test-token",
		OnWorkConn: func(conn net.Conn, m *msg.NewWorkConn) {
			workConnReceived <- m
		},
	}

	// drps: 여러 스트림 수락
	go func() {
		for {
			stream, err := serverSession.AcceptStream()
			if err != nil {
				return
			}
			go h.HandleConnection(stream)
		}
	}()

	// frpc: 스트림 #1 — Login
	stream1, _ := clientSession.OpenStream()
	writeMsg(t, stream1, &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: testAuthKey("test-token", 100),
		Timestamp:    100,
		RunID:        "run-1",
		PoolCount:    3,
	})
	resp := readMsg(t, stream1)
	if resp.(*msg.LoginResp).Error != "" {
		t.Fatal("login failed")
	}

	// frpc: 스트림 #2,#3,#4 — NewWorkConn
	for i := range 3 {
		stream, err := clientSession.OpenStream()
		if err != nil {
			t.Fatalf("OpenStream %d: %v", i, err)
		}
		writeMsg(t, stream, &msg.NewWorkConn{RunID: "run-1"})
	}

	// 3개 워크 커넥션 수신 확인
	for range 3 {
		select {
		case m := <-workConnReceived:
			if m.RunID != "run-1" {
				t.Errorf("RunID = %q, want %q", m.RunID, "run-1")
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for work conn")
		}
	}
}

func TestYamuxConcurrentLogins(t *testing.T) {
	serverSession, clientSession := yamuxPair(t)
	defer serverSession.Close()
	defer clientSession.Close()

	h := &Handler{Token: "test-token"}

	go func() {
		for {
			stream, err := serverSession.AcceptStream()
			if err != nil {
				return
			}
			go h.HandleConnection(stream)
		}
	}()

	// 독립된 frpc 2개가 동시에 Login
	results := make(chan string, 2)
	for _, runID := range []string{"client-a", "client-b"} {
		go func(rid string) {
			stream, _ := clientSession.OpenStream()
			defer stream.Close()
			writeMsg(t, stream, &msg.Login{
				Version:      "0.68.0",
				PrivilegeKey: testAuthKey("test-token", 100),
				Timestamp:    100,
				RunID:        rid,
				PoolCount:    1,
			})
			resp := readMsg(t, stream)
			results <- resp.(*msg.LoginResp).RunID
		}(runID)
	}

	seen := map[string]bool{}
	for range 2 {
		select {
		case rid := <-results:
			if rid == "" {
				t.Error("RunID should not be empty")
			}
			seen[rid] = true
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
	if len(seen) != 2 {
		t.Errorf("expected 2 unique RunIDs, got %d", len(seen))
	}
}
