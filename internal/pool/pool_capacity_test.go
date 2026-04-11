package pool

import (
	"net"
	"testing"
	"time"
)

func TestNewWithCapacity(t *testing.T) {
	p := New(func() {}, 2)
	s1, c1 := net.Pipe()
	defer c1.Close()
	s2, c2 := net.Pipe()
	defer c2.Close()

	p.Put(s1)
	p.Put(s2)

	if _, err := p.Get(time.Second); err != nil {
		t.Fatalf("first get: %v", err)
	}
	if _, err := p.Get(time.Second); err != nil {
		t.Fatalf("second get: %v", err)
	}
}

func TestPutOverflow(t *testing.T) {
	p := New(func() {}, 1)
	s1, c1 := net.Pipe()
	defer c1.Close()
	s2, c2 := net.Pipe()

	p.Put(s1)
	p.Put(s2) // overflow -> should close s2 side

	if _, err := c2.Write([]byte("x")); err == nil {
		t.Fatal("expected overflow connection to be closed")
	}
}
