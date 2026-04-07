package pool

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestPutAndGet(t *testing.T) {
	reqCount := atomic.Int32{}
	p := New(func() { reqCount.Add(1) })

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	p.Put(serverConn)

	got, err := p.Get(time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != serverConn {
		t.Error("should return the same connection")
	}

	// Get 성공 후 eager refill: RequestConn 호출됨
	time.Sleep(10 * time.Millisecond)
	if reqCount.Load() != 1 {
		t.Errorf("RequestConn called %d times, want 1", reqCount.Load())
	}
}

func TestGetEmpty(t *testing.T) {
	reqCount := atomic.Int32{}
	p := New(func() { reqCount.Add(1) })

	// 풀 비어있음 → RequestConn 호출 후 대기
	go func() {
		time.Sleep(50 * time.Millisecond)
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		p.Put(serverConn)
	}()

	got, err := p.Get(time.Second)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Error("should return a connection")
	}

	// 풀 비어있을 때 RequestConn 호출됨
	if reqCount.Load() < 1 {
		t.Error("RequestConn should be called when pool is empty")
	}
}

func TestGetTimeout(t *testing.T) {
	p := New(func() {})

	// 아무것도 Put 안 함 → 타임아웃
	_, err := p.Get(100 * time.Millisecond)
	if err == nil {
		t.Error("Get should timeout")
	}
}

func TestClose(t *testing.T) {
	p := New(func() {})

	s1, c1 := net.Pipe()
	s2, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	p.Put(s1)
	p.Put(s2)
	p.Close()

	// Close 후 Get → 에러
	_, err := p.Get(100 * time.Millisecond)
	if err == nil {
		t.Error("Get after Close should fail")
	}

	// Close 후 Put → 무시 (패닉 없어야 함)
	s3, c3 := net.Pipe()
	defer c3.Close()
	p.Put(s3) // should not panic
}

func TestConcurrentGetPut(t *testing.T) {
	p := New(func() {})

	done := make(chan struct{})
	// 10개 동시 Get
	for range 10 {
		go func() {
			conn, err := p.Get(time.Second)
			if err != nil {
				return
			}
			conn.Close()
			done <- struct{}{}
		}()
	}

	// 10개 Put
	for range 10 {
		s, c := net.Pipe()
		defer c.Close()
		p.Put(s)
	}

	for range 10 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for concurrent Get")
		}
	}
}

func TestGetMultipleEagerRefill(t *testing.T) {
	reqCount := atomic.Int32{}
	p := New(func() { reqCount.Add(1) })

	// 3개 Put
	for range 3 {
		s, c := net.Pipe()
		defer c.Close()
		p.Put(s)
	}

	// 3개 Get → 각각 eager refill
	for range 3 {
		conn, err := p.Get(time.Second)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		conn.Close()
	}

	time.Sleep(50 * time.Millisecond)
	if reqCount.Load() != 3 {
		t.Errorf("RequestConn called %d times, want 3", reqCount.Load())
	}
}
