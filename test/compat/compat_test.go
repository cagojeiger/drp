package compat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kangheeyong/drp/test/compat/capture"
	"github.com/kangheeyong/drp/test/compat/compare"
	"github.com/kangheeyong/drp/test/compat/framework"
	"github.com/kangheeyong/drp/test/compat/schema"
)

var (
	drpsBin string
	frpsBin string
	frpcBin string
)

// TestMain builds drps and downloads frps/frpc once for the entire suite.
func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "drp-compat-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tmpdir: %s\n", err)
		os.Exit(1)
	}

	// 1. Build drps binary
	drpsBin = filepath.Join(tmpDir, "drps")
	cmd := exec.Command("go", "build", "-o", drpsBin, "./cmd/drps/")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build drps: %s\n%s", err, out)
		os.Exit(1)
	}

	// 2. Download frps + frpc from GitHub releases (cached)
	frpsBin, frpcBin, err = framework.DownloadFrp(framework.DefaultFrpVersion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "download frp: %s\n", err)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(code)
}

const authToken = "compat-token"

func TestCompat(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compat suite in short mode")
	}

	scenarios, err := LoadAllScenarios()
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}
	if len(scenarios) == 0 {
		t.Fatal("no scenarios found")
	}

	for _, s := range scenarios {
		s := s
		t.Run(s.Name, func(t *testing.T) {
			t.Parallel()
			runScenario(t, s)
		})
	}
}

func runScenario(t *testing.T, s schema.Scenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fw := framework.New(drpsBin, frpsBin, frpcBin)
	defer fw.Cleanup()

	// 1. Start backend
	var backendPort int
	switch s.Upstream.Kind {
	case "http-echo":
		p, err := framework.StartHTTPEcho(ctx)
		if err != nil {
			t.Fatalf("start http-echo: %v", err)
		}
		backendPort = p
	case "ws-echo":
		p, err := framework.StartWSEcho(ctx)
		if err != nil {
			t.Fatalf("start ws-echo: %v", err)
		}
		backendPort = p
	default:
		t.Fatalf("unsupported upstream kind %q", s.Upstream.Kind)
	}

	// 2. Inject backend port (replace localPort: 0 sentinel)
	for i := range s.Frpc.Proxies {
		if s.Frpc.Proxies[i].LocalPort == 0 {
			s.Frpc.Proxies[i].LocalPort = backendPort
		}
	}

	// 3. Allocate ports
	drpsFrpcPort := fw.PortAllocator.Get()
	drpsHTTPPort := fw.PortAllocator.Get()
	frpsFrpcPort := fw.PortAllocator.Get()
	frpsHTTPPort := fw.PortAllocator.Get()
	defer fw.PortAllocator.Release(drpsFrpcPort)
	defer fw.PortAllocator.Release(drpsHTTPPort)
	defer fw.PortAllocator.Release(frpsFrpcPort)
	defer fw.PortAllocator.Release(frpsHTTPPort)

	if drpsFrpcPort == 0 || drpsHTTPPort == 0 || frpsFrpcPort == 0 || frpsHTTPPort == 0 {
		t.Fatal("port allocation exhausted")
	}

	// 4. Start drps (env vars only)
	fw.StartDrps(ctx, t, authToken, drpsFrpcPort, drpsHTTPPort)

	// 5. Start frps
	frpsToml := fmt.Sprintf("bindPort = %d\nvhostHTTPPort = %d\n\n[auth]\nmethod = \"token\"\ntoken = %q\n",
		frpsFrpcPort, frpsHTTPPort, authToken)
	fw.StartFrps(ctx, t, frpsToml, frpsFrpcPort, frpsHTTPPort)

	// 6. Start frpc → drps
	frpcDrpsToml := renderFrpcToml(s, "127.0.0.1", drpsFrpcPort)
	fw.StartFrpc(ctx, t, frpcDrpsToml)

	// 7. Start frpc → frps
	frpcFrpsToml := renderFrpcToml(s, "127.0.0.1", frpsFrpcPort)
	fw.StartFrpc(ctx, t, frpcFrpsToml)

	time.Sleep(500 * time.Millisecond) // work-conn pool warmup

	drpsEP := fmt.Sprintf("127.0.0.1:%d", drpsHTTPPort)
	frpsEP := fmt.Sprintf("127.0.0.1:%d", frpsHTTPPort)

	// 8. Capture + compare
	if s.Load != nil {
		runLoadScenario(t, ctx, s, drpsEP, frpsEP)
	} else {
		runSingleScenario(t, ctx, s, drpsEP, frpsEP)
	}
}

