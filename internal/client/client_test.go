package client_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/client"
	"github.com/cagojeiger/drp/internal/server"
	"github.com/cagojeiger/drp/internal/transport"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

func baseConfig(controlAddr, alias, hostname, localAddr string) client.Config {
	return client.Config{
		ServerAddr: controlAddr,
		Alias:      alias,
		Hostname:   hostname,
		ProxyType:  "http",
		LocalAddr:  localAddr,
		APIKey:     "test-key",
		Version:    "1.2.3",
	}
}

func normalizeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "::" {
		return net.JoinHostPort("::1", port)
	}
	if host == "" || host == "0.0.0.0" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

func startTestServer(t *testing.T, opts ...func(*server.ServerConfig)) (addrs server.ServerAddrs, stop func()) {
	t.Helper()

	cfg := server.ServerConfig{
		NodeID:       "test-node",
		HTTPAddr:     ":0",
		HTTPSAddr:    ":0",
		ControlAddr:  ":0",
		QuicAddr:     ":0",
		MeshBindAddr: "127.0.0.1",
		MeshBindPort: 0,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := server.New(cfg)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	select {
	case <-s.Ready():
	case err := <-errCh:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not become ready")
	}

	addrs = s.Addr()
	addrs.HTTP = normalizeAddr(addrs.HTTP)
	addrs.HTTPS = normalizeAddr(addrs.HTTPS)
	addrs.Control = normalizeAddr(addrs.Control)

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("server run failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not shut down in time")
			}
		})
	}
	t.Cleanup(stop)

	return addrs, stop
}

func startTestClient(t *testing.T, controlAddr, alias, hostname, localAddr string) (c *client.Client, stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	c = client.New(baseConfig(controlAddr, alias, hostname, localAddr), transport.TCPDialer{})
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	select {
	case <-c.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("client exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("client never became ready")
	}

	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("client run failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client did not shut down in time")
			}
		})
	}
	t.Cleanup(stop)

	return c, stop
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

func TestClientLoginSuccess(t *testing.T) {
	addrs, _ := startTestServer(t)
	_, stopClient := startTestClient(t, addrs.Control, "myapp", "myapp.example.com", "127.0.0.1:9")
	stopClient()
}

func TestClientLoginFailure(t *testing.T) {
	addrs, _ := startTestServer(t, func(cfg *server.ServerConfig) {
		cfg.Authenticate = func(login *drppb.Login) (bool, string) {
			return false, "bad api key"
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.New(baseConfig(addrs.Control, "myapp", "myapp.example.com", "127.0.0.1:9"), transport.TCPDialer{}).Run(ctx)
	if !errors.Is(err, client.ErrLoginFailed) {
		t.Fatalf("expected ErrLoginFailed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad api key") {
		t.Fatalf("expected 'bad api key' in error, got: %v", err)
	}
}

func TestClientProxyRegFailure(t *testing.T) {
	addrs, _ := startTestServer(t, func(cfg *server.ServerConfig) {
		cfg.AuthorizeProxy = func(proxy *drppb.NewProxy) (bool, string) {
			return false, "hostname taken"
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.New(baseConfig(addrs.Control, "myapp", "myapp.example.com", "127.0.0.1:9"), transport.TCPDialer{}).Run(ctx)
	if !errors.Is(err, client.ErrNewProxyFailed) {
		t.Fatalf("expected ErrNewProxyFailed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "hostname taken") {
		t.Fatalf("expected 'hostname taken' in error, got: %v", err)
	}
}

func TestClientWorkConn(t *testing.T) {
	echoAddr := startEchoServer(t)
	addrs, _ := startTestServer(t)
	_, stopClient := startTestClient(t, addrs.Control, "myapp", "myapp.example.com", echoAddr)

	time.Sleep(100 * time.Millisecond)

	userConn, err := net.Dial("tcp", addrs.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer userConn.Close()

	if _, err := userConn.Write([]byte("GET / HTTP/1.1\r\nHost: myapp.example.com\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	userConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := userConn.Read(buf)
	if err != nil {
		t.Fatalf("read user response: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "GET / HTTP/1.1") {
		t.Fatalf("unexpected response: %q", got)
	}

	stopClient()
}
