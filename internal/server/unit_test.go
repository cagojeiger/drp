package server

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/protocol"
	"github.com/cagojeiger/drp/internal/registry"
	drppb "github.com/cagojeiger/drp/proto/drp"
)

// ---------- helpers ----------

func readN(t *testing.T, r *bufio.Reader, conn net.Conn, n int) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, n)
	off := 0
	for off < n {
		nn, err := r.Read(b[off:])
		off += nn
		if err != nil {
			t.Fatalf("readN(%d): read %d bytes then: %v", n, off, err)
		}
	}
	return b
}

func readEnvWithBuf(t *testing.T, r *bufio.Reader, conn net.Conn) *drppb.Envelope {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	return env
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for goroutine")
	}
}

func mustWrite(t *testing.T, conn net.Conn, b []byte) {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(b); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

// drainConn reads from conn until EOF or timeout, returning everything read.
// The conn's other end must be closed eventually to unblock this.
func drainConn(conn net.Conn, timeout time.Duration) string {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return sb.String()
}

func tlsClientHelloBytes(t *testing.T, sni string) []byte {
	t.Helper()
	client, server := net.Pipe()
	defer func() { _ = server.Close() }()

	helloCh := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := server.Read(buf)
		if err != nil {
			helloCh <- nil
			return
		}
		helloCh <- append([]byte(nil), buf[:n]...)
	}()

	go func() {
		tlsConn := tls.Client(client, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		_ = tlsConn.SetDeadline(time.Now().Add(300 * time.Millisecond))
		_ = tlsConn.Handshake()
		_ = tlsConn.Close()
	}()

	select {
	case hello := <-helloCh:
		if len(hello) == 0 {
			t.Fatal("failed to capture client hello")
		}
		return hello
	case <-time.After(2 * time.Second):
		t.Fatal("timeout capturing client hello")
		return nil
	}
}

// ---------- routeRequest tests ----------

func TestRouteRequest_NotFound(t *testing.T) {
	t.Parallel()
	s := &Server{lookup: &fakeLookup{services: map[string]registry.ServiceInfo{}}}
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.routeRequest("unknown.com", serverConn, nil)
	}()

	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "502 Bad Gateway") {
		t.Fatalf("expected 502, got %q", got)
	}
}

func TestRouteRequest_Local(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = workClient.Close() }()

	s := &Server{
		lookup: &fakeLookup{services: map[string]registry.ServiceInfo{
			"local.example.com": {ProxyAlias: "app", IsLocal: true},
		}},
		broker: &fakeBroker{conn: workServer},
	}

	done := make(chan struct{})
	buf := []byte("GET / HTTP/1.1\r\nHost: local.example.com\r\n\r\n")
	go func() {
		s.routeRequest("local.example.com", userServer, buf)
		close(done)
	}()

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}
	if got := string(readN(t, workR, workClient, len(buf))); got != string(buf) {
		t.Fatalf("buffered data mismatch: %q", got)
	}

	mustWrite(t, workClient, []byte("from-work"))
	userClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	tmp := make([]byte, 100)
	n, _ := userClient.Read(tmp)
	if !strings.Contains(string(tmp[:n]), "from-work") {
		t.Fatalf("work->user mismatch: %q", tmp[:n])
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestRouteRequest_Remote(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	streamServer, streamClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = streamClient.Close() }()

	s := &Server{
		lookup: &fakeLookup{services: map[string]registry.ServiceInfo{
			"remote.example.com": {ProxyAlias: "app", NodeID: "node-b", IsLocal: false},
		}},
		relayer: &fakeRelayer{conn: streamServer},
	}

	done := make(chan struct{})
	buf := []byte("GET /r HTTP/1.1\r\nHost: remote.example.com\r\n\r\n")
	go func() {
		s.routeRequest("remote.example.com", userServer, buf)
		close(done)
	}()

	streamR := bufio.NewReader(streamClient)
	env := readEnvWithBuf(t, streamR, streamClient)
	ro := env.GetRelayOpen()
	if ro == nil || ro.ProxyAlias != "app" {
		t.Fatalf("unexpected RelayOpen: %+v", ro)
	}
	if got := string(readN(t, streamR, streamClient, len(buf))); got != string(buf) {
		t.Fatalf("buffered data mismatch: %q", got)
	}

	mustWrite(t, streamClient, []byte("relay->user"))
	userClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	tmp := make([]byte, 100)
	n, _ := userClient.Read(tmp)
	if !strings.Contains(string(tmp[:n]), "relay->user") {
		t.Fatalf("stream->user mismatch: %q", tmp[:n])
	}

	_ = userClient.Close()
	_ = streamClient.Close()
	waitDone(t, done)
}

