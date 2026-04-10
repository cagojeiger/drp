package main

import (
	"io"
	"log"
	"net/http"

	"golang.org/x/net/websocket"
)

func main() {
	wsServer := websocket.Server{
		Handler: func(ws *websocket.Conn) {
			io.Copy(ws, ws)
		},
		Handshake: func(config *websocket.Config, req *http.Request) error {
			return nil // accept all origins
		},
	}
	http.Handle("/ws", wsServer)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ws-echo ok"))
	})
	log.Fatal(http.ListenAndServe(":9090", nil))
}
