package framework

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// Framework ties together port allocation, process management, and backend
// lifecycle for one test scenario.
type Framework struct {
	PortAllocator *Allocator
	TempDir       string
	DrpsBin       string
	FrpsBin       string
	FrpcBin       string
	processes     []*Process
}

// New creates a Framework with the given binary paths and a fresh temp dir.
func New(drpsBin, frpsBin, frpcBin string) *Framework {
	tmp, _ := os.MkdirTemp("", "drp-fw-*")
	return &Framework{
		PortAllocator: NewAllocatorFromEnv(),
		TempDir:       tmp,
		DrpsBin:       drpsBin,
		FrpsBin:       frpsBin,
		FrpcBin:       frpcBin,
	}
}

// StartDrps starts a drps process configured EXCLUSIVELY via env vars.
//
// DO NOT pass CLI flags — config.go uses flag.Parse() which conflicts with
// go test flags. Env vars (DRPS_TOKEN, DRPS_FRPC_ADDR, DRPS_HTTP_ADDR)
// override flag defaults (config.go:48-56).
//
// Waits for combined output pattern "drps listening" (emitted via
// log.Printf to stderr, captured by Process's shared SafeBuffer) + TCP
// readiness on both ports.
func (f *Framework) StartDrps(ctx context.Context, t *testing.T, token string, frpcPort, httpPort int) *Process {
	t.Helper()
	env := []string{
		"DRPS_TOKEN=" + token,
		"DRPS_FRPC_ADDR=:" + strconv.Itoa(frpcPort),
		"DRPS_HTTP_ADDR=:" + strconv.Itoa(httpPort),
	}
	p := NewProcessWithEnv(f.DrpsBin, nil, env)
	if err := p.Start(); err != nil {
		t.Fatalf("start drps: %v", err)
	}
	f.processes = append(f.processes, p)

	if err := p.WaitForOutput("drps listening", 15*time.Second); err != nil {
		t.Fatalf("drps readiness: %v", err)
	}
	if err := WaitForTCP(fmt.Sprintf("127.0.0.1:%d", frpcPort), 5*time.Second); err != nil {
		t.Fatalf("drps frpc port: %v", err)
	}
	return p
}

// StartFrps starts a frps process with a TOML config written to TempDir.
// Waits for "frps started successfully" in output + TCP readiness.
func (f *Framework) StartFrps(ctx context.Context, t *testing.T, configToml string, bindPort, vhostHTTPPort int) *Process {
	t.Helper()
	cfgPath := fmt.Sprintf("%s/frps-%d.toml", f.TempDir, bindPort)
	if err := os.WriteFile(cfgPath, []byte(configToml), 0o644); err != nil {
		t.Fatalf("write frps config: %v", err)
	}

	p := NewProcess(f.FrpsBin, "-c", cfgPath)
	if err := p.Start(); err != nil {
		t.Fatalf("start frps: %v", err)
	}
	f.processes = append(f.processes, p)

	if err := p.WaitForOutput("frps started successfully", 15*time.Second); err != nil {
		t.Fatalf("frps readiness: %v", err)
	}
	if err := WaitForTCP(fmt.Sprintf("127.0.0.1:%d", bindPort), 5*time.Second); err != nil {
		t.Fatalf("frps bind port: %v", err)
	}
	return p
}

// StartFrpc starts a frpc process with a rendered TOML config.
// Waits for "start proxy success" in output.
func (f *Framework) StartFrpc(ctx context.Context, t *testing.T, configToml string) *Process {
	t.Helper()
	cfgPath := fmt.Sprintf("%s/frpc-%d.toml", f.TempDir, time.Now().UnixNano())
	if err := os.WriteFile(cfgPath, []byte(configToml), 0o644); err != nil {
		t.Fatalf("write frpc config: %v", err)
	}

	p := NewProcess(f.FrpcBin, "-c", cfgPath)
	if err := p.Start(); err != nil {
		t.Fatalf("start frpc: %v", err)
	}
	f.processes = append(f.processes, p)

	if err := p.WaitForOutput("start proxy success", 15*time.Second); err != nil {
		t.Fatalf("frpc readiness: %v", err)
	}
	return p
}

// Cleanup stops all processes in LIFO order and removes the temp dir.
func (f *Framework) Cleanup() {
	for i := len(f.processes) - 1; i >= 0; i-- {
		_ = f.processes[i].Stop()
	}
	os.RemoveAll(f.TempDir)
}
