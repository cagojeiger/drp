package client

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

type chanDialer struct {
	ch chan net.Conn
}

func (d *chanDialer) Dial(addr string) (net.Conn, error) {
	return <-d.ch, nil
}

func baseConfig(localAddr string) Config {
	return Config{
		ServerAddr: "drps.example.com:9000",
		Alias:      "myapp",
		Hostname:   "myapp.example.com",
		ProxyType:  "http",
		LocalAddr:  localAddr,
		APIKey:     "test-key",
		Version:    "1.2.3",
	}
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					_, _ = c.Write(buf[:n])
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func readExactly(r *bufio.Reader, n int) []byte {
	out := make([]byte, n)
	read := 0
	for read < n {
		m, err := r.Read(out[read:])
		if err != nil {
			return nil
		}
		read += m
	}
	return out
}

func TestClientLoginSuccess(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	dialer := &chanDialer{ch: make(chan net.Conn, 1)}
	dialer.ch <- clientSide

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New(baseConfig("127.0.0.1:9"), dialer)
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	r := bufio.NewReader(serverSide)
	env, _ := protocol.ReadEnvelope(r)
	if got := env.GetLogin(); got == nil || got.ApiKey != "test-key" || got.Version != "1.2.3" {
		t.Fatalf("unexpected login: %+v", got)
	}
	_ = protocol.WriteEnvelope(serverSide, &drppb.Envelope{Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: true}}})
	env, _ = protocol.ReadEnvelope(r)
	if got := env.GetNewProxy(); got == nil || got.Alias != "myapp" || got.Hostname != "myapp.example.com" || got.Type != "http" {
		t.Fatalf("unexpected new proxy: %+v", got)
	}
	_ = protocol.WriteEnvelope(serverSide, &drppb.Envelope{Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: true}}})

	select {
	case <-c.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("client never became ready")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return")
	}
}

func TestClientLoginFailure(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	dialer := &chanDialer{ch: make(chan net.Conn, 1)}
	dialer.ch <- clientSide

	errCh := make(chan error, 1)
	go func() { errCh <- New(baseConfig("127.0.0.1:9"), dialer).Run(context.Background()) }()

	r := bufio.NewReader(serverSide)
	_, _ = protocol.ReadEnvelope(r)
	_ = protocol.WriteEnvelope(serverSide, &drppb.Envelope{Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: false, Error: "bad api key"}}})

	if err := <-errCh; err == nil || err.Error() != "login failed: bad api key" {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientProxyRegFailure(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()

	dialer := &chanDialer{ch: make(chan net.Conn, 1)}
	dialer.ch <- clientSide

	errCh := make(chan error, 1)
	go func() { errCh <- New(baseConfig("127.0.0.1:9"), dialer).Run(context.Background()) }()

	r := bufio.NewReader(serverSide)
	_, _ = protocol.ReadEnvelope(r)
	_ = protocol.WriteEnvelope(serverSide, &drppb.Envelope{Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: true}}})
	_, _ = protocol.ReadEnvelope(r)
	_ = protocol.WriteEnvelope(serverSide, &drppb.Envelope{Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: false, Error: "hostname taken"}}})

	if err := <-errCh; err == nil || err.Error() != "new proxy failed: hostname taken" {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientWorkConn(t *testing.T) {
	echoAddr := startEchoServer(t)
	controlClient, controlServer := net.Pipe()
	defer controlServer.Close()
	workClient, workServer := net.Pipe()
	defer workServer.Close()

	dialer := &chanDialer{ch: make(chan net.Conn, 2)}
	dialer.ch <- controlClient
	dialer.ch <- workClient

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := New(baseConfig(echoAddr), dialer)
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	controlR := bufio.NewReader(controlServer)
	_, _ = protocol.ReadEnvelope(controlR)
	_ = protocol.WriteEnvelope(controlServer, &drppb.Envelope{Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: true}}})
	_, _ = protocol.ReadEnvelope(controlR)
	_ = protocol.WriteEnvelope(controlServer, &drppb.Envelope{Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: true}}})

	select {
	case <-c.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("client never became ready")
	}

	_ = protocol.WriteEnvelope(controlServer, &drppb.Envelope{Payload: &drppb.Envelope_ReqWorkConn{ReqWorkConn: &drppb.ReqWorkConn{ProxyAlias: "myapp"}}})

	workR := bufio.NewReader(workServer)
	env, _ := protocol.ReadEnvelope(workR)
	if got := env.GetNewWorkConn(); got == nil || got.ProxyAlias != "myapp" {
		t.Fatalf("unexpected new_work_conn: %+v", got)
	}
	_ = protocol.WriteEnvelope(workServer, &drppb.Envelope{Payload: &drppb.Envelope_StartWorkConn{StartWorkConn: &drppb.StartWorkConn{ProxyAlias: "myapp"}}})

	_, _ = workServer.Write([]byte("hello"))
	if got := string(readExactly(workR, 5)); got != "hello" {
		t.Fatalf("unexpected echoed payload: got %q want %q", got, "hello")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return")
	}
}
