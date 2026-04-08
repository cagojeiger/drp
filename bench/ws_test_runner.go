package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	port := "18080"
	count := 500
	concurrency := 10

	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	if len(os.Args) > 2 {
		count, _ = strconv.Atoi(os.Args[2])
	}
	if len(os.Args) > 3 {
		concurrency, _ = strconv.Atoi(os.Args[3])
	}

	var (
		success atomic.Int64
		fail    atomic.Int64
		totalNs atomic.Int64
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	start := time.Now()

	for i := 0; i < count; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			t0 := time.Now()
			err := doWS(port)
			elapsed := time.Since(t0)

			if err != nil {
				fail.Add(1)
			} else {
				success.Add(1)
				totalNs.Add(int64(elapsed))
			}
		}()
	}

	wg.Wait()
	wall := time.Since(start)

	s := success.Load()
	f := fail.Load()
	var avgMs float64
	if s > 0 {
		avgMs = float64(totalNs.Load()) / float64(s) / 1e6
	}

	fmt.Printf("  WebSocket Results:\n")
	fmt.Printf("    Total:       %d\n", count)
	fmt.Printf("    Success:     %d\n", s)
	fmt.Printf("    Failed:      %d\n", f)
	fmt.Printf("    Wall time:   %.3fs\n", wall.Seconds())
	fmt.Printf("    RPS:         %.1f conn/s\n", float64(s)/wall.Seconds())
	fmt.Printf("    Avg latency: %.2fms\n", avgMs)
}

func doWS(port string) error {
	conn, err := net.DialTimeout("tcp", "localhost:"+port, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// HTTP upgrade request
	upgrade := "GET /ws HTTP/1.1\r\n" +
		"Host: ws.local\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	if _, err := conn.Write([]byte(upgrade)); err != nil {
		return fmt.Errorf("write upgrade: %w", err)
	}

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	resp := string(buf[:n])
	if !strings.Contains(resp, "101") {
		return fmt.Errorf("not 101: %.60s", resp)
	}

	// Send text frame (masked, as per RFC 6455 client requirement)
	payload := []byte("hello")
	frame := make([]byte, 2+4+len(payload))
	frame[0] = 0x81 // FIN + text
	frame[1] = byte(len(payload)) | 0x80 // masked, length
	// mask key = [0,0,0,0] (simplest)
	copy(frame[6:], payload)
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	// Read echo response frame
	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read echo: %w", err)
	}
	if n < 2 {
		return fmt.Errorf("short frame: %d bytes", n)
	}
	// Verify it's a text frame with "hello"
	if buf[0] != 0x81 {
		return fmt.Errorf("unexpected opcode: 0x%02x", buf[0])
	}
	frameLen := int(buf[1] & 0x7f)
	if frameLen != len(payload) {
		return fmt.Errorf("payload length %d, want %d", frameLen, len(payload))
	}
	got := string(buf[2 : 2+frameLen])
	if got != "hello" {
		return fmt.Errorf("echo = %q, want %q", got, "hello")
	}

	return nil
}
