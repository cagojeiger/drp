package relay

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func setupRelayPair(t *testing.T) (server, client *RelayManager, serverAddr string) {
	t.Helper()

	cert, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}

	server = NewRelayManager(cert)
	err = server.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	serverAddr = server.Addr().String()

	clientCert, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	client = NewRelayManager(clientCert)
	t.Cleanup(func() { _ = client.Close() })

	return server, client, serverAddr
}

func TestQuicStreamRoundTrip(t *testing.T) {
	server, client, serverAddr := setupRelayPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.DialStream(ctx, serverAddr)
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("client write: %v", err)
	}

	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatalf("accept stream: %v", err)
	}
	defer func() { _ = accepted.Close() }()

	buf := make([]byte, len("hello"))
	if _, err := io.ReadFull(accepted, buf); err != nil {
		t.Fatalf("server read: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("unexpected payload: got %q want %q", string(buf), "hello")
	}

	if _, err := accepted.Write([]byte("world")); err != nil {
		t.Fatalf("server write: %v", err)
	}
	buf = make([]byte, len("world"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf) != "world" {
		t.Fatalf("unexpected payload: got %q want %q", string(buf), "world")
	}
}

func TestQuicMultipleStreams(t *testing.T) {
	server, client, serverAddr := setupRelayPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const streamCount = 10
	errCh := make(chan error, streamCount*2)
	seen := make(map[string]bool, streamCount)
	var mu sync.Mutex

	var serverWG sync.WaitGroup
	serverWG.Add(1)
	go func() {
		defer serverWG.Done()
		for i := 0; i < streamCount; i++ {
			accepted, err := server.Accept(ctx)
			if err != nil {
				errCh <- err
				return
			}

			buf := make([]byte, len("msg-0"))
			if _, err := io.ReadFull(accepted, buf); err != nil {
				_ = accepted.Close()
				errCh <- err
				return
			}
			_ = accepted.Close()

			msg := string(buf)
			mu.Lock()
			if seen[msg] {
				mu.Unlock()
				errCh <- io.ErrUnexpectedEOF
				return
			}
			seen[msg] = true
			mu.Unlock()
		}
	}()

	var clientWG sync.WaitGroup
	for i := 0; i < streamCount; i++ {
		i := i
		clientWG.Add(1)
		go func() {
			defer clientWG.Done()

			conn, err := client.DialStream(ctx, serverAddr)
			if err != nil {
				errCh <- err
				return
			}
			defer func() { _ = conn.Close() }()

			msg := []byte("msg-" + string(rune('0'+i)))
			if _, err := conn.Write(msg); err != nil {
				errCh <- err
				return
			}
		}()
	}

	clientWG.Wait()
	serverWG.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("multiple streams failed: %v", err)
		}
	}

	if len(seen) != streamCount {
		t.Fatalf("unexpected message count: got %d want %d", len(seen), streamCount)
	}
}

func TestQuicStreamToNetConnInterface(t *testing.T) {
	_, client, serverAddr := setupRelayPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := client.DialStream(ctx, serverAddr)
	if err != nil {
		t.Fatalf("dial stream: %v", err)
	}
	defer func() { _ = conn.Close() }()

	iface := net.Conn(conn)
	if iface == nil {
		t.Fatal("net.Conn interface is nil")
	}

	local := conn.LocalAddr()
	remote := conn.RemoteAddr()
	if local == nil || remote == nil {
		t.Fatalf("invalid addresses: local=%v remote=%v", local, remote)
	}
	if local.String() == "" || remote.String() == "" {
		t.Fatalf("empty addresses: local=%q remote=%q", local.String(), remote.String())
	}
	if local.Network() == "" {
		t.Fatal("empty local network")
	}
}

func TestQuicLazyConnectionCache(t *testing.T) {
	_, client, serverAddr := setupRelayPair(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := client.DialStream(ctx, serverAddr)
	if err != nil {
		t.Fatalf("first dial stream: %v", err)
	}
	defer func() { _ = first.Close() }()

	second, err := client.DialStream(ctx, serverAddr)
	if err != nil {
		t.Fatalf("second dial stream: %v", err)
	}
	defer func() { _ = second.Close() }()

	if first.RemoteAddr() == nil || second.RemoteAddr() == nil {
		t.Fatalf("unexpected nil remote addresses: first=%v second=%v", first.RemoteAddr(), second.RemoteAddr())
	}
	if first.RemoteAddr().String() != second.RemoteAddr().String() {
		t.Fatalf("unexpected remote addresses: first=%v second=%v", first.RemoteAddr(), second.RemoteAddr())
	}
}
