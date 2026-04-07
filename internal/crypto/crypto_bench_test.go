package crypto

import (
	"bytes"
	"io"
	"testing"
)

func BenchmarkDeriveKey(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		DeriveKey("test-token")
	}
}

func BenchmarkCryptoRoundTrip(b *testing.B) {
	key := DeriveKey("test-token")
	data := bytes.Repeat([]byte("hello world "), 100) // 1.2KB

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		w, _ := NewCryptoWriter(&buf, key)
		w.Write(data)

		r, _ := NewCryptoReader(&buf, key)
		io.ReadAll(r)
	}
}

func BenchmarkSnappyRoundTrip(b *testing.B) {
	data := bytes.Repeat([]byte("hello world "), 100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		w := NewSnappyWriter(&buf)
		w.Write(data)
		w.Close()

		r := NewSnappyReader(&buf)
		io.ReadAll(r)
	}
}
