package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
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
