package crypto

import (
	"bytes"
	"io"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	// frp 스펙: PBKDF2(token, salt="frp", iter=64, keyLen=16, sha1)
	key := DeriveKey("my-token")

	if len(key) != 16 { // aes.BlockSize = 16
		t.Errorf("key length = %d, want 16", len(key))
	}

	// 같은 입력 → 같은 키
	key2 := DeriveKey("my-token")
	if !bytes.Equal(key, key2) {
		t.Error("DeriveKey should be deterministic")
	}

	// 다른 입력 → 다른 키
	key3 := DeriveKey("other-token")
	if bytes.Equal(key, key3) {
		t.Error("different tokens should produce different keys")
	}
}

func TestDeriveKeyEmptyToken(t *testing.T) {
	key := DeriveKey("")
	if len(key) != 16 {
		t.Errorf("key length = %d, want 16", len(key))
	}
}

func TestNewCryptoReadWriter(t *testing.T) {
	// 양방향 암호화 래퍼: 쓴 데이터를 읽으면 원본이 돌아와야 함
	key := DeriveKey("test-token")

	var buf bytes.Buffer
	writer, err := NewCryptoWriter(&buf, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}

	plaintext := []byte("hello frp protocol")
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// 버퍼에 있는 건 암호문이어야 함 (평문과 다름)
	ciphertext := buf.Bytes()
	if bytes.Equal(ciphertext, plaintext) {
		t.Error("ciphertext should differ from plaintext")
	}
}

func TestCryptoRoundTrip(t *testing.T) {
	key := DeriveKey("test-token")
	plaintext := []byte("hello frp protocol - round trip test")

	// 암호화
	var buf bytes.Buffer
	writer, err := NewCryptoWriter(&buf, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// 복호화
	reader, err := NewCryptoReader(&buf, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	decrypted, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip failed:\n  orig:      %q\n  decrypted: %q", plaintext, decrypted)
	}
}

func TestCryptoRoundTripMultipleWrites(t *testing.T) {
	key := DeriveKey("test-token")

	var buf bytes.Buffer
	writer, err := NewCryptoWriter(&buf, key)
	if err != nil {
		t.Fatalf("NewCryptoWriter: %v", err)
	}

	// 여러 번 쓰기
	chunks := []string{"first ", "second ", "third"}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// 한 번에 읽기
	reader, err := NewCryptoReader(&buf, key)
	if err != nil {
		t.Fatalf("NewCryptoReader: %v", err)
	}
	decrypted, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	want := "first second third"
	if string(decrypted) != want {
		t.Errorf("got %q, want %q", decrypted, want)
	}
}

func TestCryptoWrongKey(t *testing.T) {
	key1 := DeriveKey("token-1")
	key2 := DeriveKey("token-2")
	plaintext := []byte("secret message")

	// key1로 암호화
	var buf bytes.Buffer
	writer, _ := NewCryptoWriter(&buf, key1)
	writer.Write(plaintext)

	// key2로 복호화 → 원본과 달라야 함
	reader, err := NewCryptoReader(&buf, key2)
	if err != nil {
		// IV 파싱 단계에서 에러 날 수도 있음 — 이것도 올바른 동작
		return
	}
	decrypted, err := io.ReadAll(reader)
	if err != nil {
		return // 복호화 중 에러도 올바른 동작
	}

	if bytes.Equal(decrypted, plaintext) {
		t.Error("wrong key should not produce correct plaintext")
	}
}

func TestCryptoLargeData(t *testing.T) {
	key := DeriveKey("test-token")

	// 1MB 데이터
	plaintext := make([]byte, 1<<20)
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	var buf bytes.Buffer
	writer, _ := NewCryptoWriter(&buf, key)
	writer.Write(plaintext)

	reader, _ := NewCryptoReader(&buf, key)
	decrypted, _ := io.ReadAll(reader)

	if !bytes.Equal(decrypted, plaintext) {
		t.Error("large data round-trip failed")
	}
}
