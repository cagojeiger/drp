package compat

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"github.com/kangheeyong/drp/test/compat/capture"
	"github.com/kangheeyong/drp/test/compat/compare"
	"github.com/kangheeyong/drp/test/compat/launcher"
	"github.com/kangheeyong/drp/test/compat/schema"
)

// TestCompat is the table-driven runner over every scenarios/*.yaml.
// Each subtest spins up a fresh docker network with backend + drps + frps +
// two frpc instances, executes the request against both proxies, and compares.
//
// Run serialized (COMPAT_SERIAL=1) to avoid docker-in-docker race conditions
// on constrained runners, or in parallel (default) for faster local runs.
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

	serial := os.Getenv("COMPAT_SERIAL") == "1"

	for _, s := range scenarios {
		s := s
		t.Run(s.Name, func(t *testing.T) {
			if !serial {
				t.Parallel()
			}
			runScenario(t, s)
		})
	}
}

func runScenario(t *testing.T, s schema.Scenario) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	net := launcher.NewNetwork(ctx, t)
	var containers []testcontainers.Container
	defer func() {
		for _, c := range containers {
			_ = c.Terminate(context.Background())
		}
		_ = net.Remove(context.Background())
	}()

	// --- backend ----------------------------------------------------------
	switch s.Upstream.Kind {
	case "nginx":
		containers = append(containers, launcher.StartNginx(ctx, t, net.Name))
	case "ws-echo":
		containers = append(containers, launcher.StartWSEcho(ctx, t, net.Name))
	default:
		t.Fatalf("unsupported upstream kind %q (practical-minimum suite)", s.Upstream.Kind)
	}

	// --- drps + frps (parallel targets) -----------------------------------
	drps := launcher.StartDrps(ctx, t, net.Name)
	containers = append(containers, drps)
	frps := launcher.StartFrps(ctx, t, net.Name, FrpsBaseline)
	containers = append(containers, frps)

	// --- frpc instances for each target -----------------------------------
	frpcToml := renderFrpcToml(s)
	frpcDrps := launcher.StartFrpc(ctx, t, net.Name, "drps", frpcToml, "start proxy success")
	containers = append(containers, frpcDrps)
	frpcFrps := launcher.StartFrpc(ctx, t, net.Name, "frps", frpcToml, "start proxy success")
	containers = append(containers, frpcFrps)

	// frps sometimes needs a beat for its work-conn pool to drain.
	time.Sleep(500 * time.Millisecond)

	drpsEP := launcher.Endpoint(ctx, t, drps, "18080/tcp")
	frpsEP := launcher.Endpoint(ctx, t, frps, "18080/tcp")

	// --- capture ----------------------------------------------------------
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
		t.Fatalf("unsupported mode %q (practical-minimum suite)", s.Expect.Mode)
	}

	t.Logf("\n%s", report.Render())
	if !report.Pass() {
		t.Fatalf("compat mismatch: %s", report.Summary())
	}
}

// captureHTTPWithRetry retries on transient failures while frpc pools warm up.
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
		t.Fatalf("%s body=%d bytes, want ≥%d", tag, len(resp.Body), e.MinBodyBytes)
	}
	if e.WSFrameCount > 0 && len(resp.WSFrames) != e.WSFrameCount {
		t.Fatalf("%s ws_frames=%d, want %d", tag, len(resp.WSFrames), e.WSFrameCount)
	}
}

func renderFrpcToml(s schema.Scenario) string {
	var b strings.Builder
	token := s.Frpc.AuthToken
	if token == "" {
		token = launcher.AuthToken
	}
	fmt.Fprintf(&b, "serverAddr = \"{{SERVER}}\"\n")
	fmt.Fprintf(&b, "serverPort = 17000\n")
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
