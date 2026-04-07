package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/kangheeyong/drp/internal/pool"
	"github.com/kangheeyong/drp/internal/router"
	"github.com/kangheeyong/drp/internal/wrap"
)

const workConnTimeout = 10 * time.Second

// PoolLookup returns the work connection pool for a proxy name.
type PoolLookup func(proxyName string) (*pool.Pool, bool)

// Handler is the HTTP handler that proxies requests through frpc work connections.
type Handler struct {
	router          *router.Router
	poolLookup      PoolLookup
	token           string
	WorkConnTimeout time.Duration
	ResponseTimeout time.Duration
}

func NewHandler(rt *router.Router, poolLookup PoolLookup, token string) *Handler {
	return &Handler{
		router:          rt,
		poolLookup:      poolLookup,
		token:           token,
		WorkConnTimeout: workConnTimeout,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. 라우팅
	cfg, ok := h.router.Lookup(r.Host, r.URL.Path)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	log.Printf("proxy: %s%s → proxy=%s runID=%s", r.Host, r.URL.Path, cfg.ProxyName, cfg.RunID)

	// 2. Basic Auth 검증
	if cfg.HTTPUser != "" {
		user, pass, ok := r.BasicAuth()
		if !ok || user != cfg.HTTPUser || pass != cfg.HTTPPwd {
			w.Header().Set("WWW-Authenticate", `Basic realm="Authorization Required"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// 3. 워크 커넥션 획득
	p, ok := h.poolLookup(cfg.ProxyName)
	if !ok {
		log.Printf("proxy: pool not found for %s", cfg.ProxyName)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	log.Printf("proxy: getting work conn for %s", cfg.ProxyName)
	workConn, err := p.Get(h.WorkConnTimeout)
	if err != nil {
		log.Printf("proxy: get work conn failed: %v", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	log.Printf("proxy: got work conn, wrapping enc=%v comp=%v", cfg.UseEncryption, cfg.UseCompression)

	// 3. StartWorkConn + 암호화/압축 래핑
	wrapped, err := wrap.Wrap(workConn, h.token, cfg.ProxyName, cfg.UseEncryption, cfg.UseCompression)
	if err != nil {
		log.Printf("proxy: wrap failed: %v", err)
		workConn.Close()
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	log.Printf("proxy: wrap done, forwarding request")

	// 5. HostHeaderRewrite
	if cfg.HostHeaderRewrite != "" {
		r.Host = cfg.HostHeaderRewrite
	}

	// 6. Custom request headers
	for k, v := range cfg.Headers {
		r.Header.Set(k, v)
	}

	// 5. Response timeout
	if h.ResponseTimeout > 0 {
		if nc, ok := workConn.(net.Conn); ok {
			nc.SetDeadline(time.Now().Add(h.ResponseTimeout))
		}
	}

	// 6. Forward request and relay response
	transport := &connTransport{conn: wrapped}
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	if outReq.URL.Host == "" {
		outReq.URL.Host = r.Host
	}

	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			http.Error(w, "gateway timeout", http.StatusGatewayTimeout)
		} else {
			log.Printf("proxy: roundtrip error: %v", err)
			http.Error(w, "bad gateway", http.StatusBadGateway)
		}
		return
	}

	// WebSocket 101: hijack and bidirectional relay
	if resp.StatusCode == http.StatusSwitchingProtocols {
		h.handleUpgrade(w, resp)
		return
	}

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	// Custom response headers
	for k, v := range cfg.ResponseHeaders {
		w.Header().Set(k, v)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	resp.Body.Close()
}

func (h *Handler) handleUpgrade(w http.ResponseWriter, resp *http.Response) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket not supported", http.StatusInternalServerError)
		resp.Body.Close()
		return
	}

	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		resp.Body.Close()
		return
	}

	// Write 101 response header to client (without body)
	fmt.Fprintf(clientBuf, "HTTP/%d.%d %d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.StatusCode, http.StatusText(resp.StatusCode))
	resp.Header.Write(clientBuf)
	clientBuf.WriteString("\r\n")
	clientBuf.Flush()

	// Bidirectional relay between client and backend
	backend := resp.Body.(io.ReadWriteCloser)
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(backend, clientConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, backend)
		done <- struct{}{}
	}()
	<-done

	clientConn.Close()
	backend.Close()
}

// connTransport is an http.RoundTripper that uses a pre-established connection.
type connTransport struct {
	conn interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
	}
}

func (t *connTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Write(t.conn); err != nil {
		t.conn.Close()
		return nil, err
	}

	br := bufio.NewReader(t.conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.conn.Close()
		return nil, err
	}

	// 101 Switching Protocols: bufio에 남은 데이터가 있으면 합치고, 없으면 conn 직접 사용
	var r io.Reader
	if br.Buffered() > 0 {
		r = io.MultiReader(br, t.conn)
	} else {
		r = t.conn
	}
	resp.Body = &connClosingBody{
		ReadCloser: &readCloser{Reader: r, Closer: resp.Body},
		conn:       t.conn,
	}
	return resp, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// connClosingBody closes the underlying connection when the body is closed.
// Implements io.ReadWriteCloser so httputil.ReverseProxy can handle 101 Switching Protocols.
type connClosingBody struct {
	io.ReadCloser
	conn interface {
		io.Writer
		io.Closer
	}
}

func (b *connClosingBody) Write(p []byte) (int, error) {
	return b.conn.Write(p)
}

func (b *connClosingBody) Close() error {
	err := b.ReadCloser.Close()
	b.conn.Close()
	return err
}
