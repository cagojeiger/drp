package protocol

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"

	drppb "github.com/cagojeiger/drp/proto/drp"
	"google.golang.org/protobuf/proto"
)

func roundTripEnvelope(t *testing.T, env *drppb.Envelope) *drppb.Envelope {
	t.Helper()

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteEnvelope(left, env)
	}()

	got, err := ReadEnvelope(bufio.NewReader(right))
	if err != nil {
		t.Fatalf("ReadEnvelope() error = %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteEnvelope() error = %v", err)
	}

	return got
}

func TestWriteReadEnvelopeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		env  *drppb.Envelope
	}{
		{"login", &drppb.Envelope{Payload: &drppb.Envelope_Login{Login: &drppb.Login{ApiKey: "k", Version: "1"}}}},
		{"login_resp", &drppb.Envelope{Payload: &drppb.Envelope_LoginResp{LoginResp: &drppb.LoginResp{Ok: true}}}},
		{"new_proxy", &drppb.Envelope{Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{Alias: "a", Hostname: "h", Type: "http"}}}},
		{"new_proxy_resp", &drppb.Envelope{Payload: &drppb.Envelope_NewProxyResp{NewProxyResp: &drppb.NewProxyResp{Ok: false, Error: "e"}}}},
		{"req_work_conn", &drppb.Envelope{Payload: &drppb.Envelope_ReqWorkConn{ReqWorkConn: &drppb.ReqWorkConn{ProxyAlias: "a"}}}},
		{"new_work_conn", &drppb.Envelope{Payload: &drppb.Envelope_NewWorkConn{NewWorkConn: &drppb.NewWorkConn{ProxyAlias: "a"}}}},
		{"start_work_conn", &drppb.Envelope{Payload: &drppb.Envelope_StartWorkConn{StartWorkConn: &drppb.StartWorkConn{ProxyAlias: "a"}}}},
		{"ping", &drppb.Envelope{Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}}}},
		{"pong", &drppb.Envelope{Payload: &drppb.Envelope_Pong{Pong: &drppb.Pong{}}}},
		{"relay_open", &drppb.Envelope{Payload: &drppb.Envelope_RelayOpen{RelayOpen: &drppb.RelayOpen{ProxyAlias: "a", RequestId: "r"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roundTripEnvelope(t, tt.env)
			if !proto.Equal(got, tt.env) {
				t.Fatalf("round-trip mismatch: got=%v want=%v", got, tt.env)
			}
		})
	}
}

func TestWriteReadEmptyMessages(t *testing.T) {
	for _, env := range []*drppb.Envelope{
		{Payload: &drppb.Envelope_Ping{Ping: &drppb.Ping{}}},
		{Payload: &drppb.Envelope_Pong{Pong: &drppb.Pong{}}},
	} {
		got := roundTripEnvelope(t, env)
		if !proto.Equal(got, env) {
			t.Fatalf("round-trip mismatch: got=%v want=%v", got, env)
		}
	}
}

func TestLargePayload(t *testing.T) {
	env := &drppb.Envelope{Payload: &drppb.Envelope_NewProxy{NewProxy: &drppb.NewProxy{
		Alias:    "a",
		Hostname: strings.Repeat("h", 64*1024),
		Type:     "http",
	}}}

	got := roundTripEnvelope(t, env)
	if !proto.Equal(got, env) {
		t.Fatalf("round-trip mismatch for large payload")
	}
}

func TestExtractHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"host_basic", "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com"},
		{"host_with_port", "GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n", "example.com"},
		{"host_case_insensitive", "GET / HTTP/1.1\r\nhost: EXAMPLE.COM\r\n\r\n", "example.com"},
		{"host_ipv6", "GET / HTTP/1.1\r\nHost: [::1]:8080\r\n\r\n", "::1"},
		{"host_missing", "GET / HTTP/1.1\r\n\r\n", ""},
		{"garbage", "random garbage", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractHost([]byte(tt.raw))
			if got != tt.want {
				t.Fatalf("ExtractHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func buildClientHello(serverName string) []byte {
	appendU16 := func(dst []byte, v int) []byte {
		return append(dst, byte(v>>8), byte(v))
	}

	name := []byte(serverName)
	sniData := make([]byte, 0, 5+len(name))
	sniData = appendU16(sniData, 3+len(name))
	sniData = append(sniData, 0x00)
	sniData = appendU16(sniData, len(name))
	sniData = append(sniData, name...)

	exts := make([]byte, 0, 4+len(sniData))
	exts = appendU16(exts, 0x0000)
	exts = appendU16(exts, len(sniData))
	exts = append(exts, sniData...)

	ch := []byte{0x03, 0x03}
	ch = append(ch, make([]byte, 32)...)
	ch = append(ch, 0x00)
	ch = appendU16(ch, 2)
	ch = append(ch, 0x13, 0x01)
	ch = append(ch, 0x01, 0x00)
	ch = appendU16(ch, len(exts))
	ch = append(ch, exts...)

	hs := []byte{0x01, 0x00, 0x00, 0x00}
	hsLen := len(ch)
	hs[1] = byte(hsLen >> 16)
	hs[2] = byte(hsLen >> 8)
	hs[3] = byte(hsLen)
	hs = append(hs, ch...)

	record := []byte{0x16, 0x03, 0x03, 0x00, 0x00}
	recordLen := len(hs)
	record[3] = byte(recordLen >> 8)
	record[4] = byte(recordLen)
	record = append(record, hs...)

	return record
}

func TestExtractSNI(t *testing.T) {
	notClientHello := buildClientHello("example.com")
	notClientHello[5] = 0x02

	tests := []struct {
		name  string
		hello []byte
		want  string
	}{
		{"valid_client_hello", buildClientHello("example.com"), "example.com"},
		{"non_tls", []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"), ""},
		{"empty", nil, ""},
		{"short", []byte{0x16, 0x03, 0x03}, ""},
		{"not_client_hello", notClientHello, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractSNI(tt.hello)
			if got != tt.want {
				t.Fatalf("ExtractSNI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPipe(t *testing.T) {
	srcR, srcW := net.Pipe()
	dstR, dstW := net.Pipe()
	defer srcR.Close()
	defer srcW.Close()
	defer dstR.Close()
	defer dstW.Close()

	const msg = "hello world"
	errPipe := make(chan error, 1)
	errWrite := make(chan error, 1)

	go func() {
		errPipe <- Pipe(dstW, srcR)
	}()

	go func() {
		_, err := srcW.Write([]byte(msg))
		if err == nil {
			err = srcW.Close()
		}
		errWrite <- err
	}()

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(dstR, buf); err != nil {
		t.Fatalf("io.ReadFull() error = %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("Pipe copied %q, want %q", string(buf), msg)
	}
	if err := <-errWrite; err != nil {
		t.Fatalf("source write error = %v", err)
	}
	if err := <-errPipe; err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
}