// ---------- localRoute tests ----------

func TestLocalRoute_Success(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = workClient.Close() }()

	s := &Server{broker: &fakeBroker{conn: workServer}}
	info := registry.ServiceInfo{ProxyAlias: "app"}
	buf := []byte("prefetch")

	done := make(chan struct{})
	go func() {
		s.localRoute(info, userServer, buf)
		close(done)
	}()

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}
	if got := string(readN(t, workR, workClient, len(buf))); got != string(buf) {
		t.Fatalf("buffered data mismatch: %q", got)
	}

	mustWrite(t, workClient, []byte("w2u"))
	userClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	tmp := make([]byte, 100)
	n, _ := userClient.Read(tmp)
	if !strings.Contains(string(tmp[:n]), "w2u") {
		t.Fatalf("work->user mismatch: %q", tmp[:n])
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestLocalRoute_BrokerTimeout(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	s := &Server{broker: &fakeBroker{err: ErrWorkConnTimeout}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.localRoute(registry.ServiceInfo{ProxyAlias: "app"}, serverConn, nil)
	}()

	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "504 Gateway Timeout") {
		t.Fatalf("expected 504 response, got %q", got)
	}
}

func TestLocalRoute_BrokerServiceNotFound(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	s := &Server{broker: &fakeBroker{err: ErrServiceNotFound}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.localRoute(registry.ServiceInfo{ProxyAlias: "app"}, serverConn, nil)
	}()

	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "502 Bad Gateway") {
		t.Fatalf("expected 502 response, got %q", got)
	}
}

// ---------- remoteRelay tests ----------

func TestRemoteRelay_Success(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	streamServer, streamClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = streamClient.Close() }()

	s := &Server{relayer: &fakeRelayer{conn: streamServer}}
	info := registry.ServiceInfo{ProxyAlias: "app", NodeID: "node-z"}
	buf := []byte("hello")

	done := make(chan struct{})
	go func() {
		s.remoteRelay(info, userServer, buf)
		close(done)
	}()

	streamR := bufio.NewReader(streamClient)
	env := readEnvWithBuf(t, streamR, streamClient)
	ro := env.GetRelayOpen()
	if ro == nil || ro.ProxyAlias != "app" {
		t.Fatalf("unexpected RelayOpen: %+v", ro)
	}
	if got := string(readN(t, streamR, streamClient, len(buf))); got != string(buf) {
		t.Fatalf("buffered data mismatch: %q", got)
	}

	mustWrite(t, streamClient, []byte("to-user"))
	userClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	tmp := make([]byte, 100)
	n, _ := userClient.Read(tmp)
	if !strings.Contains(string(tmp[:n]), "to-user") {
		t.Fatalf("stream->user mismatch: %q", tmp[:n])
	}

	_ = userClient.Close()
	_ = streamClient.Close()
	waitDone(t, done)
}

func TestRemoteRelay_DialFailed(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	s := &Server{relayer: &fakeRelayer{err: errors.New("dial failed")}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.remoteRelay(registry.ServiceInfo{ProxyAlias: "app", NodeID: "node-1"}, serverConn, nil)
	}()

	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "502 Bad Gateway") {
		t.Fatalf("expected 502 response, got %q", got)
	}
}

// ---------- handleRelayConn tests ----------

