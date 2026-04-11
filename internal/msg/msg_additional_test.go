package msg

import (
	"bytes"
	"encoding/binary"
	"testing"
)

type countWriter struct {
	writes int
	buf    bytes.Buffer
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

func TestWriteMsgSingleWrite(t *testing.T) {
	cw := &countWriter{}
	if err := WriteMsg(cw, &Ping{PrivilegeKey: "k", Timestamp: 1}); err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}
	if cw.writes != 1 {
		t.Fatalf("writes=%d, want 1", cw.writes)
	}
}

func TestTypeByteSwitchConsistency(t *testing.T) {
	tests := []struct {
		msg Message
		b   byte
	}{
		{&Login{}, 'o'},
		{&LoginResp{}, '1'},
		{&NewProxy{}, 'p'},
		{&NewProxyResp{}, '2'},
		{&CloseProxy{}, 'c'},
		{&ReqWorkConn{}, 'r'},
		{&NewWorkConn{}, 'w'},
		{&StartWorkConn{}, 's'},
		{&Ping{}, 'h'},
		{&Pong{}, '4'},
	}
	for _, tt := range tests {
		got, ok := TypeOf(tt.msg)
		if !ok {
			t.Fatalf("TypeOf(%T) returned !ok", tt.msg)
		}
		if got != tt.b {
			t.Fatalf("TypeOf(%T)=%q, want %q", tt.msg, got, tt.b)
		}
	}
}

func TestReadMsgRejectNegativeLength(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte('h')
	// int64(-1)와 동일한 비트패턴을 uint64로 기록
	var lenBuf [8]byte
	binary.BigEndian.PutUint64(lenBuf[:], ^uint64(0))
	buf.Write(lenBuf[:])
	buf.WriteString("{}")

	if _, err := ReadMsg(&buf); err == nil {
		t.Fatal("expected error for negative/invalid length")
	}
}
