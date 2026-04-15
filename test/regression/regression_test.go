// Package regression provides drps-only E2E tests — metrics, auth,
// concurrency, and burst scenarios that do not require frps comparison.
// Uses the process-based framework from test/framework/.
package regression

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kangheeyong/drp/test/framework"
)

var (
	drpsBin string
	frpcBin string
)

// TestMain builds drps and downloads frpc once.
// frps is NOT needed for regression (drps-only tests).
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "drp-regression-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %s\n", err)
		os.Exit(1)
	}

	drpsBin = filepath.Join(tmpDir, "drps")
	cmd := exec.Command("go", "build", "-o", drpsBin, "./cmd/drps/")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build drps: %s\n%s", err, out)
		os.Exit(1)
	}

	_, frpcBin, err = framework.DownloadFrp(framework.DefaultFrpVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "download frp: %s\n", err)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

const authToken = "test-token"

// setupDrpsWithFrpc starts an HTTP echo backend + drps + frpc, returning
// the vhost HTTP endpoint and a cleanup function.
func setupDrpsWithFrpc(t *testing.T, domains []string) (httpEndpoint string, cleanup func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

	fw := framework.New(drpsBin, "", frpcBin)
	backendPort, err := framework.StartHTTPEcho(ctx)
	if err != nil {
		t.Fatalf("start http-echo: %v", err)
	}

	frpcPort := fw.PortAllocator.Get()
	httpPort := fw.PortAllocator.Get()
	if frpcPort == 0 || httpPort == 0 {
		t.Fatal("port exhausted")
	}

	fw.StartDrps(ctx, t, authToken, frpcPort, httpPort)

	var proxies strings.Builder
	for _, d := range domains {
		fmt.Fprintf(&proxies, "[[proxies]]\nname = %q\ntype = \"http\"\nlocalIP = \"127.0.0.1\"\nlocalPort = %d\ncustomDomains = [%q]\n\n",
			d, backendPort, d)
	}
	frpcToml := fmt.Sprintf("serverAddr = \"127.0.0.1\"\nserverPort = %d\nauth.method = \"token\"\nauth.token = %q\ntransport.tls.enable = false\n\n%s",
		frpcPort, authToken, proxies.String())
	fw.StartFrpc(ctx, t, frpcToml)

	time.Sleep(500 * time.Millisecond)

	return fmt.Sprintf("127.0.0.1:%d", httpPort), func() {
		fw.Cleanup()
		cancel()
	}
}

// setupDrpsWithFrpcWS starts a WS echo backend + drps + frpc for websocket testing.
func setupDrpsWithFrpcWS(t *testing.T) (wsAddr string, cleanup func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)

	fw := framework.New(drpsBin, "", frpcBin)
	backendPort, err := framework.StartWSEcho(ctx)
	if err != nil {
		t.Fatalf("start ws-echo: %v", err)
	}

	frpcPort := fw.PortAllocator.Get()
	httpPort := fw.PortAllocator.Get()
	if frpcPort == 0 || httpPort == 0 {
		t.Fatal("port exhausted")
	}

	fw.StartDrps(ctx, t, authToken, frpcPort, httpPort)

	frpcToml := fmt.Sprintf(`serverAddr = "127.0.0.1"
serverPort = %d
auth.method = "token"
auth.token = %q
transport.tls.enable = false

[[proxies]]
name = "bench-ws"
type = "http"
localIP = "127.0.0.1"
localPort = %d
customDomains = ["ws.local"]
`, frpcPort, authToken, backendPort)
	fw.StartFrpc(ctx, t, frpcToml)

	time.Sleep(500 * time.Millisecond)

	return fmt.Sprintf("127.0.0.1:%d", httpPort), func() {
		fw.Cleanup()
		cancel()
	}
}

// --- 1. TestFrpcLoginSuccess ---

func TestFrpcLoginSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fw := framework.New(drpsBin, "", frpcBin)
	defer fw.Cleanup()

	backendPort, _ := framework.StartHTTPEcho(ctx)
	frpcPort := fw.PortAllocator.Get()
	httpPort := fw.PortAllocator.Get()

	fw.StartDrps(ctx, t, authToken, frpcPort, httpPort)

	frpcToml := fmt.Sprintf(`serverAddr = "127.0.0.1"
serverPort = %d
auth.method = "token"
auth.token = %q
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "127.0.0.1"
localPort = %d
customDomains = ["test.local"]
`, frpcPort, authToken, backendPort)

	p := fw.StartFrpc(ctx, t, frpcToml)
	if !strings.Contains(p.Output(), "login to server success") {
		t.Fatalf("frpc did not report login success; output: %s", p.Output())
	}
}

// --- 2. TestFrpcNotFoundDomain ---

func TestFrpcNotFoundDomain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"test.local"})
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", "http://"+ep+"/", nil)
	req.Host = "unknown.domain.com"

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// --- 3. TestFrpcMultipleProxies ---