func runSingleScenario(t *testing.T, ctx context.Context, s schema.Scenario, drpsEP, frpsEP string) {
	t.Helper()
	var report *compare.Report
	switch s.Expect.Mode {
	case "http":
		drpsResp := captureHTTPWithRetry(ctx, drpsEP, s.Request)
		frpsResp := captureHTTPWithRetry(ctx, frpsEP, s.Request)
		report = compare.HTTP(s.Name, drpsResp, frpsResp, s.ExpectedDivergence)
		assertExpect(t, s.Expect, drpsResp, "drps")
		assertExpect(t, s.Expect, frpsResp, "frps")
	case "websocket":
		drpsResp := captureWSWithRetry(ctx, drpsEP, s.Request)
		frpsResp := captureWSWithRetry(ctx, frpsEP, s.Request)
		report = compare.WebSocket(s.Name, drpsResp, frpsResp, s.ExpectedDivergence)
		assertExpect(t, s.Expect, drpsResp, "drps")
		assertExpect(t, s.Expect, frpsResp, "frps")
	default:
		t.Fatalf("unsupported mode %q", s.Expect.Mode)
	}
	t.Logf("\n%s", report.Render())
	if !report.Pass() {
		t.Fatalf("compat mismatch: %s", report.Summary())
	}
}

func runLoadScenario(t *testing.T, ctx context.Context, s schema.Scenario, drpsEP, frpsEP string) {
	t.Helper()
	total := s.Load.Total
	conc := s.Load.Concurrency
	if total <= 0 || conc <= 0 {
		t.Fatalf("load profile invalid: total=%d concurrency=%d", total, conc)
	}

	run := func(tag, endpoint string) (ok, fail int64) {
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		var okCount, failCount atomic.Int64
		for range total {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				resp := capture.DoHTTP(ctx, endpoint, s.Request)
				if resp.Err == nil && resp.Status >= 200 && resp.Status < 400 {
					okCount.Add(1)
				} else {
					failCount.Add(1)
				}
			}()
		}
		wg.Wait()
		return okCount.Load(), failCount.Load()
	}

	drpsOK, drpsFail := run("drps", drpsEP)
	frpsOK, frpsFail := run("frps", frpsEP)

	t.Logf("load %s: drps ok=%d fail=%d | frps ok=%d fail=%d",
		s.Name, drpsOK, drpsFail, frpsOK, frpsFail)

	if drpsFail > 0 {
		t.Errorf("drps: %d/%d failed", drpsFail, total)
	}
	if frpsFail > 0 {
		t.Errorf("frps: %d/%d failed", frpsFail, total)
	}
}

func captureHTTPWithRetry(ctx context.Context, endpoint string, req schema.RequestSpec) *capture.CapturedResponse {
	var resp *capture.CapturedResponse
	for i := 0; i < 5; i++ {
		resp = capture.DoHTTP(ctx, endpoint, req)
		if resp.Err == nil && resp.Status > 0 && resp.Status < 500 {
			return resp
		}
		time.Sleep(500 * time.Millisecond)
	}
	return resp
}

func captureWSWithRetry(ctx context.Context, endpoint string, req schema.RequestSpec) *capture.CapturedResponse {
	var resp *capture.CapturedResponse
	for i := 0; i < 5; i++ {
		resp = capture.DoWebSocket(ctx, endpoint, req.Host, req.Path, req.WSFrames)
		if resp.Err == nil && resp.Status == 101 {
			return resp
		}
		time.Sleep(500 * time.Millisecond)
	}
	return resp
}

func assertExpect(t *testing.T, e schema.ExpectSpec, resp *capture.CapturedResponse, tag string) {
	t.Helper()
	if resp.Err != nil {
		t.Fatalf("%s capture error: %v", tag, resp.Err)
	}
	if e.Status != 0 && resp.Status != e.Status {
		t.Fatalf("%s status=%d, want %d", tag, resp.Status, e.Status)
	}
	if e.MinBodyBytes > 0 && len(resp.Body) < e.MinBodyBytes {
		t.Fatalf("%s body=%d bytes, want >=%d", tag, len(resp.Body), e.MinBodyBytes)
	}
	if e.WSFrameCount > 0 && len(resp.WSFrames) != e.WSFrameCount {
		t.Fatalf("%s ws_frames=%d, want %d", tag, len(resp.WSFrames), e.WSFrameCount)
	}
}

func renderFrpcToml(s schema.Scenario, serverAddr string, serverPort int) string {
	var b strings.Builder
	token := s.Frpc.AuthToken
	if token == "" {
		token = authToken
	}
	fmt.Fprintf(&b, "serverAddr = %q\n", serverAddr)
	fmt.Fprintf(&b, "serverPort = %d\n", serverPort)
	fmt.Fprintf(&b, "auth.method = \"token\"\n")
	fmt.Fprintf(&b, "auth.token = %q\n", token)
	fmt.Fprintf(&b, "transport.tls.enable = false\n\n")
	for _, p := range s.Frpc.Proxies {
		fmt.Fprintf(&b, "[[proxies]]\n")
		fmt.Fprintf(&b, "name = %q\n", p.Name)
		fmt.Fprintf(&b, "type = %q\n", p.Type)
		if p.LocalIP != "" {
			fmt.Fprintf(&b, "localIP = %q\n", p.LocalIP)
		}
		fmt.Fprintf(&b, "localPort = %d\n", p.LocalPort)
		fmt.Fprintf(&b, "customDomains = [")
		for i, d := range p.CustomDomains {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q", d)
		}
		b.WriteString("]\n\n")
	}
	return b.String()
}

// repoRoot walks up from this file to find go.mod.
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
