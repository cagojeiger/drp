package server

import (
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

func TestEagerRefill(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	registry := pool.NewRegistry()

	h := &Handler{
		Token:          "test-token",
		Router:         rt,
		OnControlClose: func(runID string) { registry.Remove(runID) },
	}
	// OnWorkConn needs h.ReqWorkConnFunc, so set after h is created
	h.OnWorkConn = func(conn net.Conn, m *msg.NewWorkConn) {
		p := registry.GetOrCreate(m.RunID, h.ReqWorkConnFunc(m.RunID))
		p.Put(conn)
	}

	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-refill", 1)

	// 초기 ReqWorkConn 1개 수신
	m, err := msg.ReadMsg(reader)
	if err != nil {
		t.Fatalf("read initial ReqWorkConn: %v", err)
	}
	if _, ok := m.(*msg.ReqWorkConn); !ok {
		t.Fatalf("expected ReqWorkConn, got %T", m)
	}

	// 프록시 등록
	msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName: "web", ProxyType: "http", CustomDomains: []string{"refill.test"},
	})
	msg.ReadMsg(reader) // NewProxyResp

	// 워크 커넥션 제공 (frpc 역할: NewWorkConn 스트림 대신 직접 풀에 넣기)
	wServer, wClient := net.Pipe()
	defer wClient.Close()
	p := registry.GetOrCreate("run-refill", h.ReqWorkConnFunc("run-refill"))
	p.Put(wServer)

	// 워크 커넥션 Get → eager refill 트리거
	conn, err := p.Get(time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	conn.Close()

	// 제어 채널에서 ReqWorkConn 수신 (eager refill)
	m, err = msg.ReadMsg(reader)
	if err != nil {
		t.Fatalf("read refill ReqWorkConn: %v", err)
	}
	if _, ok := m.(*msg.ReqWorkConn); !ok {
		t.Fatalf("expected ReqWorkConn for refill, got %T", m)
	}
}