func TestFrpcMultipleProxies(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"site-a.local", "site-b.local"})
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	for _, host := range []string{"site-a.local", "site-b.local"} {
		var resp *http.Response
		var err error
		for i := range 3 {
			req, _ := http.NewRequest("GET", "http://"+ep+"/", nil)
			req.Host = host
			resp, err = client.Do(req)
			if err == nil && resp.StatusCode == 200 {
				break
			}
			if resp != nil {
				resp.Body.Close()
			}
			t.Logf("%s attempt %d: err=%v", host, i+1, err)
			time.Sleep(time.Second)
		}
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		if resp.StatusCode != 200 {
			t.Errorf("%s: status = %d, want 200", host, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// --- 4. TestMetricsEndpointAfterTraffic ---

func TestMetricsEndpointAfterTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"test.local"})
	defer cleanup()

	client := &http.Client{Timeout: 5 * time.Second}
	for range 20 {
		req, _ := http.NewRequest("GET", "http://"+ep+"/", nil)
		req.Host = "test.local"
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	resp, err := client.Get("http://" + ep + "/__drps/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("metrics status=%d", resp.StatusCode)
	}
	var m struct {
		ReqWorkConn struct {
			Requested int64 `json:"requested"`
			Sent      int64 `json:"sent"`
		} `json:"req_work_conn"`
		Pool struct {
			GetHit int64 `json:"get_hit"`
		} `json:"pool"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if m.ReqWorkConn.Requested <= 0 {
		t.Fatalf("requested=%d, want > 0", m.ReqWorkConn.Requested)
	}
	if m.ReqWorkConn.Sent <= 0 {
		t.Fatalf("sent=%d, want > 0", m.ReqWorkConn.Sent)
	}
	if m.Pool.GetHit <= 0 {
		t.Fatalf("get_hit=%d, want > 0", m.Pool.GetHit)
	}
}

// --- 5. TestHTTPConcurrentProxyNo5xx ---

func TestHTTPConcurrentProxyNo5xx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"test.local"})
	defer cleanup()

	httpBurst(t, ep, "test.local", 120, 20)
}

// --- 6. TestHTTPBurst1000NoNon2xx ---

func TestHTTPBurst1000NoNon2xx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"test.local"})
	defer cleanup()

	httpBurst(t, ep, "test.local", 1000, 50)
}

// --- 7. TestWebSocketBasic ---

func TestWebSocketBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	addr, cleanup := setupDrpsWithFrpcWS(t)
	defer cleanup()

	var lastErr error
	for range 5 {
		lastErr = doRawWSEcho(addr, "ws.local")
		if lastErr == nil {
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
	t.Fatalf("ws echo failed: %v", lastErr)
}

// --- 8. TestWSBurstNoFail ---

func TestWSBurstNoFail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	addr, cleanup := setupDrpsWithFrpcWS(t)
	defer cleanup()

	total := 500
	concurrency := 10
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var failed atomic.Int64

	for range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := doRawWSEcho(addr, "ws.local"); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()

	if failed.Load() > 0 {
		t.Fatalf("ws failed=%d/%d", failed.Load(), total)
	}
}

// --- 9. TestMetricsInflightZeroAfterBurst ---

func TestMetricsInflightZeroAfterBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping regression test")
	}
	ep, cleanup := setupDrpsWithFrpc(t, []string{"test.local"})
	defer cleanup()

	httpBurst(t, ep, "test.local", 300, 30)
	time.Sleep(400 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ep + "/__drps/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	var m struct {
		ReqWorkConn struct {
			Requested int64 `json:"requested"`
			Sent      int64 `json:"sent"`
			Inflight  int64 `json:"inflight"`
		} `json:"req_work_conn"`
		Pool struct {
			GetHit  int64 `json:"get_hit"`
			GetMiss int64 `json:"get_miss"`
		} `json:"pool"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.ReqWorkConn.Requested <= 0 || m.ReqWorkConn.Sent <= 0 {
		t.Fatalf("counters: requested=%d sent=%d", m.ReqWorkConn.Requested, m.ReqWorkConn.Sent)
	}
	if m.ReqWorkConn.Inflight != 0 {
		t.Fatalf("inflight=%d, want 0 after burst", m.ReqWorkConn.Inflight)
	}
	if m.Pool.GetHit+m.Pool.GetMiss <= 0 {
		t.Fatalf("pool counters zero")
	}
}

// --- helpers ---

func httpBurst(t *testing.T, ep, host string, total, concurrency int) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var non2xx, failed atomic.Int64

	for range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, _ := http.NewRequest("GET", "http://"+ep+"/", nil)
			req.Host = host
			resp, err := client.Do(req)
			if err != nil {
				failed.Add(1)
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				non2xx.Add(1)
			}
		}()
	}
	wg.Wait()

	if failed.Load() > 0 {
		t.Fatalf("failed=%d", failed.Load())
	}
	if non2xx.Load() > 0 {
		t.Fatalf("non-2xx=%d", non2xx.Load())
	}
}

func doRawWSEcho(addr string, hostHeader string) error {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	upgrade := "GET /ws HTTP/1.1\r\n" +
		"Host: " + hostHeader + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"\r\n"
	if _, err := conn.Write([]byte(upgrade)); err != nil {
		return fmt.Errorf("write upgrade: %w", err)
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	resp := string(buf[:n])
	if !strings.Contains(resp, "101") {
		return fmt.Errorf("not 101: %.80s", resp)
	}

	payload := []byte("hello")
	frame := make([]byte, 2+4+len(payload))
	frame[0] = 0x81
	frame[1] = byte(len(payload)) | 0x80
	copy(frame[6:], payload)
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}

	n, err = conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read echo: %w", err)
	}
	if n < 2 {
		return fmt.Errorf("short frame: %d", n)
	}
	if buf[0] != 0x81 {
		return fmt.Errorf("unexpected opcode: 0x%02x", buf[0])
	}
	frameLen := int(buf[1] & 0x7f)
	if frameLen != len(payload) {
		return fmt.Errorf("payload length %d, want %d", frameLen, len(payload))
	}
	got := string(buf[2 : 2+frameLen])
	if got != "hello" {
		return fmt.Errorf("echo = %q, want %q", got, "hello")
	}
	return nil
}

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	panic("repo root not found from " + file)
}
