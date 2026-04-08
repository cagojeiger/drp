package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
)

func TestProxyPoolLookupByRunID(t *testing.T) {
	rt := router.New()
	_ = rt.Add(&router.RouteConfig{
		Domain:    "lookup.test",
		Location:  "/",
		ProxyName: "web",
		RunID:     "run-lookup",
	})

	drpsConn, frpcConn := net.Pipe()
	defer frpcConn.Close()
	p := pool.New(func() {})
	p.Put(drpsConn)

	var gotRunID string
	h := NewHandler(rt, func(runID string) (*pool.Pool, bool) {
		gotRunID = runID
		return p, true
	}, testAESKey)

	go fakeFrpc(t, frpcConn, "ok")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "lookup.test"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d, want 200", w.Code)
	}
	if gotRunID != "run-lookup" {
		t.Fatalf("pool lookup runID=%q, want %q", gotRunID, "run-lookup")
	}
}
