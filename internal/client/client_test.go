package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
)

type pipeDialer struct {
	conns chan net.Conn
}

func (d *pipeDialer) Dial(string) (net.Conn, error) {
	return <-d.conns, nil
}

func newPipeDialer(conns ...net.Conn) transport.Dialer {
	ch := make(chan net.Conn, len(conns))
	for _, c := range conns {
		ch <- c
	}
	return &pipeDialer{conns: ch}
}

func TestRun_LoginSuccess(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	c := New(Config{
		ServerAddr: "fake:9000",
		Alias:      "myapp",
		Hostname:   "myapp.example.com",
		LocalAddr:  "fake:5000",
	}, newPipeDialer(clientConn))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))

		msgType, _, err := protocol.ReadMsg(serverConn)
		if err != nil {
			t.Errorf("read login: %v", err)
			return
		}
		if msgType != protocol.MsgLogin {
			t.Errorf("expected Login, got 0x%02x", msgType)
			return
		}

		_ = protocol.WriteMsg(serverConn, protocol.MsgLoginResp, &protocol.LoginRespBody{OK: true, Message: "ok"})

		msgType, _, err = protocol.ReadMsg(serverConn)
		if err != nil {
			t.Errorf("read new proxy: %v", err)
			return
		}
		if msgType != protocol.MsgNewProxy {
			t.Errorf("expected NewProxy, got 0x%02x", msgType)
			return
		}

		_ = protocol.WriteMsg(serverConn, protocol.MsgNewProxyResp, &protocol.NewProxyRespBody{OK: true, Message: "ok"})

		<-ctx.Done()
	}()

	go c.Run(ctx)

	select {
	case <-c.Ready():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not become ready")
	}
}

func TestRun_LoginRejected(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	c := New(Config{
		ServerAddr: "fake:9000",
		Alias:      "myapp",
		Hostname:   "myapp.example.com",
		LocalAddr:  "fake:5000",
	}, newPipeDialer(clientConn))

	go func() {
		_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
		_, _, _ = protocol.ReadMsg(serverConn)
		_ = protocol.WriteMsg(serverConn, protocol.MsgLoginResp, &protocol.LoginRespBody{OK: false, Message: "denied"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.Run(ctx)
	if err == nil {
		t.Fatal("expected login error, got nil")
	}
	if err.Error() != "login failed: denied" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRun_ProxyRejected(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()

	c := New(Config{
		ServerAddr: "fake:9000",
		Alias:      "myapp",
		Hostname:   "myapp.example.com",
		LocalAddr:  "fake:5000",
	}, newPipeDialer(clientConn))

	go func() {
		_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))

		_, _, _ = protocol.ReadMsg(serverConn)
		_ = protocol.WriteMsg(serverConn, protocol.MsgLoginResp, &protocol.LoginRespBody{OK: true, Message: "ok"})

		_, _, _ = protocol.ReadMsg(serverConn)
		_ = protocol.WriteMsg(serverConn, protocol.MsgNewProxyResp, &protocol.NewProxyRespBody{OK: false, Message: "duplicate"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.Run(ctx)
	if err == nil {
		t.Fatal("expected proxy error, got nil")
	}
	if err.Error() != "proxy registration failed: duplicate" {
		t.Fatalf("unexpected error: %v", err)
	}
}
