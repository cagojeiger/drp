package msg

import (
	"bytes"
	"io"
	"testing"
)

func BenchmarkWriteMsg(b *testing.B) {
	w := io.Discard
	m := &Ping{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		WriteMsg(w, m)
	}
}

func BenchmarkReadMsg(b *testing.B) {
	// pre-encode a message
	var buf bytes.Buffer
	WriteMsg(&buf, &Ping{})
	data := buf.Bytes()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		ReadMsg(r)
	}
}

func BenchmarkTypeOf(b *testing.B) {
	m := &Login{}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		TypeOf(m)
	}
}

func BenchmarkMsgRoundTrip(b *testing.B) {
	m := &NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"test.example.com"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		WriteMsg(&buf, m)
		ReadMsg(&buf)
	}
}