func TestHandleRelayConn_Success(t *testing.T) {
	t.Parallel()
	relayServer, relayClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = relayClient.Close() }()
	defer func() { _ = workClient.Close() }()

	s := &Server{broker: &fakeBroker{conn: workServer}}
	done := make(chan struct{})
	go func() {
		s.handleRelayConn(relayServer)
		close(done)
	}()

	// net.Pipe is synchronous: writes block until reads drain the other end.
	// handleRelayConn writes StartWorkConn to workServer before it starts
	// reading from relayServer, so we must read workClient concurrently.
	workR := bufio.NewReader(workClient)

	// Send RelayOpen (handleRelayConn reads this first)
	if err := protocol.WriteEnvelope(relayClient, &drppb.Envelope{
		Payload: &drppb.Envelope_RelayOpen{RelayOpen: &drppb.RelayOpen{ProxyAlias: "app", RequestId: "r1"}},
	}); err != nil {
		t.Fatalf("write RelayOpen: %v", err)
	}

	// Read StartWorkConn (unblocks handleRelayConn's write to workServer)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}

	// relay→work: net.Pipe requires concurrent read/write
	relayWorkCh := make(chan string, 1)
	go func() {
		got := readN(t, workR, workClient, len("relay-data"))
		relayWorkCh <- string(got)
	}()
	mustWrite(t, relayClient, []byte("relay-data"))
	if got := <-relayWorkCh; got != "relay-data" {
		t.Fatalf("relay->work mismatch: %q", got)
	}

	// work→relay: net.Pipe requires concurrent read/write
	workRelayCh := make(chan string, 1)
	go func() {
		relayClient.SetReadDeadline(time.Now().Add(3 * time.Second))
		tmp := make([]byte, 100)
		n, _ := relayClient.Read(tmp)
		workRelayCh <- string(tmp[:n])
	}()
	mustWrite(t, workClient, []byte("work-data"))
	if got := <-workRelayCh; !strings.Contains(got, "work-data") {
		t.Fatalf("work->relay mismatch: %q", got)
	}

	_ = relayClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestHandleRelayConn_InvalidMessage(t *testing.T) {
	t.Parallel()
	relayServer, relayClient := net.Pipe()
	defer func() { _ = relayClient.Close() }()

	s := &Server{broker: &fakeBroker{}}
	done := make(chan struct{})
	go func() {
		s.handleRelayConn(relayServer)
		close(done)
	}()

	if err := protocol.WriteEnvelope(relayClient, &drppb.Envelope{
		Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}},
	}); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	waitDone(t, done)
}

func TestHandleRelayConn_BrokerError(t *testing.T) {
	t.Parallel()
	relayServer, relayClient := net.Pipe()
	defer func() { _ = relayClient.Close() }()

	s := &Server{broker: &fakeBroker{err: ErrServiceNotFound}}
	done := make(chan struct{})
	go func() {
		s.handleRelayConn(relayServer)
		close(done)
	}()

	if err := protocol.WriteEnvelope(relayClient, &drppb.Envelope{
		Payload: &drppb.Envelope_RelayOpen{RelayOpen: &drppb.RelayOpen{ProxyAlias: "missing", RequestId: "r2"}},
	}); err != nil {
		t.Fatalf("write RelayOpen: %v", err)
	}

	waitDone(t, done)
}

// ---------- serveConn tests ----------

func TestServeConn(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = workClient.Close() }()

	done := make(chan struct{})
	buf := []byte("front-buffer")
	go func() {
		serveConn(workServer, userServer, "app", buf)
		close(done)
	}()

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}
	if got := string(readN(t, workR, workClient, len(buf))); got != string(buf) {
		t.Fatalf("buffered data mismatch: %q", got)
	}

	mustWrite(t, userClient, []byte("user-data"))
	if got := string(readN(t, workR, workClient, len("user-data"))); got != "user-data" {
		t.Fatalf("user->work mismatch: %q", got)
	}
	mustWrite(t, workClient, []byte("work-data"))
	userClient.SetReadDeadline(time.Now().Add(3 * time.Second))
	tmp := make([]byte, 100)
	n, _ := userClient.Read(tmp)
	if !strings.Contains(string(tmp[:n]), "work-data") {
		t.Fatalf("work->user mismatch: %q", tmp[:n])
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

// ---------- handleHTTP tests ----------

func TestHandleHTTP_ValidHost(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = workClient.Close() }()

	s := &Server{
		lookup: &fakeLookup{services: map[string]registry.ServiceInfo{
			"myapp.example.com": {ProxyAlias: "app", IsLocal: true},
		}},
		broker: &fakeBroker{conn: workServer},
	}

	done := make(chan struct{})
	go func() {
		s.handleHTTP(userServer)
		close(done)
	}()

	req := []byte("GET /ok HTTP/1.1\r\nHost: myapp.example.com\r\n\r\n")
	mustWrite(t, userClient, req)

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}
	if got := string(readN(t, workR, workClient, len(req))); got != string(req) {
		t.Fatalf("request mismatch: %q", got)
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestHandleHTTP_NoHost(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	s := &Server{}
	done := make(chan struct{})
	go func() {
		s.handleHTTP(serverConn)
		close(done)
	}()

	mustWrite(t, userConn, []byte("GET / HTTP/1.1\r\nUser-Agent: test\r\n\r\n"))
	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "400 Bad Request") {
		t.Fatalf("expected 400 response, got %q", got)
	}
}

