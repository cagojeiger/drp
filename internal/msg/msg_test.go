package msg

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// frp 와이어 프로토콜: [1byte type] [8byte BE length] [N byte JSON]

func TestTypeBytes(t *testing.T) {
	// frp 프로토콜에서 정의된 타입 바이트 매핑
	tests := []struct {
		msg      Message
		wantType byte
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
			t.Errorf("TypeOf(%T) returned not ok", tt.msg)
			continue
		}
		if got != tt.wantType {
			t.Errorf("TypeOf(%T) = %q, want %q", tt.msg, got, tt.wantType)
		}
	}
}

func TestWriteMsg(t *testing.T) {
	login := &Login{
		Version:      "0.68.0",
		User:         "admin",
		PrivilegeKey: "abc123",
		Timestamp:    1711785600,
		RunID:        "test-run-id",
		PoolCount:    1,
	}

	var buf bytes.Buffer
	err := WriteMsg(&buf, login)
	if err != nil {
		t.Fatalf("WriteMsg: %v", err)
	}

	data := buf.Bytes()

	// 첫 바이트: 타입
	if data[0] != 'o' {
		t.Errorf("type byte = %q, want %q", data[0], byte('o'))
	}

	// 다음 8바이트: JSON 길이 (big-endian int64)
	jsonLen := int64(binary.BigEndian.Uint64(data[1:9]))

	// 나머지: JSON 본문
	jsonBody := data[9:]
	if int64(len(jsonBody)) != jsonLen {
		t.Errorf("json body len = %d, want %d", len(jsonBody), jsonLen)
	}

	// JSON 파싱 가능한지 확인
	var parsed Login
	if err := json.Unmarshal(jsonBody, &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if parsed.Version != "0.68.0" {
		t.Errorf("Version = %q, want %q", parsed.Version, "0.68.0")
	}
	if parsed.User != "admin" {
		t.Errorf("User = %q, want %q", parsed.User, "admin")
	}
}

func TestReadMsg(t *testing.T) {
	// 수동으로 와이어 포맷 구성
	login := Login{
		Version: "0.68.0",
		RunID:   "test-123",
	}
	jsonData, _ := json.Marshal(login)

	var buf bytes.Buffer
	buf.WriteByte('o')
	binary.Write(&buf, binary.BigEndian, int64(len(jsonData)))
	buf.Write(jsonData)

	msg, err := ReadMsg(&buf)
	if err != nil {
		t.Fatalf("ReadMsg: %v", err)
	}

	got, ok := msg.(*Login)
	if !ok {
		t.Fatalf("ReadMsg returned %T, want *Login", msg)
	}
	if got.Version != "0.68.0" {
		t.Errorf("Version = %q, want %q", got.Version, "0.68.0")
	}
	if got.RunID != "test-123" {
		t.Errorf("RunID = %q, want %q", got.RunID, "test-123")
	}
}

func TestRoundTrip(t *testing.T) {
	// 모든 메시지 타입에 대해 Write → Read 라운드트립
	messages := []Message{
		&Login{Version: "0.68.0", Hostname: "myhost", Os: "linux", Arch: "amd64", User: "admin", PrivilegeKey: "key", Timestamp: 100, RunID: "r1", PoolCount: 2},
		&LoginResp{Version: "drps-0.1", RunID: "r1", Error: ""},
		&NewProxy{ProxyName: "web", ProxyType: "http", UseEncryption: true, CustomDomains: []string{"a.com", "b.com"}, HTTPUser: "u", HTTPPwd: "p"},
		&NewProxyResp{ProxyName: "web", RemoteAddr: ":80", Error: ""},
		&CloseProxy{ProxyName: "web"},
		&ReqWorkConn{},
		&NewWorkConn{RunID: "r1", PrivilegeKey: "pk", Timestamp: 200},
		&StartWorkConn{ProxyName: "web", SrcAddr: "1.2.3.4", DstAddr: "5.6.7.8", SrcPort: 12345, DstPort: 80},
		&Ping{PrivilegeKey: "pk", Timestamp: 300},
		&Pong{Error: ""},
	}

	for _, orig := range messages {
		var buf bytes.Buffer
		if err := WriteMsg(&buf, orig); err != nil {
			t.Errorf("WriteMsg(%T): %v", orig, err)
			continue
		}

		decoded, err := ReadMsg(&buf)
		if err != nil {
			t.Errorf("ReadMsg for %T: %v", orig, err)
			continue
		}

		// JSON 비교
		origJSON, _ := json.Marshal(orig)
		decodedJSON, _ := json.Marshal(decoded)
		if !bytes.Equal(origJSON, decodedJSON) {
			t.Errorf("round-trip %T mismatch:\n  orig:    %s\n  decoded: %s", orig, origJSON, decodedJSON)
		}
	}
}

func TestReadMsgUnknownType(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteByte(0xFF) // 알 수 없는 타입
	binary.Write(&buf, binary.BigEndian, int64(2))
	buf.WriteString("{}")

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Error("ReadMsg with unknown type should return error")
	}
}

func TestReadMsgMaxSize(t *testing.T) {
	// frp 프로토콜 최대 메시지 크기: 10240 bytes
	var buf bytes.Buffer
	buf.WriteByte('h') // Ping
	binary.Write(&buf, binary.BigEndian, int64(10241)) // 초과
	buf.Write(make([]byte, 10241))

	_, err := ReadMsg(&buf)
	if err == nil {
		t.Error("ReadMsg with oversized body should return error")
	}
}
