package server

import (
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

func TestHeartbeatTimeout(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	rt := router.New()
	h := &Handler{
		Token:            "test-token",
		Router:           rt,
		HeartbeatTimeout: 300 * time.Millisecond,
		OnControlClose:   func(string) {},
	}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	// Login
	frpcLogin(t, clientConn, "test-token", "run-hb", 0)

	// Ping 안 보냄 → 타임아웃으로 종료되어야 함
	select {
	case <-done:
		// 성공 — 타임아웃으로 종료됨
	case <-time.After(2 * time.Second):
		t.Fatal("controlLoop should exit on heartbeat timeout")
	}
}

func TestHeartbeatKeptAlive(t *testing.T) {
	serverConn, clientConn := net.Pipe()

	rt := router.New()
	h := &Handler{
		Token:            "test-token",
		Router:           rt,
		HeartbeatTimeout: 300 * time.Millisecond,
		OnControlClose:   func(string) {},
	}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-hb2", 0)

	// Ping을 계속 보내면 살아있어야 함
	for range 3 {
		time.Sleep(100 * time.Millisecond)
		msg.WriteMsg(writer, &msg.Ping{})
		m, err := msg.ReadMsg(reader)
		if err != nil {
			t.Fatalf("read Pong: %v", err)
		}
		if _, ok := m.(*msg.Pong); !ok {
			t.Fatalf("expected Pong, got %T", m)
		}
	}

	// 여전히 살아있음
	select {
	case <-done:
		t.Fatal("controlLoop should still be alive")
	default:
		// 좋음
	}

	// 정리
	clientConn.Close()
	<-done
}