func TestHandleHTTP_UnknownHost(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()
	defer func() { _ = userConn.Close() }()

	s := &Server{
		lookup: &fakeLookup{services: map[string]registry.ServiceInfo{}},
	}
	done := make(chan struct{})
	go func() {
		s.handleHTTP(serverConn)
		close(done)
	}()

	mustWrite(t, userConn, []byte("GET / HTTP/1.1\r\nHost: unknown.test\r\n\r\n"))
	got := drainConn(userConn, 3*time.Second)
	waitDone(t, done)
	if !strings.Contains(got, "502 Bad Gateway") {
		t.Fatalf("expected 502 response, got %q", got)
	}
}

// ---------- handleHTTPS tests ----------

func TestHandleHTTPS_ValidSNI(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()
	defer func() { _ = userClient.Close() }()
	defer func() { _ = workClient.Close() }()

	s := &Server{
		lookup: &fakeLookup{services: map[string]registry.ServiceInfo{
			"tls.example.com": {ProxyAlias: "app", IsLocal: true},
		}},
		broker: &fakeBroker{conn: workServer},
	}

	done := make(chan struct{})
	go func() {
		s.handleHTTPS(userServer)
		close(done)
	}()

	hello := tlsClientHelloBytes(t, "tls.example.com")
	mustWrite(t, userClient, hello)

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if swc := env.GetStartWorkConn(); swc == nil || swc.ProxyAlias != "app" {
		t.Fatalf("unexpected StartWorkConn: %+v", swc)
	}
	if got := readN(t, workR, workClient, len(hello)); string(got) != string(hello) {
		t.Fatalf("tls hello mismatch")
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestHandleHTTPS_NoSNI(t *testing.T) {
	t.Parallel()
	serverConn, userConn := net.Pipe()

	s := &Server{}
	done := make(chan struct{})
	go func() {
		s.handleHTTPS(serverConn)
		close(done)
	}()

	// Non-TLS data, then close
	_, _ = userConn.Write([]byte("not-tls-data"))
	_ = userConn.Close()

	waitDone(t, done)
}

// ---------- handleControl + clientSession tests ----------

func TestHandleControl_LoginAndProxy(t *testing.T) {
	t.Parallel()
	registrar := &fakeRegistrar{}
	s := &Server{
		registrar: registrar,
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(clientConn)
	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k", Version: "v"}},
	}); err != nil {
		t.Fatalf("write login: %v", err)
	}
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read login resp: %v", err)
	}
	if resp := env.GetLoginResp(); resp == nil || !resp.Ok {
		t.Fatalf("unexpected login resp: %+v", resp)
	}

	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{Alias: "app", Hostname: "app.example.com", Type: "http"}},
	}); err != nil {
		t.Fatalf("write new proxy: %v", err)
	}
	env, err = protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read new proxy resp: %v", err)
	}
	if resp := env.GetNewProxyResp(); resp == nil || !resp.Ok {
		t.Fatalf("unexpected new proxy resp: %+v", resp)
	}

	if reg := registrar.getRegistered(); len(reg) != 1 || reg[0] != "app.example.com" {
		t.Fatalf("unexpected registered list: %+v", reg)
	}

	// Verify service entry in map
	s.mu.RLock()
	_, ok := s.services["app"]
	s.mu.RUnlock()
	if !ok {
		t.Fatal("expected service entry for app")
	}

	// Close connection to trigger cleanup
	_ = clientConn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if unreg := registrar.getUnregistered(); len(unreg) == 1 && unreg[0] == "app.example.com" {
			// Verify removed from services map
			s.mu.RLock()
			_, stillThere := s.services["app"]
			s.mu.RUnlock()
			if stillThere {
				t.Fatal("service entry should have been removed after disconnect")
			}
			waitDone(t, done)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected unregister call, got %+v", registrar.getUnregistered())
}

