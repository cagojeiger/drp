package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startDrps creates and starts a drps container.
func startDrps(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    "..",
				Dockerfile: "Dockerfile",
			},
			ExposedPorts: []string{"17000/tcp", "18080/tcp"},
			Networks:     []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"drps"},
			},
			WaitingFor: wait.ForLog("drps listening").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start drps: %v", err)
	}
	return c
}

// startFrpc creates and starts a frpc container with the given config.
func startFrpc(ctx context.Context, t *testing.T, netName, frpcToml, waitLog string) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    ".",
				Dockerfile: "Dockerfile.frpc",
			},
			Cmd:      []string{"-c", "/etc/frpc.toml"},
			Networks: []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"frpc"},
			},
			Files: []testcontainers.ContainerFile{
				{
					ContainerFilePath: "/etc/frpc.toml",
					Reader:            strings.NewReader(frpcToml),
					FileMode:          0644,
				},
			},
			WaitingFor: wait.ForLog(waitLog).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start frpc: %v", err)
	}
	return c
}

// startBackend creates a simple nginx backend container.
func startBackend(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:    "nginx:alpine",
			Networks: []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"backend"},
			},
			WaitingFor: wait.ForHTTP("/").WithPort("80/tcp").WithStartupTimeout(15 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start backend: %v", err)
	}
	return c
}

func startWSEcho(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    "../bench/ws-echo",
				Dockerfile: "Dockerfile",
			},
			ExposedPorts: []string{"9090/tcp"},
			Networks:     []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"ws-echo"},
			},
			WaitingFor: wait.ForListeningPort("9090/tcp").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ws-echo: %v", err)
	}
	return c
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

func TestFrpcLoginSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer net.Remove(ctx)

	drps := startDrps(ctx, t, net.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localPort = 8080
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, net.Name, frpcToml, "login to server success")
	defer frpc.Terminate(ctx)
}

func TestFrpcFullPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer net.Remove(ctx)

	backend := startBackend(ctx, t, net.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, net.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, net.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	// HTTP 요청
	drpsPort, _ := drps.MappedPort(ctx, "18080/tcp")
	drpsHost, _ := drps.Host(ctx)
	url := fmt.Sprintf("http://%s:%s/", drpsHost, drpsPort.Port())

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Host = "test.local"

	// 워크 커넥션 준비 대기 — 최대 3번 재시도
	var resp *http.Response
	for i := range 3 {
		resp, err = client.Do(req)
		if err == nil && resp.StatusCode == 200 {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		t.Logf("attempt %d: err=%v", i+1, err)
		time.Sleep(time.Second)
		req, _ = http.NewRequest("GET", url, nil)
		req.Host = "test.local"
	}
	if err != nil || resp.StatusCode != 200 {
		// drps 로그 출력
		logs, _ := drps.Logs(ctx)
		logBuf, _ := io.ReadAll(logs)
		t.Logf("drps logs:\n%s", string(logBuf))
		// frpc 로그 출력
		logs2, _ := frpc.Logs(ctx)
		logBuf2, _ := io.ReadAll(logs2)
		t.Logf("frpc logs:\n%s", string(logBuf2))
		if err != nil {
			t.Fatalf("HTTP request failed after retries: %v", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "nginx") {
		t.Errorf("body should contain 'nginx', got: %s", string(body)[:200])
	}
	t.Logf("response: %d bytes, status %d", len(body), resp.StatusCode)
}

func TestFrpcNotFoundDomain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer net.Remove(ctx)

	drps := startDrps(ctx, t, net.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localPort = 8080
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, net.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	// 등록 안 된 도메인으로 요청
	drpsPort, _ := drps.MappedPort(ctx, "18080/tcp")
	drpsHost, _ := drps.Host(ctx)
	url := fmt.Sprintf("http://%s:%s/", drpsHost, drpsPort.Port())

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
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

func TestFrpcMultipleProxies(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer net.Remove(ctx)

	backend := startBackend(ctx, t, net.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, net.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web1"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["site-a.local"]

[[proxies]]
name = "web2"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["site-b.local"]
`
	frpc := startFrpc(ctx, t, net.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	drpsPort, _ := drps.MappedPort(ctx, "18080/tcp")
	drpsHost, _ := drps.Host(ctx)
	client := &http.Client{Timeout: 5 * time.Second}

	for _, host := range []string{"site-a.local", "site-b.local"} {
		var resp *http.Response
		for i := range 3 {
			url := fmt.Sprintf("http://%s:%s/", drpsHost, drpsPort.Port())
			req, _ := http.NewRequest("GET", url, nil)
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

func TestWebSocketE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	wsEcho := startWSEcho(ctx, t, netw.Name)
	defer wsEcho.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "bench-ws"
type = "http"
localIP = "ws-echo"
localPort = 9090
customDomains = ["ws.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	addr := net.JoinHostPort(host, port.Port())

	var lastErr error
	for range 5 {
		lastErr = doRawWSEcho(addr, "ws.local")
		if lastErr == nil {
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
	t.Fatalf("ws upgrade/echo failed: %v", lastErr)
}

func TestMetricsEndpointAfterTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	backend := startBackend(ctx, t, netw.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	baseURL := fmt.Sprintf("http://%s:%s", host, port.Port())

	client := &http.Client{Timeout: 5 * time.Second}
	for range 20 {
		req, _ := http.NewRequest("GET", baseURL+"/", nil)
		req.Host = "test.local"
		resp, err := client.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	resp, err := client.Get(baseURL + "/__drps/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("metrics status=%d, want 200", resp.StatusCode)
	}
	var m struct {
		ReqWorkConn struct {
			Requested int64 `json:"requested"`
			Sent      int64 `json:"sent"`
		} `json:"req_work_conn"`
		Pool struct {
			GetHit      int64 `json:"get_hit"`
			RefillSent  int64 `json:"refill_sent"`
			ActivePools int64 `json:"active_pools"`
		} `json:"pool"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if m.ReqWorkConn.Requested <= 0 {
		t.Fatalf("req_work_conn.requested=%d, want > 0", m.ReqWorkConn.Requested)
	}
	if m.ReqWorkConn.Sent <= 0 {
		t.Fatalf("req_work_conn.sent=%d, want > 0", m.ReqWorkConn.Sent)
	}
	if m.Pool.GetHit <= 0 {
		t.Fatalf("pool.get_hit=%d, want > 0", m.Pool.GetHit)
	}
}

func TestHTTPConcurrentProxyNo5xx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	backend := startBackend(ctx, t, netw.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	url := fmt.Sprintf("http://%s:%s/", host, port.Port())
	client := &http.Client{Timeout: 5 * time.Second}

	total := 120
	concurrency := 20
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var ok200 atomic.Int64
	var non2xx atomic.Int64
	var failed atomic.Int64

	for range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			req, _ := http.NewRequest("GET", url, nil)
			req.Host = "test.local"
			resp, err := client.Do(req)
			if err != nil {
				failed.Add(1)
				return
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode == 200 {
				ok200.Add(1)
				return
			}
			non2xx.Add(1)
		}()
	}
	wg.Wait()

	if failed.Load() > 0 {
		t.Fatalf("request failed=%d", failed.Load())
	}
	if non2xx.Load() > 0 {
		t.Fatalf("non-2xx=%d", non2xx.Load())
	}
	if ok200.Load() != int64(total) {
		t.Fatalf("ok200=%d, want %d", ok200.Load(), total)
	}
}

func TestHTTPBurst1000NoNon2xx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	backend := startBackend(ctx, t, netw.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	url := fmt.Sprintf("http://%s:%s/", host, port.Port())
	client := &http.Client{Timeout: 5 * time.Second}

	total := 1000
	concurrency := 50
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var non2xx atomic.Int64
	var failed atomic.Int64

	for range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			req, _ := http.NewRequest("GET", url, nil)
			req.Host = "test.local"
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
		t.Fatalf("request failed=%d", failed.Load())
	}
	if non2xx.Load() > 0 {
		t.Fatalf("non-2xx=%d", non2xx.Load())
	}
}

func TestWSBurst200NoFail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	wsEcho := startWSEcho(ctx, t, netw.Name)
	defer wsEcho.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "bench-ws"
type = "http"
localIP = "ws-echo"
localPort = 9090
customDomains = ["ws.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	addr := net.JoinHostPort(host, port.Port())

	total := 200
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

func TestWSBurst500NoFail(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	wsEcho := startWSEcho(ctx, t, netw.Name)
	defer wsEcho.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "bench-ws"
type = "http"
localIP = "ws-echo"
localPort = 9090
customDomains = ["ws.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	addr := net.JoinHostPort(host, port.Port())

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

func TestMetricsInflightZeroAfterBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()

	netw, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	defer netw.Remove(ctx)

	backend := startBackend(ctx, t, netw.Name)
	defer backend.Terminate(ctx)

	drps := startDrps(ctx, t, netw.Name)
	defer drps.Terminate(ctx)

	frpcToml := `
serverAddr = "drps"
serverPort = 17000
auth.token = "test-token"
transport.tls.enable = false

[[proxies]]
name = "web"
type = "http"
localIP = "backend"
localPort = 80
customDomains = ["test.local"]
`
	frpc := startFrpc(ctx, t, netw.Name, frpcToml, "start proxy success")
	defer frpc.Terminate(ctx)

	port, _ := drps.MappedPort(ctx, "18080/tcp")
	host, _ := drps.Host(ctx)
	baseURL := fmt.Sprintf("http://%s:%s", host, port.Port())
	client := &http.Client{Timeout: 5 * time.Second}

	total := 300
	concurrency := 30
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for range total {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			req, _ := http.NewRequest("GET", baseURL+"/", nil)
			req.Host = "test.local"
			resp, err := client.Do(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	time.Sleep(400 * time.Millisecond)

	resp, err := client.Get(baseURL + "/__drps/metrics")
	if err != nil {
		t.Fatalf("GET metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("metrics status=%d, want 200", resp.StatusCode)
	}
	var m struct {
		ReqWorkConn struct {
			Requested int64 `json:"requested"`
			Sent      int64 `json:"sent"`
			Inflight  int64 `json:"inflight"`
		} `json:"req_work_conn"`
		Pool struct {
			GetHit     int64 `json:"get_hit"`
			GetMiss    int64 `json:"get_miss"`
			RefillSent int64 `json:"refill_sent"`
		} `json:"pool"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if m.ReqWorkConn.Requested <= 0 || m.ReqWorkConn.Sent <= 0 {
		t.Fatalf("invalid req_work_conn counters: requested=%d sent=%d", m.ReqWorkConn.Requested, m.ReqWorkConn.Sent)
	}
	if m.ReqWorkConn.Inflight != 0 {
		t.Fatalf("req_work_conn.inflight=%d, want 0 after burst", m.ReqWorkConn.Inflight)
	}
	if m.Pool.GetHit+m.Pool.GetMiss <= 0 {
		t.Fatalf("pool get counters are zero")
	}
}

// Aliases for docs/tests/*.md names
func TestHTTPConcurrentProxy(t *testing.T) { TestHTTPConcurrentProxyNo5xx(t) }
func TestHTTPLoadZeroErrors(t *testing.T)  { TestHTTPBurst1000NoNon2xx(t) }
func TestWebSocketConcurrent(t *testing.T) { TestWSBurst200NoFail(t) }
func TestMetricsEndpoint(t *testing.T)     { TestMetricsEndpointAfterTraffic(t) }
func TestMetricsAfterLoad(t *testing.T)    { TestMetricsInflightZeroAfterBurst(t) }
func TestIB(t *testing.T)                  { TestHTTPBurst1000NoNon2xx(t) }
