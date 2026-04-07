package main

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
	"github.com/kangheeyong/drp/internal/config"
	"github.com/kangheeyong/drp/internal/crypto"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/proxy"
	"github.com/kangheeyong/drp/internal/router"
	"github.com/kangheeyong/drp/internal/server"
)

func main() {
	cfg := config.Load()

	rt := router.New()
	registry := pool.NewRegistry()
	aesKey := crypto.DeriveKey(cfg.Token)

	h := &server.Handler{
		Token:  cfg.Token,
		Router: rt,
		OnControlClose: func(runID string) {
			log.Printf("control closed: runID=%s", runID)
			registry.Remove(runID)
		},
	}
	h.OnWorkConn = func(conn net.Conn, m *msg.NewWorkConn) {
		log.Printf("work conn: runID=%s", m.RunID)
		p := registry.GetOrCreate(m.RunID, h.ReqWorkConnFunc(m.RunID), cfg.PoolCapacity)
		p.Put(conn)
	}

	proxyHandler := proxy.NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		return registry.Get(runID)
	}, aesKey)

	// frpc 리스너
	frpcLn, err := net.Listen("tcp", cfg.FrpcAddr)
	if err != nil {
		log.Fatalf("frpc listen: %v", err)
	}
	log.Printf("drps listening on %s (frpc), %s (http)", cfg.FrpcAddr, cfg.HTTPAddr)

	go func() {
		for {
			conn, err := frpcLn.Accept()
			if err != nil {
				log.Printf("accept: %v", err)
				continue
			}
			go handleTCP(conn, h)
		}
	}()

	log.Fatal(http.ListenAndServe(cfg.HTTPAddr, proxyHandler))
}

func handleTCP(conn net.Conn, h *server.Handler) {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard

	session, err := yamux.Server(conn, cfg)
	if err != nil {
		log.Printf("yamux: %v", err)
		conn.Close()
		return
	}

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		go h.HandleConnection(stream)
	}
}