func TestHandleControl_LoginRejected(t *testing.T) {
	t.Parallel()
	s := &Server{
		cfg: ServerConfig{Authenticate: func(login *drppb.Login) (bool, string) {
			return false, "bad auth"
		}},
		registrar: &fakeRegistrar{},
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(clientConn)
	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "bad", Version: "v"}},
	}); err != nil {
		t.Fatalf("write login: %v", err)
	}
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read login resp: %v", err)
	}
	resp := env.GetLoginResp()
	if resp == nil || resp.Ok || resp.Error != "bad auth" {
		t.Fatalf("unexpected login response: %+v", resp)
	}

	waitDone(t, done)
}

func TestHandleControl_ProxyRejected(t *testing.T) {
	t.Parallel()
	registrar := &fakeRegistrar{}
	s := &Server{
		cfg: ServerConfig{AuthorizeProxy: func(proxy *drppb.NewProxy) (bool, string) {
			return false, "proxy not allowed"
		}},
		registrar: registrar,
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(clientConn)

	// Login (succeeds — no Authenticate set)
	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "test"}},
	})
	env, _ := protocol.ReadEnvelope(r)
	if !env.GetLoginResp().Ok {
		t.Fatal("login should have succeeded")
	}

	// NewProxy (rejected)
	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: "app", Hostname: "app.test", Type: "http",
		}},
	})
	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read proxy resp: %v", err)
	}
	proxyResp := env.GetNewProxyResp()
	if proxyResp == nil || proxyResp.Ok {
		t.Fatal("expected proxy rejection")
	}
	if proxyResp.Error != "proxy not allowed" {
		t.Fatalf("expected 'proxy not allowed', got: %q", proxyResp.Error)
	}

	if reg := registrar.getRegistered(); len(reg) != 0 {
		t.Fatalf("should not have registered, got: %v", reg)
	}

	waitDone(t, done)
}

func TestHandleControl_PingPong(t *testing.T) {
	t.Parallel()
	s := &Server{
		registrar: &fakeRegistrar{},
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()

	go s.handleControl(serverConn)

	r := bufio.NewReader(clientConn)

	// Login
	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "test"}},
	})
	protocol.ReadEnvelope(r)

	// NewProxy
	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: "app", Hostname: "app.test", Type: "http",
		}},
	})
	protocol.ReadEnvelope(r)

	// Ping
	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}},
	}); err != nil {
		t.Fatalf("write ping: %v", err)
	}

	env, err := protocol.ReadEnvelope(r)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if env.GetPong() == nil {
		t.Fatalf("expected Pong, got %T", env.Payload)
	}
}

func TestHandleControl_WorkConn(t *testing.T) {
	t.Parallel()
	entry := &serviceEntry{alias: "app", hostname: "app.example.com", workQueue: make(chan net.Conn, 1)}
	s := &Server{services: map[string]*serviceEntry{"app": entry}}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	done := make(chan struct{})
	go func() {
		s.handleControl(serverConn)
		close(done)
	}()

	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{ProxyAlias: "app"}},
	}); err != nil {
		t.Fatalf("write NewWorkConn: %v", err)
	}

	select {
	case conn := <-entry.workQueue:
		if conn == nil {
			t.Fatal("unexpected nil conn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("work connection was not queued")
	}

	waitDone(t, done)
}

func TestHandleControl_WorkConn_UnknownAlias(t *testing.T) {
	t.Parallel()
	s := &Server{services: make(map[string]*serviceEntry)}

	serverConn, clientConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	done := make(chan struct{})
	go func() {
		s.handleControl(serverConn)
		close(done)
	}()

	if err := protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{ProxyAlias: "nonexistent"}},
	}); err != nil {
		t.Fatalf("write NewWorkConn: %v", err)
	}

	waitDone(t, done)
}

// ---------- realBroker tests ----------

func TestRealBroker_ServiceNotFound(t *testing.T) {
	t.Parallel()
	var mu sync.RWMutex
	b := &realBroker{mu: &mu, services: make(map[string]*serviceEntry)}
	_, err := b.RequestAndWait("nonexistent", 100*time.Millisecond)
	if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("expected ErrServiceNotFound, got: %v", err)
	}
}

