package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"

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
	debug := os.Getenv("DRPS_DEBUG") == "1"

	rt := router.New()
	registry := pool.NewRegistry()
	aesKey := crypto.DeriveKey(cfg.Token)
	reqStats := &server.ReqWorkConnStats{}

	h := &server.Handler{
		Token:    cfg.Token,
		Router:   rt,
		ReqStats: reqStats,
		OnControlClose: func(runID string) {
			if debug {
				log.Printf("control closed: runID=%s", runID)
			}
			registry.Remove(runID)
		},
	}
	h.OnWorkConn = func(conn net.Conn, m *msg.NewWorkConn) {
		if debug {
			log.Printf("work conn: runID=%s", m.RunID)
		}
		p := registry.GetOrCreate(m.RunID, h.ReqWorkConnFunc(m.RunID), cfg.PoolCapacity)
		p.Put(conn)
	}

	proxyHandler := proxy.NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		return registry.Get(runID)
	}, aesKey)
	mux := http.NewServeMux()
	mux.HandleFunc("/__drps/metrics", server.MetricsHandler(reqStats, registry.AggregateStats))
	if os.Getenv("DRPS_PPROF") == "1" {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		log.Printf("pprof enabled on %s/debug/pprof/", cfg.HTTPAddr)
	}
	mux.Handle("/", proxyHandler)

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
			go handleTCP(conn, h, cfg)
		}
	}()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func handleTCP(conn net.Conn, h *server.Handler, appCfg *config.Config) {
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	// frps와 동일하게 스트림 윈도우를 크게 잡아 window-update 프레임 빈도를 낮춘다.
	ycfg.MaxStreamWindowSize = uint32(appCfg.YamuxMaxStreamWindow)
	ycfg.AcceptBacklog = appCfg.YamuxAcceptBacklog
	ycfg.EnableKeepAlive = appCfg.YamuxEnableKeepAlive
	ycfg.KeepAliveInterval = time.Duration(appCfg.YamuxKeepAliveSeconds) * time.Second
	ycfg.ConnectionWriteTimeout = time.Duration(appCfg.YamuxWriteTimeoutSec) * time.Second

	session, err := yamux.Server(conn, ycfg)
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
