package e2e_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/client"
	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/server"
	"github.com/cagojeiger/drp/internal/transport"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

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

func startServerHelper(t *testing.T, nodeID string, joinPeers []string) (*server.Server, server.ServerAddrs, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := server.ServerConfig{
		NodeID:       nodeID,
		HTTPAddr:     ":0",
		HTTPSAddr:    ":0",
		ControlAddr:  ":0",
		QuicAddr:     ":0",
		MeshBindAddr: "127.0.0.1",
		MeshBindPort: 0,
		JoinPeers:    joinPeers,
	}
	s := server.New(cfg)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	select {
	case <-s.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server not ready")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
	})
	addrs := s.Addr()
	return s, addrs, cancel
}

func startClientHelper(t *testing.T, controlAddr, alias, hostname, echoAddr string) (*client.Client, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := client.New(client.Config{
		ServerAddr: controlAddr,
		Alias:      alias,
		Hostname:   hostname,
		ProxyType:  "http",
		LocalAddr:  echoAddr,
		APIKey:     "test",
		Version:    "1.0",
	}, transport.TCPDialer{})

	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()

	select {
	case <-c.Ready():
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("client not ready")
	}

	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
		}
	})
	return c, cancel
}

func httpRequest(t *testing.T, httpAddr, hostname, path string) string {
	t.Helper()
	conn, err := net.Dial("tcp", httpAddr)
	if err != nil {
		t.Fatalf("dial http: %v", err)
	}
	defer conn.Close()

	req := "GET " + path + " HTTP/1.1\r\nHost: " + hostname + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write http request: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read http response: %v", err)
	}
	return string(buf[:n])
}

func TestE2E_H1_LocalHit(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, addrs, _ := startServerHelper(t, "e2e-node-1", nil)
	startClientHelper(t, addrs.Control, "myapp", "myapp.example.com", echoAddr)

	time.Sleep(100 * time.Millisecond)

	resp := httpRequest(t, addrs.HTTP, "myapp.example.com", "/hello")
	if !strings.Contains(resp, "GET /hello") {
		t.Fatalf("expected echoed request, got: %q", resp)
	}
}

func TestE2E_H2_OneHopRelay(t *testing.T) {
	echoAddr := startEchoServer(t)

	_, addrsA, _ := startServerHelper(t, "e2e-relay-A", nil)
	_, addrsB, _ := startServerHelper(t, "e2e-relay-B", []string{addrsA.Mesh})

	time.Sleep(200 * time.Millisecond)

	startClientHelper(t, addrsA.Control, "relayapp", "relayapp.example.com", echoAddr)

	time.Sleep(300 * time.Millisecond)

	resp := httpRequest(t, addrsB.HTTP, "relayapp.example.com", "/relayed")
	if !strings.Contains(resp, "GET /relayed") {
		t.Fatalf("expected echoed relayed request, got: %q", resp)
	}
}

func TestE2E_F1_UnknownHost(t *testing.T) {
	_, addrs, _ := startServerHelper(t, "e2e-node-unknown", nil)

	resp := httpRequest(t, addrs.HTTP, "nope.example.com", "/")
	if !strings.Contains(resp, "502") {
		t.Fatalf("expected 502, got: %q", resp)
	}
}

func TestE2E_F2_ClientDisconnect(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, addrs, _ := startServerHelper(t, "e2e-node-disconnect", nil)

	_, clientCancel := startClientHelper(t, addrs.Control, "dcapp", "dcapp.example.com", echoAddr)

	time.Sleep(100 * time.Millisecond)

	resp := httpRequest(t, addrs.HTTP, "dcapp.example.com", "/before")
	if !strings.Contains(resp, "GET /before") {
		t.Fatalf("expected echoed request before disconnect, got: %q", resp)
	}

	clientCancel()
	time.Sleep(300 * time.Millisecond)

	conn, err := net.Dial("tcp", addrs.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	conn.Write([]byte("GET /after HTTP/1.1\r\nHost: dcapp.example.com\r\n\r\n"))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "502") {
		t.Fatalf("expected 502 after disconnect, got: %q", string(buf[:n]))
	}
}

func TestE2E_F3_MultipleServices(t *testing.T) {
	echo1 := startEchoServer(t)
	echo2 := startEchoServer(t)
	_, addrs, _ := startServerHelper(t, "e2e-multi", nil)

	startClientHelper(t, addrs.Control, "svc1", "svc1.example.com", echo1)
	startClientHelper(t, addrs.Control, "svc2", "svc2.example.com", echo2)

	time.Sleep(100 * time.Millisecond)

	resp1 := httpRequest(t, addrs.HTTP, "svc1.example.com", "/s1")
	if !strings.Contains(resp1, "GET /s1") {
		t.Fatalf("svc1 unexpected: %q", resp1)
	}

	resp2 := httpRequest(t, addrs.HTTP, "svc2.example.com", "/s2")
	if !strings.Contains(resp2, "GET /s2") {
		t.Fatalf("svc2 unexpected: %q", resp2)
	}
}

// registerManualClient performs the Login+NewProxy handshake directly,
// returning the control conn and buffered reader for manual work-conn handling.
func registerManualClient(t *testing.T, controlAddr, alias, hostname string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", controlAddr)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}

	r := bufio.NewReader(conn)

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

	if err := protocol.WriteEnvelope(conn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: alias, Hostname: hostname, Type: "http",
		}},
	}); err != nil {
		t.Fatalf("write new proxy: %v", err)
	}
	env, err = protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read proxy resp: %v", err)
	}
	if resp := env.GetNewProxyResp(); resp == nil || !resp.Ok {
		t.Fatalf("new proxy failed: %+v", resp)
	}

	return conn, r
}

func workConnLoop(t *testing.T, controlR *bufio.Reader, controlAddr, echoAddr string, done <-chan struct{}) {
	t.Helper()
	for {
		select {
		case <-done:
			return
		default:
		}

		env, err := protocol.ReadEnvelope(controlR)
		if err != nil {
			return
		}

		switch p := env.Payload.(type) {
		case *drppb.Envelope_ReqWorkConn:
			go func(alias string) {
				workConn, err := net.Dial("tcp", controlAddr)
				if err != nil {
					return
				}
				defer workConn.Close()

				protocol.WriteEnvelope(workConn, &drppb.Envelope{
					Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{
						ProxyAlias: alias,
					}},
				})

				workR := bufio.NewReader(workConn)
				env, err := protocol.ReadEnvelope(workR)
				if err != nil {
					return
				}
				if env.GetStartWorkConn() == nil {
					return
				}

				localConn, err := net.Dial("tcp", echoAddr)
				if err != nil {
					return
				}
				defer localConn.Close()

				go io.Copy(workConn, localConn)
				io.Copy(localConn, workR)
			}(p.ReqWorkConn.ProxyAlias)
		case *drppb.Envelope_Pong:
		}
	}
}

func TestE2E_H3_ManualClientLocalHit(t *testing.T) {
	echoAddr := startEchoServer(t)
	_, addrs, _ := startServerHelper(t, "e2e-manual", nil)

	ctrlConn, ctrlR := registerManualClient(t, addrs.Control, "manual", "manual.example.com")
	defer ctrlConn.Close()

	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go workConnLoop(t, ctrlR, addrs.Control, echoAddr, done)

	time.Sleep(100 * time.Millisecond)

	resp := httpRequest(t, addrs.HTTP, "manual.example.com", "/manual-test")
	if !strings.Contains(resp, "GET /manual-test") {
		t.Fatalf("unexpected: %q", resp)
	}
}