func TestRealBroker_Timeout(t *testing.T) {
	t.Parallel()
	ctrlC, ctrlS := net.Pipe()
	defer func() { _ = ctrlC.Close() }()
	defer func() { _ = ctrlS.Close() }()

	var mu sync.RWMutex
	services := map[string]*serviceEntry{
		"myapp": {
			alias:     "myapp",
			hostname:  "myapp.test",
			ctrlConn:  ctrlS,
			workQueue: make(chan net.Conn, 10),
		},
	}

	b := &realBroker{mu: &mu, services: services}

	// Drain the ReqWorkConn
	go func() {
		r := bufio.NewReader(ctrlC)
		protocol.ReadEnvelope(r)
	}()

	_, err := b.RequestAndWait("myapp", 100*time.Millisecond)
	if !errors.Is(err, ErrWorkConnTimeout) {
		t.Fatalf("expected ErrWorkConnTimeout, got: %v", err)
	}
}

func TestRealBroker_Success(t *testing.T) {
	t.Parallel()
	ctrlC, ctrlS := net.Pipe()
	defer func() { _ = ctrlC.Close() }()
	defer func() { _ = ctrlS.Close() }()

	queue := make(chan net.Conn, 10)
	var mu sync.RWMutex
	services := map[string]*serviceEntry{
		"myapp": {
			alias:     "myapp",
			hostname:  "myapp.test",
			ctrlConn:  ctrlS,
			workQueue: queue,
		},
	}

	b := &realBroker{mu: &mu, services: services}

	go func() {
		r := bufio.NewReader(ctrlC)
		env, err := protocol.ReadEnvelope(r)
		if err != nil {
			return
		}
		if env.GetReqWorkConn() == nil {
			return
		}
		fakeConn, _ := net.Pipe()
		queue <- fakeConn
	}()

	conn, err := b.RequestAndWait("myapp", 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil conn")
	}
	_ = conn.Close()
}

// ---------- acceptWorkConn tests ----------

func TestAcceptWorkConn_Success(t *testing.T) {
	t.Parallel()
	queue := make(chan net.Conn, 10)
	s := &Server{services: map[string]*serviceEntry{
		"myapp": {alias: "myapp", hostname: "myapp.test", workQueue: queue},
	}}

	_, workS := net.Pipe()
	s.acceptWorkConn(workS, &drppb.NewWorkConn{ProxyAlias: "myapp"})

	select {
	case conn := <-queue:
		if conn == nil {
			t.Fatal("expected non-nil conn")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("conn not in queue")
	}
}

func TestAcceptWorkConn_UnknownAlias(t *testing.T) {
	t.Parallel()
	s := &Server{services: make(map[string]*serviceEntry)}

	_, workS := net.Pipe()
	s.acceptWorkConn(workS, &drppb.NewWorkConn{ProxyAlias: "ghost"})

	// Verify connection was closed
	time.Sleep(50 * time.Millisecond)
	_, err := workS.Write([]byte("test"))
	if err == nil {
		t.Fatal("expected write to closed conn to fail")
	}
}

func TestHandleControl_SessionEOFCleansUp(t *testing.T) {
	t.Parallel()
	registrar := &fakeRegistrar{}
	s := &Server{
		registrar: registrar,
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(clientConn)

	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k"}},
	})
	env, _ := protocol.ReadEnvelope(r)
	if !env.GetLoginResp().Ok {
		t.Fatal("login should succeed")
	}

	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: "svc", Hostname: "svc.test", Type: "http",
		}},
	})
	env, _ = protocol.ReadEnvelope(r)
	if !env.GetNewProxyResp().Ok {
		t.Fatal("proxy should succeed")
	}

	s.mu.RLock()
	_, exists := s.services["svc"]
	s.mu.RUnlock()
	if !exists {
		t.Fatal("service should exist before disconnect")
	}

	_ = clientConn.Close()

	waitDone(t, done)

	s.mu.RLock()
	_, stillExists := s.services["svc"]
	s.mu.RUnlock()
	if stillExists {
		t.Fatal("service should be removed after connection drop")
	}

	unreg := registrar.getUnregistered()
	if len(unreg) != 1 || unreg[0] != "svc.test" {
		t.Fatalf("expected unregister of svc.test, got %v", unreg)
	}
}

