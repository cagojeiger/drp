package server_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/server"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

// ---------- helpers ----------

func testConfig(nodeID string) server.ServerConfig {
	return server.ServerConfig{
		NodeID:       nodeID,
		HTTPAddr:     ":0",
		HTTPSAddr:    ":0",
		ControlAddr:  ":0",
		QuicAddr:     ":0",
		MeshBindAddr: "127.0.0.1",
		MeshBindPort: 0,
	}
}

func startServer(t *testing.T, cfg server.ServerConfig) (*server.Server, server.ServerAddrs, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := server.New(cfg)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	select {
	case <-s.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not become ready")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Log("server did not shut down in time")
		}
	})

	addrs := s.Addr()
	return s, addrs, cancel
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

// registerClient performs the Login+NewProxy handshake on a freshly dialed
// control connection and returns the control conn and its buffered reader.
func registerClient(t *testing.T, controlAddr, alias, hostname string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", controlAddr)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}

	r := bufio.NewReader(conn)

	// Login
	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "test", Version: "1.0"}},
	}); err != nil {
		t.Fatalf("write login: %v", err)
	}
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read login resp: %v", err)
	}
	if resp := env.GetLoginResp(); resp == nil || !resp.Ok {
		t.Fatalf("login failed: %+v", resp)
	}

	// NewProxy
	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias:    alias,
			Hostname: hostname,
			Type:     "http",
		}},
	}); err != nil {
		t.Fatalf("write new proxy: %v", err)
	}
	env, err = protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read new proxy resp: %v", err)
	}
	if resp := env.GetNewProxyResp(); resp == nil || !resp.Ok {
		t.Fatalf("new proxy failed: %+v", resp)
	}

	return conn, r
}

// workConnHandler runs in a goroutine. It reads ReqWorkConn from the control
// connection, dials back as a work connection, and pipes to the echo server.
func workConnHandler(t *testing.T, controlR *bufio.Reader, controlAddr, echoAddr string) {
	t.Helper()
	env, err := protocol.ReadEnvelope(controlR)
	if err != nil {
		t.Errorf("read ReqWorkConn: %v", err)
		return
	}
	reqWC := env.GetReqWorkConn()
	if reqWC == nil {
		t.Errorf("expected ReqWorkConn, got %T", env.Payload)
		return
	}

	// Dial back for work connection.
	workConn, err := net.Dial("tcp", controlAddr)
	if err != nil {
		t.Errorf("dial work conn: %v", err)
		return
	}
	defer workConn.Close()

	if err := protocol.WriteEnvelope(workConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{
			ProxyAlias: reqWC.ProxyAlias,
		}},
	}); err != nil {
		t.Errorf("write NewWorkConn: %v", err)
		return
	}

	workR := bufio.NewReader(workConn)
	env, err = protocol.ReadEnvelope(workR)
	if err != nil {
		t.Errorf("read StartWorkConn: %v", err)
		return
	}
	if env.GetStartWorkConn() == nil {
		t.Errorf("expected StartWorkConn, got %T", env.Payload)
		return
	}

	// Pipe to echo server.
	localConn, err := net.Dial("tcp", echoAddr)
	if err != nil {
		t.Errorf("dial echo: %v", err)
		return
	}
	defer localConn.Close()

	// Use workR (bufio.Reader) as source to avoid losing bytes already buffered.
	go io.Copy(workConn, localConn)
	io.Copy(localConn, workR)
}

// ---------- tests ----------

func TestServerLocalHit(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, addrs, _ := startServer(t, testConfig("node-1"))

	// Register client.
	ctrlConn, ctrlR := registerClient(t, addrs.Control, "myapp", "myapp.example.com")
	defer ctrlConn.Close()

	// Start work conn handler goroutine.
	go workConnHandler(t, ctrlR, addrs.Control, echoAddr)

	// Give the registration a moment to propagate via mesh.
	time.Sleep(50 * time.Millisecond)

	// User HTTP request.
	userConn, err := net.Dial("tcp", addrs.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer userConn.Close()

	req := "GET / HTTP/1.1\r\nHost: myapp.example.com\r\n\r\n"
	if _, err := userConn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	// The echo server reflects whatever it receives. We should get the request back.
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
}

func TestServerUnknownHost(t *testing.T) {
	_, addrs, _ := startServer(t, testConfig("node-2"))

	conn, err := net.Dial("tcp", addrs.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: unknown.example.com\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "502") {
		t.Fatalf("expected 502, got: %q", got)
	}
}

func TestServerClientDisconnect(t *testing.T) {
	_, addrs, _ := startServer(t, testConfig("node-3"))

	ctrlConn, _ := registerClient(t, addrs.Control, "myapp", "myapp.example.com")

	// Give registration time to propagate.
	time.Sleep(50 * time.Millisecond)

	// Disconnect the client.
	ctrlConn.Close()

	// Wait for server to notice the disconnect and clean up.
	time.Sleep(200 * time.Millisecond)

	// Now request should fail with 502.
	conn, err := net.Dial("tcp", addrs.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: myapp.example.com\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "502") {
		t.Fatalf("expected 502 after client disconnect, got: %q", got)
	}
}

func TestServerMultipleProxies(t *testing.T) {
	echo1 := startEchoServer(t)
	echo2 := startEchoServer(t)
	_, addrs, _ := startServer(t, testConfig("node-4"))

	// Register two clients with different hostnames.
	ctrl1, ctrlR1 := registerClient(t, addrs.Control, "app1", "app1.example.com")
	defer ctrl1.Close()
	ctrl2, ctrlR2 := registerClient(t, addrs.Control, "app2", "app2.example.com")
	defer ctrl2.Close()

	// Work conn handlers.
	go workConnHandler(t, ctrlR1, addrs.Control, echo1)
	go workConnHandler(t, ctrlR2, addrs.Control, echo2)

	time.Sleep(50 * time.Millisecond)

	// Request app1.
	{
		conn, err := net.Dial("tcp", addrs.HTTP)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if _, err := conn.Write([]byte("GET /1 HTTP/1.1\r\nHost: app1.example.com\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read app1: %v", err)
		}
		if !strings.Contains(string(buf[:n]), "GET /1") {
			t.Fatalf("app1 unexpected response: %q", string(buf[:n]))
		}
	}

	// Request app2.
	{
		conn, err := net.Dial("tcp", addrs.HTTP)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if _, err := conn.Write([]byte("GET /2 HTTP/1.1\r\nHost: app2.example.com\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read app2: %v", err)
		}
		if !strings.Contains(string(buf[:n]), "GET /2") {
			t.Fatalf("app2 unexpected response: %q", string(buf[:n]))
		}
	}
}
