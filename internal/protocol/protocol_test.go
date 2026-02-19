package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

func TestReadWriteMsg(t *testing.T) {
	var buf bytes.Buffer
	want := LoginBody{Alias: "myapp"}

	if err := WriteMsg(&buf, MsgLogin, want); err != nil {
		t.Fatalf("WriteMsg failed: %v", err)
	}

	msgType, body, err := ReadMsg(&buf)
	if err != nil {
		t.Fatalf("ReadMsg failed: %v", err)
	}

	if msgType != MsgLogin {
		t.Fatalf("msg type mismatch: got %q, want %q", msgType, MsgLogin)
	}

	var got LoginBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}

	if got != want {
		t.Fatalf("body mismatch: got %+v, want %+v", got, want)
	}
}

func TestAllMessageTypes(t *testing.T) {
	testCases := []struct {
		name       string
		msgType    byte
		body       interface{}
		expectZero bool
	}{
		{name: "Login", msgType: MsgLogin, body: LoginBody{Alias: "a"}},
		{name: "LoginResp", msgType: MsgLoginResp, body: LoginRespBody{OK: true, Message: "ok"}},
		{name: "NewProxy", msgType: MsgNewProxy, body: NewProxyBody{Alias: "a", Hostname: "a.example.com"}},
		{name: "NewProxyResp", msgType: MsgNewProxyResp, body: NewProxyRespBody{OK: true, Message: "ok"}},
		{name: "ReqWorkConn", msgType: MsgReqWorkConn, body: ReqWorkConnBody{}, expectZero: true},
		{name: "NewWorkConn", msgType: MsgNewWorkConn, body: NewWorkConnBody{Alias: "a"}},
		{name: "StartWorkConn", msgType: MsgStartWorkConn, body: StartWorkConnBody{Hostname: "a.example.com"}},
		{name: "Ping", msgType: MsgPing, body: nil, expectZero: true},
		{name: "Pong", msgType: MsgPong, body: nil, expectZero: true},
		{name: "MeshHello", msgType: MsgMeshHello, body: MeshHelloBody{NodeID: "A", Peers: []string{"B"}, ControlPort: 9000}},
		{name: "WhoHas", msgType: MsgWhoHas, body: WhoHasBody{MsgID: "id1", Hostname: "a.example.com", TTL: 3, Path: []string{"C"}}},
		{name: "IHave", msgType: MsgIHave, body: IHaveBody{MsgID: "id1", Hostname: "a.example.com", NodeID: "A", Path: []string{"C", "B"}}},
		{name: "RelayOpen", msgType: MsgRelayOpen, body: RelayOpenBody{RelayID: "r1", Hostname: "a.example.com", NextHops: []string{"B", "A"}}},
	}

	var buf bytes.Buffer
	for _, tc := range testCases {
		if err := WriteMsg(&buf, tc.msgType, tc.body); err != nil {
			t.Fatalf("WriteMsg(%s) failed: %v", tc.name, err)
		}
	}

	for _, tc := range testCases {
		msgType, body, err := ReadMsg(&buf)
		if err != nil {
			t.Fatalf("ReadMsg(%s) failed: %v", tc.name, err)
		}

		if msgType != tc.msgType {
			t.Fatalf("msg type mismatch for %s: got %q, want %q", tc.name, msgType, tc.msgType)
		}

		if tc.expectZero {
			if len(body) != 0 {
				t.Fatalf("expected empty body for %s, got %q", tc.name, string(body))
			}
			continue
		}

		if len(body) == 0 {
			t.Fatalf("expected non-empty body for %s", tc.name)
		}
	}
}

func TestTLVFrameLayout(t *testing.T) {
	body := []byte(`{"alias":"demo"}`)
	frame := make([]byte, 5+len(body))
	frame[0] = MsgLogin
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)))
	copy(frame[5:], body)

	msgType, gotBody, err := ReadMsg(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("ReadMsg failed: %v", err)
	}

	if msgType != MsgLogin {
		t.Fatalf("msg type mismatch: got %q, want %q", msgType, MsgLogin)
	}

	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body mismatch: got %q, want %q", string(gotBody), string(body))
	}
}

func TestEmptyBody(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, MsgReqWorkConn, ReqWorkConnBody{}); err != nil {
		t.Fatalf("WriteMsg failed: %v", err)
	}

	raw := buf.Bytes()
	if len(raw) != 5 {
		t.Fatalf("frame length mismatch: got %d, want 5", len(raw))
	}

	if raw[0] != MsgReqWorkConn {
		t.Fatalf("msg type mismatch: got %q, want %q", raw[0], MsgReqWorkConn)
	}

	if gotLen := binary.BigEndian.Uint32(raw[1:5]); gotLen != 0 {
		t.Fatalf("body length mismatch: got %d, want 0", gotLen)
	}

	msgType, body, err := ReadMsg(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMsg failed: %v", err)
	}

	if msgType != MsgReqWorkConn {
		t.Fatalf("msg type mismatch after read: got %q, want %q", msgType, MsgReqWorkConn)
	}

	if len(body) != 0 {
		t.Fatalf("expected empty body, got %q", string(body))
	}
}

func TestExtractHost(t *testing.T) {
	testCases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "host without port",
			raw:  "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n",
			want: "example.com",
		},
		{
			name: "host with port",
			raw:  "GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n",
			want: "example.com",
		},
		{
			name: "case insensitive header",
			raw:  "GET / HTTP/1.1\r\nhost: EXAMPLE.COM\r\n\r\n",
			want: "EXAMPLE.COM",
		},
		{
			name: "missing host header",
			raw:  "GET / HTTP/1.1\r\n\r\n",
			want: "",
		},
		{
			name: "ipv6 literal host",
			raw:  "GET / HTTP/1.1\r\nHost: [::1]:8080\r\n\r\n",
			want: "[::1]:8080",
		},
	}

	for _, tc := range testCases {
		got := ExtractHost([]byte(tc.raw))
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestGenerateID(t *testing.T) {
	id := GenerateID()

	if len(id) != 12 {
		t.Fatalf("id length mismatch: got %d, want 12", len(id))
	}

	for i := 0; i < len(id); i++ {
		ch := id[i]
		isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')
		if !isHex {
			t.Fatalf("id has non-hex character %q at index %d", ch, i)
		}
	}
}

func TestPipe(t *testing.T) {
	data := bytes.Repeat([]byte("relay-data-"), 8000)
	src := bytes.NewReader(data)
	dst := &captureWriteCloser{}

	if err := Pipe(dst, src); err != nil {
		t.Fatalf("Pipe failed: %v", err)
	}

	if !dst.closed {
		t.Fatal("destination was not closed")
	}

	if !bytes.Equal(dst.Bytes(), data) {
		t.Fatalf("copied data mismatch: got %d bytes, want %d bytes", dst.Len(), len(data))
	}
}

func TestReadMsgEOF(t *testing.T) {
	_, _, err := ReadMsg(bytes.NewReader(nil))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

type captureWriteCloser struct {
	bytes.Buffer
	closed bool
}

func (c *captureWriteCloser) Close() error {
	c.closed = true
	return nil
}