func TestHandleControl_LoginWriteFailure(t *testing.T) {
	t.Parallel()
	s := &Server{
		registrar: &fakeRegistrar{},
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k"}},
	})
	_ = clientConn.Close()

	waitDone(t, done)
}

func TestHandleControl_PongWriteFailure(t *testing.T) {
	t.Parallel()
	registrar := &fakeRegistrar{}
	s := &Server{
		registrar: registrar,
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(clientConn)
	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k"}},
	})
	if env, err := protocol.ReadEnvelope(r); err != nil || !env.GetLoginResp().Ok {
		t.Fatal("login should succeed")
	}

	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: "hb", Hostname: "hb.test", Type: "http",
		}},
	})
	if env, err := protocol.ReadEnvelope(r); err != nil || !env.GetNewProxyResp().Ok {
		t.Fatal("proxy should succeed")
	}

	_ = protocol.WriteEnvelope(clientConn, &drppb.Envelope{
		Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}},
	})
	_ = clientConn.Close()

	waitDone(t, done)

	unreg := registrar.getUnregistered()
	if len(unreg) != 1 || unreg[0] != "hb.test" {
		t.Fatalf("expected unregister of hb.test, got %v", unreg)
	}
}

func TestLocalRoute_PipeMidStreamClose(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	workServer, workClient := net.Pipe()

	s := &Server{broker: &fakeBroker{conn: workServer}}
	info := registry.ServiceInfo{ProxyAlias: "app"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.localRoute(info, userServer, nil)
	}()

	workR := bufio.NewReader(workClient)
	env := readEnvWithBuf(t, workR, workClient)
	if env.GetStartWorkConn() == nil {
		t.Fatal("expected StartWorkConn")
	}

	_ = userClient.Close()
	_ = workClient.Close()
	waitDone(t, done)
}

func TestRemoteRelay_PipeMidStreamClose(t *testing.T) {
	t.Parallel()
	userServer, userClient := net.Pipe()
	streamServer, streamClient := net.Pipe()

	s := &Server{relayer: &fakeRelayer{conn: streamServer}}
	info := registry.ServiceInfo{ProxyAlias: "app", NodeID: "node-b", IsLocal: false}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.remoteRelay(info, userServer, nil)
	}()

	streamR := bufio.NewReader(streamClient)
	env := readEnvWithBuf(t, streamR, streamClient)
	if env.GetRelayOpen() == nil {
		t.Fatal("expected RelayOpen")
	}

	_ = userClient.Close()
	_ = streamClient.Close()
	waitDone(t, done)
}

func TestRealBroker_ControlConnWriteError(t *testing.T) {
	t.Parallel()
	ctrlServer, ctrlClient := net.Pipe()

	_ = ctrlClient.Close()

	var mu sync.RWMutex
	services := map[string]*serviceEntry{
		"app": {
			alias:     "app",
			hostname:  "app.test",
			ctrlConn:  ctrlServer,
			workQueue: make(chan net.Conn, 10),
		},
	}

	b := &realBroker{mu: &mu, services: services}
	_, err := b.RequestAndWait("app", 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected error when control conn is closed")
	}
}

func TestHandleControl_ReadErrorAfterProxy(t *testing.T) {
	t.Parallel()
	registrar := &fakeRegistrar{}
	s := &Server{
		registrar: registrar,
		services:  make(map[string]*serviceEntry),
	}

	serverConn, clientConn := net.Pipe()
	fc := newFaultConn(clientConn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleControl(serverConn)
	}()

	r := bufio.NewReader(fc)

	_ = protocol.WriteEnvelope(fc, &drppb.Envelope{
		Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k"}},
	})
	protocol.ReadEnvelope(r)

	_ = protocol.WriteEnvelope(fc, &drppb.Envelope{
		Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
			Alias: "myapp", Hostname: "myapp.test", Type: "http",
		}},
	})
	protocol.ReadEnvelope(r)

	fc.InjectReadError(errors.New("connection reset by peer"))

	_ = fc.Close()

	waitDone(t, done)

	unreg := registrar.getUnregistered()
	if len(unreg) != 1 || unreg[0] != "myapp.test" {
		t.Fatalf("expected unregister of myapp.test, got %v", unreg)
	}
}
