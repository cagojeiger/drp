package server

import (
	"net"
	"testing"
	"time"

	"github.com/kangheeyong/drp/internal/msg"
	"github.com/kangheeyong/drp/internal/router"
)

func TestCloseProxyMapRemoval(t *testing.T) {
	TestRouterIntegrationCloseProxy(t)
}

func TestMultipleCloseProxyIdempotent(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	rt := router.New()
	h := &Handler{Token: "test-token", Router: rt}
	go h.HandleConnection(serverConn)

	reader, writer := frpcLogin(t, clientConn, "test-token", "run-1", 0)
	_ = msg.WriteMsg(writer, &msg.NewProxy{
		ProxyName:     "web",
		ProxyType:     "http",
		CustomDomains: []string{"idem.test"},
	})
	_, _ = msg.ReadMsg(reader)

	_ = msg.WriteMsg(writer, &msg.CloseProxy{ProxyName: "web"})
	_ = msg.WriteMsg(writer, &msg.CloseProxy{ProxyName: "web"})
	time.Sleep(30 * time.Millisecond)

	if _, ok := rt.Lookup("idem.test", "/"); ok {
		t.Fatal("route should be removed")
	}
}
