// Package proxy implements the HTTP data-path of drps. Every request that
// arrives on the vhost port is matched against the router, enriched with
// the matched RouteConfig via context, and handed to a stdlib
// httputil.ReverseProxy whose Transport dials work-connections out of the
// pool owned by the matching frpc session.
//
// This file intentionally keeps the whole pipeline in one place — Handler,
// the ReverseProxy/Transport builders, and the dial helpers — because they
// share non-trivial invariants that are easier to reason about side-by-side.
package proxy

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
	"github.com/kangheeyong/drp/internal/wrap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const (
	// workConnTimeout bounds how long dialWorkConn waits for the pool to
	// deliver an idle work-conn before giving up with a gateway error.
	workConnTimeout = 10 * time.Second

	// reverseProxyBufSize is the size of each pooled copy buffer used by
	// httputil.ReverseProxy. 32 KiB matches the stdlib default and is
	// large enough to absorb a TCP window without thrashing sync.Pool.
	reverseProxyBufSize = 32 * 1024

	// Transport tuning. Values match the pre-refactor NewHandler literal.
	transportResponseHeaderTimeout = 60 * time.Second
	transportIdleConnTimeout       = 60 * time.Second
	transportMaxIdleConnsPerHost   = 5
)

// routeCtxKey is the context key under which ServeHTTP stores the matched
// RouteConfig so the ReverseProxy hooks (Rewrite/ModifyResponse) and the
// Transport dialer can retrieve it without re-running the router.
type routeCtxKey struct{}

// PoolLookup returns the work-connection pool associated with a run ID, or
// (nil, false) if no such session exists.
type PoolLookup func(runID string) (*pool.Pool, bool)

// Handler is the HTTP handler that proxies requests through frpc work
// connections. It is created once per drps instance and shared across
// goroutines.
type Handler struct {
	router          *router.Router
	poolLookup      PoolLookup
	aesKey          []byte
	proxy           http.Handler
	WorkConnTimeout time.Duration
	ResponseTimeout time.Duration
}

// NewHandler wires up the HTTP pipeline: a ReverseProxy whose Transport
// dials out of the work-conn pool, wrapped in h2c so the same handler can
// serve cleartext HTTP/2 upgrades.
func NewHandler(rt *router.Router, lookup PoolLookup, aesKey []byte) *Handler {
	h := &Handler{
		router:          rt,
		poolLookup:      lookup,
		aesKey:          aesKey,
		WorkConnTimeout: workConnTimeout,
	}
	h.proxy = h2c.NewHandler(h.newReverseProxy(), &http2.Server{})
	return h
}

