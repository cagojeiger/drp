package server

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/mesh"
	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/transport"
)

func newTestServer() *Server {
	s := &Server{
		cfg:      Config{NodeID: "test"},
		localMap: make(map[string]*serviceEntry),
		ready:    make(chan struct{}),
	}
	s.mesh = mesh.New("test", 0, s.hasHostname, s.getWorkConn, transport.TCP{})
	return s
}

func TestHandleHTTP_MissingHost(t *testing.T) {
	srv := newTestServer()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan string, 1)
	go func() {
		_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, _ := clientConn.Read(buf)
		done <- string(buf[:n])
	}()

	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		_, _ = clientConn.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	}()

	srv.handleHTTP(serverConn)

	select {
	case resp := <-done:
		if !strings.Contains(resp, "400") {
			t.Fatalf("expected 400 response, got: %s", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestHandleHTTP_HeaderTooLarge(t *testing.T) {
	srv := newTestServer()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan string, 1)
	go func() {
		_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, _ := clientConn.Read(buf)
		done <- string(buf[:n])
	}()

	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		header := "GET / HTTP/1.1\r\nHost: example.com\r\nX-Big: " + strings.Repeat("A", 70000) + "\r\n\r\n"
		_, _ = clientConn.Write([]byte(header))
	}()

	srv.handleHTTP(serverConn)

	select {
	case resp := <-done:
		if !strings.Contains(resp, "431") {
			t.Fatalf("expected 431 response, got: %s", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestHandleHTTP_UnknownHost_NoPeers(t *testing.T) {
	srv := newTestServer()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan string, 1)
	go func() {
		_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		n, _ := clientConn.Read(buf)
		done <- string(buf[:n])
	}()

	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	go func() {
		_, _ = clientConn.Write([]byte("GET / HTTP/1.1\r\nHost: unknown.example.com\r\n\r\n"))
	}()

	srv.handleHTTP(serverConn)

	select {
	case resp := <-done:
		if !strings.Contains(resp, "502") {
			t.Fatalf("expected 502 response, got: %s", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}

func TestAcceptWorkConn_Match(t *testing.T) {
	srv := newTestServer()

	entry := &serviceEntry{
		alias:     "myapp",
		ctrlConn:  nil,
		workQueue: make(chan net.Conn, 64),
	}
	srv.localMap["myapp.example.com"] = entry

	workConn, _ := net.Pipe()
	defer workConn.Close()

	srv.acceptWorkConn(workConn, &protocol.NewWorkConnBody{Alias: "myapp"})

	select {
	case got := <-entry.workQueue:
		if got != workConn {
			t.Fatal("work conn mismatch")
		}
	default:
		t.Fatal("work conn was not queued")
	}
}

func TestAcceptWorkConn_NoMatch(t *testing.T) {
	srv := newTestServer()

	peerA, peerB := net.Pipe()
	defer peerB.Close()

	srv.acceptWorkConn(peerA, &protocol.NewWorkConnBody{Alias: "nonexistent"})

	_ = peerA.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := peerA.Read(buf)
	if err == nil {
		t.Fatal("expected conn to be closed, but read succeeded")
	}
}

func TestHandleHTTP_LocalHit(t *testing.T) {
	srv := newTestServer()

	ctrlServer, ctrlClient := net.Pipe()
	defer ctrlServer.Close()
	defer ctrlClient.Close()

	entry := &serviceEntry{
		alias:     "myapp",
		ctrlConn:  ctrlServer,
		workQueue: make(chan net.Conn, 64),
	}
	srv.localMap["myapp.example.com"] = entry

	go func() {
		_ = ctrlClient.SetReadDeadline(time.Now().Add(5 * time.Second))
		msgType, _, err := protocol.ReadMsg(ctrlClient)
		if err != nil || msgType != protocol.MsgReqWorkConn {
			return
		}

		workServer, workClient := net.Pipe()
		entry.workQueue <- workServer

		go func() {
			_ = workClient.SetDeadline(time.Now().Add(5 * time.Second))
			_, _, _ = protocol.ReadMsg(workClient)

			buf := make([]byte, 4096)
			n, _ := workClient.Read(buf)
			if n > 0 {
				_, _ = workClient.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\nhello"))
			}
			_ = workClient.Close()
		}()
	}()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan string, 1)
	go func() {
		_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = clientConn.Write([]byte("GET / HTTP/1.1\r\nHost: myapp.example.com\r\n\r\n"))
		buf := make([]byte, 4096)
		n, _ := clientConn.Read(buf)
		done <- string(buf[:n])
	}()

	_ = serverConn.SetDeadline(time.Now().Add(5 * time.Second))
	srv.handleHTTP(serverConn)

	select {
	case resp := <-done:
		if !strings.Contains(resp, "200 OK") {
			t.Fatalf("expected 200 OK, got: %s", resp)
		}
		if !strings.Contains(resp, "hello") {
			t.Fatalf("expected body 'hello', got: %s", resp)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for response")
	}
}
