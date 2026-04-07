package auth

import (
	"crypto/md5"
	"fmt"
	"testing"
)

func TestBuildAuthKey(t *testing.T) {
	// frp 스펙: MD5(token + timestamp)
	token := "my-secret-token"
	timestamp := int64(1711785600)

	got := BuildAuthKey(token, timestamp)

	// 직접 계산한 기대값
	raw := fmt.Sprintf("%s%d", token, timestamp)
	want := fmt.Sprintf("%x", md5.Sum([]byte(raw)))

	if got != want {
		t.Errorf("BuildAuthKey(%q, %d) = %q, want %q", token, timestamp, got, want)
	}
}

func TestBuildAuthKeyEmptyToken(t *testing.T) {
	got := BuildAuthKey("", 0)
	raw := fmt.Sprintf("%s%d", "", 0)
	want := fmt.Sprintf("%x", md5.Sum([]byte(raw)))

	if got != want {
		t.Errorf("BuildAuthKey empty = %q, want %q", got, want)
	}
}

func TestVerifyAuth(t *testing.T) {
	token := "server-token"
	timestamp := int64(1711785600)
	clientKey := BuildAuthKey(token, timestamp)

	if !VerifyAuth(token, timestamp, clientKey) {
		t.Error("VerifyAuth should return true for valid key")
	}
}

func TestVerifyAuthWrongToken(t *testing.T) {
	timestamp := int64(1711785600)
	clientKey := BuildAuthKey("correct-token", timestamp)

	if VerifyAuth("wrong-token", timestamp, clientKey) {
		t.Error("VerifyAuth should return false for wrong token")
	}
}

func TestVerifyAuthWrongTimestamp(t *testing.T) {
	token := "server-token"
	clientKey := BuildAuthKey(token, 100)

	if VerifyAuth(token, 200, clientKey) {
		t.Error("VerifyAuth should return false for wrong timestamp")
	}
}

func TestVerifyAuthTimingAttack(t *testing.T) {
	// VerifyAuth는 constant-time 비교를 사용해야 함
	// 직접적인 타이밍 테스트는 어렵지만, 최소한 동작은 확인
	token := "server-token"
	timestamp := int64(100)
	clientKey := BuildAuthKey(token, timestamp)

	// 맞는 키의 첫 글자만 바꿔도 false
	wrongKey := "0" + clientKey[1:]
	if VerifyAuth(token, timestamp, wrongKey) {
		t.Error("VerifyAuth should return false for tampered key")
	}
}
