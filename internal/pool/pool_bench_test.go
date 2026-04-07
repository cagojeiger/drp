package pool

import (
	"fmt"
	"net"
	"testing"
)

func BenchmarkPoolGetPut(b *testing.B) {
	p := New(func() {})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c1, c2 := net.Pipe()
		p.Put(c1)
		conn, _ := p.Get(0)
		if conn != nil {
			conn.Close()
		}
		c2.Close()
	}
}

func BenchmarkRegistryGet(b *testing.B) {
	reg := NewRegistry()
	for i := 0; i < 100; i++ {
		reg.GetOrCreate(fmt.Sprintf("run-%d", i), func() {})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reg.Get("run-50")
	}
}
