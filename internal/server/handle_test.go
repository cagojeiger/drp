package server

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
)

// helper: 메시지를 conn에 쓰기
func writeMsg(t *testing.T, conn net.Conn, m msg.Message) {
	t.Helper()
	if err := msg.WriteMsg(conn, m); err != nil {
		t.Fatalf("writeMsg: %v", err)
	}
}

// helper: conn에서 메시지 읽기
func readMsg(t *testing.T, conn net.Conn) msg.Message {
	t.Helper()
	m, err := msg.ReadMsg(conn)
	if err != nil {
		t.Fatalf("readMsg: %v", err)
	}
	return m
}

// helper: conn에서 raw 바이트로 LoginResp 읽기 (타입 바이트 검증용)
func readRawResp(t *testing.T, r io.Reader) (byte, json.RawMessage) {
	t.Helper()
	var typeBuf [1]byte
	if _, err := io.ReadFull(r, typeBuf[:]); err != nil {
		t.Fatalf("read type: %v", err)
	}
	var length int64
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		t.Fatalf("read length: %v", err)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return typeBuf[0], body
}

func TestHandleConnectionLogin(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}

	go h.HandleConnection(serverConn)

	// frpc → Login
	login := &msg.Login{
		Version:      "0.68.0",
		User:         "admin",
		PrivilegeKey: buildTestKey("test-token", 100),
		Timestamp:    100,
		RunID:        "run-1",
		PoolCount:    1,
	}
	writeMsg(t, clientConn, login)

	// drps → LoginResp
	resp := readMsg(t, clientConn)
	loginResp, ok := resp.(*msg.LoginResp)
	if !ok {
		t.Fatalf("expected *LoginResp, got %T", resp)
	}
	if loginResp.Error != "" {
		t.Errorf("login should succeed, got error: %s", loginResp.Error)
	}
	if loginResp.RunID == "" {
		t.Error("RunID should not be empty")
	}
}

func TestHandleConnectionLoginWrongToken(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "correct-token"}

	go h.HandleConnection(serverConn)

	// frpc → 잘못된 토큰
	login := &msg.Login{
		Version:      "0.68.0",
		PrivilegeKey: buildTestKey("wrong-token", 100),
		Timestamp:    100,
		RunID:        "run-1",
	}
	writeMsg(t, clientConn, login)

	// drps → LoginResp with error
	resp := readMsg(t, clientConn)
	loginResp, ok := resp.(*msg.LoginResp)
	if !ok {
		t.Fatalf("expected *LoginResp, got %T", resp)
	}
	if loginResp.Error == "" {
		t.Error("login should fail with wrong token")
	}

	// 연결이 닫혀야 함
	clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Error("connection should be closed after auth failure")
	}
}

func TestHandleConnectionNewWorkConn(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	workConnReceived := make(chan net.Conn, 1)
	h := &Handler{
		Token: "test-token",
		OnWorkConn: func(conn net.Conn, m *msg.NewWorkConn) {
			workConnReceived <- conn
		},
	}

	go h.HandleConnection(serverConn)

	// frpc → NewWorkConn (Login이 아닌 첫 메시지)
	writeMsg(t, clientConn, &msg.NewWorkConn{RunID: "run-1"})

	select {
	case <-workConnReceived:
		// 성공
	case <-time.After(time.Second):
		t.Error("OnWorkConn should be called")
	}
}

func TestHandleConnectionUnknownFirstMsg(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	h := &Handler{Token: "test-token"}

	go h.HandleConnection(serverConn)

	// frpc → Ping (첫 메시지로 올 수 없음)
	writeMsg(t, clientConn, &msg.Ping{})

	// 연결이 닫혀야 함
	clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := clientConn.Read(buf)
	if err == nil {
		t.Error("connection should be closed for unexpected first message")
	}
}

func TestHandleConnectionReadTimeout(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	h := &Handler{
		Token:       "test-token",
		ReadTimeout: 100 * time.Millisecond,
	}

	done := make(chan struct{})
	go func() {
		h.HandleConnection(serverConn)
		close(done)
	}()

	// 아무것도 안 보냄 → 타임아웃으로 닫혀야 함
	select {
	case <-done:
		// 성공 — 타임아웃으로 종료됨
	case <-time.After(time.Second):
		t.Error("should timeout and close")
	}
}

// helper
func buildTestKey(token string, timestamp int64) string {
	raw := fmt.Sprintf("%s%d", token, timestamp)
	sum := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", sum)
}
