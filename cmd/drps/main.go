package main

import (
	"io"
	"log"
	"net"
	"net/http"

	"github.com/hashicorp/yamux"
	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/proxy"
	"github.com/kangheeyong/drp/internal/router"
	"github.com/kangheeyong/drp/internal/server"
)

func main() {
	token := "test-token"
	frpcAddr := ":17000"
	httpAddr := ":18080"

	rt := router.New()
	registry := pool.NewRegistry()

	h := &server.Handler{
		Token:  token,
		Router: rt,
		OnControlClose: func(runID string) {
			log.Printf("control closed: runID=%s", runID)
			registry.Remove(runID)
		},
	}
	h.OnWorkConn = func(conn net.Conn, m *msg.NewWorkConn) {
		log.Printf("work conn: runID=%s", m.RunID)
		p := registry.GetOrCreate(m.RunID, h.ReqWorkConnFunc(m.RunID))
		p.Put(conn)
	}

	proxyHandler := proxy.NewHandler(rt, func(proxyName string) (*pool.Pool, bool) {
		// RouteConfig에서 RunID를 찾아서 해당 풀 반환
		// Router.Lookup은 이미 호출됐으므로, 여기서는 RunID로 찾아야 함
		// 간단한 방법: proxyName 대신 RunID 기반으로 찾기 위해
		// Router에서 RunID를 가져옴
		var runID string
		rt.RangeByProxy(proxyName, func(cfg *router.RouteConfig) {
			runID = cfg.RunID
		})
		if runID == "" {
			return nil, false
		}
		return registry.Get(runID)
	}, token)

	// frpc 리스너
	frpcLn, err := net.Listen("tcp", frpcAddr)
	if err != nil {
		log.Fatalf("frpc listen: %v", err)
	}
	log.Printf("drps listening on %s (frpc), %s (http)", frpcAddr, httpAddr)

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

	log.Fatal(http.ListenAndServe(httpAddr, proxyHandler))
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
