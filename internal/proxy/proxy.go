package proxy

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
	"github.com/kangheeyong/drp/internal/wrap"
)

const workConnTimeout = 10 * time.Second

// routeCtxKey is the context key for RouteConfig.
type routeCtxKey struct{}

// PoolLookup returns the work connection pool for a run ID.
type PoolLookup func(runID string) (*pool.Pool, bool)

// Handler is the HTTP handler that proxies requests through frpc work connections.
type Handler struct {
	router          *router.Router
	poolLookup      PoolLookup
	aesKey          []byte
	proxy           http.Handler
	WorkConnTimeout time.Duration
	ResponseTimeout time.Duration
}

func NewHandler(rt *router.Router, poolLookup PoolLookup, aesKey []byte) *Handler {
	h := &Handler{
		router:          rt,
		poolLookup:      poolLookup,
		aesKey:          aesKey,
		WorkConnTimeout: workConnTimeout,
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			cfg := r.In.Context().Value(routeCtxKey{}).(*router.RouteConfig)
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = r.In.Host
			if cfg.HostHeaderRewrite != "" {
				r.Out.Host = cfg.HostHeaderRewrite
			}
			for k, v := range cfg.Headers {
				r.Out.Header.Set(k, v)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			cfg := resp.Request.Context().Value(routeCtxKey{}).(*router.RouteConfig)
			for k, v := range cfg.ResponseHeaders {
				resp.Header.Set(k, v)
			}
			return nil
		},
		Transport: &http.Transport{
			IdleConnTimeout:     60 * time.Second,
			MaxIdleConnsPerHost: 5,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return h.dialWorkConn(ctx)
			},
		},
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				http.Error(rw, "gateway timeout", http.StatusGatewayTimeout)
			} else {
				log.Printf("proxy: error: %v", err)
				http.Error(rw, "bad gateway", http.StatusBadGateway)
			}
		},
	}
	h.proxy = rp
	return h
}

func (h *Handler) dialWorkConn(ctx context.Context) (net.Conn, error) {
	cfg := ctx.Value(routeCtxKey{}).(*router.RouteConfig)

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
