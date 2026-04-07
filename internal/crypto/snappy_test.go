package crypto

import (
	"bytes"
	"io"
	"testing"
)

func TestSnappyRoundTrip(t *testing.T) {
	plaintext := []byte("hello frp snappy compression test")

	var buf bytes.Buffer
	writer := NewSnappyWriter(&buf)
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reader := NewSnappyReader(&buf)
	decrypted, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip failed:\n  orig: %q\n  got:  %q", plaintext, decrypted)
	}
}

func TestSnappyCompressed(t *testing.T) {
	// 반복 데이터 → 압축 후 더 작아야 함
	plaintext := bytes.Repeat([]byte("abcdefghij"), 1000) // 10KB

	var buf bytes.Buffer
	writer := NewSnappyWriter(&buf)
	writer.Write(plaintext)
	writer.Close()

	if buf.Len() >= len(plaintext) {
		t.Errorf("compressed size %d >= original %d", buf.Len(), len(plaintext))
	}
}

func TestSnappyMultipleWrites(t *testing.T) {
	chunks := []string{"first ", "second ", "third"}

	var buf bytes.Buffer
	writer := NewSnappyWriter(&buf)
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	writer.Close()

	reader := NewSnappyReader(&buf)
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	want := "first second third"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSnappyLargeData(t *testing.T) {
	plaintext := make([]byte, 1<<20) // 1MB
	for i := range plaintext {
		plaintext[i] = byte(i % 256)
	}

	var buf bytes.Buffer
	writer := NewSnappyWriter(&buf)
	writer.Write(plaintext)
	writer.Close()

	reader := NewSnappyReader(&buf)
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Error("large data round-trip failed")
	}
}

func TestSnappyEmpty(t *testing.T) {
	var buf bytes.Buffer
	writer := NewSnappyWriter(&buf)
	writer.Close()

	reader := NewSnappyReader(&buf)
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d bytes", len(got))
	}
}
