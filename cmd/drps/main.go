// drps is the server daemon: it listens on cfg.FrpcAddr for frpc control
// streams, multiplexes them with yamux, and serves the resulting HTTP
// traffic on cfg.HTTPAddr. This file stays a thin orchestrator — every
// non-trivial behavior lives behind a named helper so main() reads as a
// sequence of intents instead of a wall of setup.
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

const httpReadHeaderTimeout = 60 * time.Second

func main() {
	cfg := config.Load()
	debug := os.Getenv("DRPS_DEBUG") == "1"

	stack := buildServerStack(cfg, debug)
	mux := buildHTTPMux(stack, cfg)

	frpcLn, err := net.Listen("tcp", cfg.FrpcAddr)
	if err != nil {
		log.Fatalf("frpc listen: %v", err)
	}
	log.Printf("drps listening on %s (frpc), %s (http)", cfg.FrpcAddr, cfg.HTTPAddr)

	go runFrpcAccept(frpcLn, stack.handler, cfg)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}
	log.Fatal(srv.ListenAndServe())
}

// serverStack bundles every live object the drps process owns: the HTTP
// router, the per-session pool registry, the server.Handler that runs on
// each frpc control stream, the ReqWorkConnStats shared with the metrics
// endpoint, and the HTTP proxy handler that serves vhost traffic.
type serverStack struct {
	router       *router.Router
	registry     *pool.Registry
	handler      *server.Handler
	reqStats     *server.ReqWorkConnStats
	proxyHandler *proxy.Handler
}

// buildServerStack wires every runtime component together. Splitting it
// out of main() makes the dependency shape explicit and gives tests a
// single entry point if we ever want to spin drps up in-process.
func buildServerStack(cfg *config.Config, debug bool) *serverStack {
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
	if cfg.ResponseTimeoutSec > 0 {
		proxyHandler.ResponseTimeout = time.Duration(cfg.ResponseTimeoutSec) * time.Second
	}

	return &serverStack{
		router:       rt,
		registry:     registry,
		handler:      h,
		reqStats:     reqStats,
		proxyHandler: proxyHandler,
	}
}

// buildHTTPMux assembles the HTTP mux: internal /__drps/metrics endpoint,
// optional pprof tree when DRPS_PPROF=1, and the catch-all proxy handler.
// The catch-all is registered last so explicit prefixes take precedence.
func buildHTTPMux(s *serverStack, cfg *config.Config) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/__drps/metrics", server.MetricsHandler(s.reqStats, s.registry.AggregateStats))
	if os.Getenv("DRPS_PPROF") == "1" {
		registerPprofEndpoints(mux)
		log.Printf("pprof enabled on %s/debug/pprof/", cfg.HTTPAddr)
	}
	mux.Handle("/", s.proxyHandler)
	return mux
}

// registerPprofEndpoints wires the net/http/pprof handlers onto mux. They
// are opt-in (DRPS_PPROF=1) so production builds do not expose a profiler
// surface by default.
func registerPprofEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// runFrpcAccept blocks on ln.Accept() forever, spawning a yamux handler
// goroutine per incoming connection. Called in its own goroutine so the
// main goroutine can continue to srv.ListenAndServe.
func runFrpcAccept(ln net.Listener, h *server.Handler, cfg *config.Config) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleFrpcConnection(conn, h, cfg)
	}
}

// handleFrpcConnection wraps one incoming frpc TCP connection in a yamux
// server session and dispatches every accepted stream to the server
// Handler. Yamux parameters mirror frps so our window-update frame
// cadence matches theirs for wire-compat benchmarking.
func handleFrpcConnection(conn net.Conn, h *server.Handler, appCfg *config.Config) {
	session, err := openYamuxSession(conn, appCfg)
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

// openYamuxSession configures a yamux server from the app config and
// returns it. Extracted so handleFrpcConnection reads as a loop body
// rather than a setup block followed by a loop.
func openYamuxSession(conn net.Conn, appCfg *config.Config) (*yamux.Session, error) {
	ycfg := yamux.DefaultConfig()
	ycfg.LogOutput = io.Discard
	// Match frps's stream-window size so window-update frame cadence stays
	// consistent across the two implementations (important for benchmarks
	// and for anyone comparing packet traces).
	ycfg.MaxStreamWindowSize = uint32(appCfg.YamuxMaxStreamWindow)
	ycfg.AcceptBacklog = appCfg.YamuxAcceptBacklog
	ycfg.EnableKeepAlive = appCfg.YamuxEnableKeepAlive
	ycfg.KeepAliveInterval = time.Duration(appCfg.YamuxKeepAliveSeconds) * time.Second
	ycfg.ConnectionWriteTimeout = time.Duration(appCfg.YamuxWriteTimeoutSec) * time.Second
	return yamux.Server(conn, ycfg)
}