// ServeHTTP is the public entry point. It runs the router, applies any
// per-route basic-auth gate, injects the matched RouteConfig into ctx, and
// hands the request off to the ReverseProxy.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg, ok := h.router.Lookup(r.Host, r.URL.Path)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if cfg.HTTPUser != "" {
		user, pass, ok := r.BasicAuth()
		if !ok || user != cfg.HTTPUser || pass != cfg.HTTPPwd {
			w.Header().Set("WWW-Authenticate", `Basic realm="Authorization Required"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	ctx := context.WithValue(r.Context(), routeCtxKey{}, cfg)
	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// newReverseProxy constructs the httputil.ReverseProxy used by this
// Handler. Hooks are extracted as small closures/functions so the builder
// literal stays readable.
func (h *Handler) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:        rewriteRequest,
		ModifyResponse: applyResponseHeaders,
		Transport:      h.newTransport(),
		BufferPool:     newBufferPool(reverseProxyBufSize),
		ErrorHandler:   proxyErrorHandler,
	}
}

// newTransport constructs the http.Transport for the ReverseProxy. The
// DialContext hook routes every dial through dialWorkConn so the work-conn
// pool is the sole source of upstream connections.
func (h *Handler) newTransport() *http.Transport {
	return &http.Transport{
		ResponseHeaderTimeout: transportResponseHeaderTimeout,
		IdleConnTimeout:       transportIdleConnTimeout,
		MaxIdleConnsPerHost:   transportMaxIdleConnsPerHost,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return h.dialWorkConn(ctx)
		},
	}
}

// rewriteRequest is the ReverseProxy Rewrite hook. It sets the outbound
// URL to a synthetic host derived from the RouteConfig, which the stdlib
// Transport uses as the idle-conn-pool key. Keying on the route identity
// (rather than r.Host) isolates pools per proxy so a badly-behaved upstream
// cannot starve its neighbors.
func rewriteRequest(r *httputil.ProxyRequest) {
	cfg := routeFromCtx(r.In.Context())
	r.Out.URL.Scheme = "http"
	r.Out.URL.Host = routeDialKey(cfg)
	if cfg.HostHeaderRewrite != "" {
		r.Out.Host = cfg.HostHeaderRewrite
	}
	for k, v := range cfg.Headers {
		r.Out.Header.Set(k, v)
	}
}

// applyResponseHeaders is the ReverseProxy ModifyResponse hook. It stamps
// per-route response headers onto the response before it reaches the client.
func applyResponseHeaders(resp *http.Response) error {
	cfg := routeFromCtx(resp.Request.Context())
	for k, v := range cfg.ResponseHeaders {
		resp.Header.Set(k, v)
	}
	return nil
}

// proxyErrorHandler is the ReverseProxy ErrorHandler hook. Upstream timeouts
// map to 504, everything else to 502.
func proxyErrorHandler(rw http.ResponseWriter, _ *http.Request, err error) {
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		http.Error(rw, "gateway timeout", http.StatusGatewayTimeout)
		return
	}
	log.Printf("proxy: error: %v", err)
	http.Error(rw, "bad gateway", http.StatusBadGateway)
}

// routeDialKey returns the synthetic host string used as the
// idle-connection-pool key for one route. The format intentionally bundles
// every identifier that should make two routes NOT share pooled conns:
// domain, location, proxy name, and a fixed ".drps" suffix so the value
// cannot collide with any real DNS name.
func routeDialKey(cfg *router.RouteConfig) string {
	return cfg.Domain + "." + cfg.Location + "." + cfg.ProxyName + ".drps"
}

// routeFromCtx extracts the RouteConfig that ServeHTTP stored on ctx.
// A missing value is an internal drps bug, not a runtime input condition,
// so we panic with a clear message rather than adding error paths to every
// hook call site.
func routeFromCtx(ctx context.Context) *router.RouteConfig {
	cfg, ok := ctx.Value(routeCtxKey{}).(*router.RouteConfig)
	if !ok {
		panic("drps internal error: proxy request missing route config in context")
	}
	return cfg
}

// dialWorkConn pulls one idle work-conn from the pool keyed by the matched
// RouteConfig's RunID, wraps it in the negotiated encryption/compression
// envelope, and optionally sets a hard response deadline if ResponseTimeout
// is configured.
func (h *Handler) dialWorkConn(ctx context.Context) (net.Conn, error) {
	cfg := routeFromCtx(ctx)

	p, ok := h.poolLookup(cfg.RunID)
	if !ok {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("pool not found for %s", cfg.RunID)}
	}

	workConn, err := p.Get(h.WorkConnTimeout)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: err}
	}

	wrapped, err := wrap.Wrap(workConn, h.aesKey, cfg.ProxyName, cfg.UseEncryption, cfg.UseCompression)
	if err != nil {
		workConn.Close()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: err}
	}

	if h.ResponseTimeout > 0 {
		wrapped.SetDeadline(time.Now().Add(h.ResponseTimeout))
	}
	return wrapped, nil
}

// bufferPool implements httputil.BufferPool on top of sync.Pool.
type bufferPool struct {
	pool sync.Pool
}

func newBufferPool(size int) *bufferPool {
	return &bufferPool{
		pool: sync.Pool{New: func() any { return make([]byte, size) }},
	}
}

func (bp *bufferPool) Get() []byte  { return bp.pool.Get().([]byte) }
func (bp *bufferPool) Put(b []byte) { bp.pool.Put(b) }
