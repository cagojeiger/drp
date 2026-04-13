package framework

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"golang.org/x/net/websocket"
)

// StartHTTPEcho starts an in-process HTTP server that returns a body >= 100
// bytes (satisfies http-get.yaml min_body_bytes: 100). Returns the listener
// port. The server shuts down when ctx is cancelled.
func StartHTTPEcho(ctx context.Context) (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("http-echo listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Body >= 100 bytes so min_body_bytes: 100 passes.
		body := `<!DOCTYPE html><html><head><title>http-echo</title></head><body><h1>http-echo ok</h1><p>drps compat test backend</p></body></html>`
		w.Write([]byte(body))
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	return port, nil
}

// StartWSEcho starts an in-process WebSocket echo server. Handler logic is
// identical to test/ws-echo/main.go: io.Copy(ws, ws). Returns the listener
// port. Shuts down when ctx is cancelled.
func StartWSEcho(ctx context.Context) (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("ws-echo listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	wsServer := websocket.Server{
		Handler: func(ws *websocket.Conn) {
			io.Copy(ws, ws)
		},
		Handshake: func(config *websocket.Config, req *http.Request) error {
			return nil // accept all origins
		},
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", wsServer)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ws-echo ok"))
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	return port, nil
}
