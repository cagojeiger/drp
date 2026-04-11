// Package launcher spins up drps, frps, frpc, and backend containers on a
// testcontainers network for the compat test suite.
//
// This is intentionally a single-file package for the practical-minimum
// compat suite. If the suite grows beyond HTTP + WebSocket, split into
// per-target files.
package launcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	tcnet "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// AuthToken is the shared token used by drps, frps, and all frpc clients
// in the compat suite. Must match fixtures/frps.toml.
const AuthToken = "compat-token"

// RepoRoot returns the absolute path to the repository root by walking up
// from this source file (runtime.Caller) until it finds go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
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
	t.Fatalf("repo root not found from %s", file)
	return ""
}

// NewNetwork creates a fresh docker network for one scenario.
func NewNetwork(ctx context.Context, t *testing.T) *testcontainers.DockerNetwork {
	t.Helper()
	n, err := tcnet.New(ctx)
	if err != nil {
		t.Fatalf("create network: %v", err)
	}
	return n
}

// StartDrps starts the drps container from the repo Dockerfile.
// Network alias: "drps". Exposes 17000/tcp (frpc) and 18080/tcp (vhost HTTP).
func StartDrps(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	root := RepoRoot(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    root,
				Dockerfile: "Dockerfile",
				KeepImage:  true,
			},
			Env: map[string]string{
				"DRPS_TOKEN": AuthToken,
			},
			ExposedPorts: []string{"17000/tcp", "18080/tcp"},
			Networks:     []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"drps"},
			},
			WaitingFor: wait.ForLog("drps listening").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start drps: %v", err)
	}
	return c
}

// StartFrps starts the frps v0.68.0 container from test/Dockerfile.frps
// with the embedded baseline TOML mounted at /etc/frp/frps.toml.
// Network alias: "frps".
func StartFrps(ctx context.Context, t *testing.T, netName string, baseline []byte) testcontainers.Container {
	t.Helper()
	root := RepoRoot(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    filepath.Join(root, "test"),
				Dockerfile: "Dockerfile.frps",
				KeepImage:  true,
			},
			Cmd:          []string{"-c", "/etc/frp/frps.toml"},
			ExposedPorts: []string{"17000/tcp", "18080/tcp"},
			Networks:     []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"frps"},
			},
			Files: []testcontainers.ContainerFile{
				{
					ContainerFilePath: "/etc/frp/frps.toml",
					Reader:            strings.NewReader(string(baseline)),
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForLog("frps started successfully").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start frps: %v", err)
	}
	return c
}

// StartFrpc starts an frpc container pointed at `serverAlias` ("drps" or "frps").
// The given frpcToml replaces {{SERVER}} markers with the alias.
func StartFrpc(ctx context.Context, t *testing.T, netName, serverAlias, frpcToml, waitLog string) testcontainers.Container {
	t.Helper()
	root := RepoRoot(t)
	rendered := strings.ReplaceAll(frpcToml, "{{SERVER}}", serverAlias)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    filepath.Join(root, "test"),
				Dockerfile: "Dockerfile.frpc",
				KeepImage:  true,
			},
			Cmd:      []string{"-c", "/etc/frpc.toml"},
			Networks: []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"frpc-" + serverAlias},
			},
			Files: []testcontainers.ContainerFile{
				{
					ContainerFilePath: "/etc/frpc.toml",
					Reader:            strings.NewReader(rendered),
					FileMode:          0o644,
				},
			},
			WaitingFor: wait.ForLog(waitLog).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start frpc(%s): %v", serverAlias, err)
	}
	return c
}

// StartNginx starts a stock nginx:alpine with network alias "backend".
func StartNginx(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:    "nginx:alpine",
			Networks: []string{netName},
			NetworkAliases: map[string][]string{
				netName: {"backend"},
			},
			WaitingFor: wait.ForHTTP("/").WithPort("80/tcp").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nginx: %v", err)
	}
	return c
}

// StartWSEcho builds test/ws-echo/Dockerfile and registers network alias "ws-echo".
func StartWSEcho(ctx context.Context, t *testing.T, netName string) testcontainers.Container {
	t.Helper()
	root := RepoRoot(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    filepath.Join(root, "test", "ws-echo"),
				Dockerfile: "Dockerfile",
				KeepImage:  true,
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

// TerminateAll cleanly stops every container and removes the network.
// Executes directly; does NOT return a closure.
func TerminateAll(ctx context.Context, t *testing.T, net *testcontainers.DockerNetwork, containers ...testcontainers.Container) {
	t.Helper()
	for _, c := range containers {
		if c == nil {
			continue
		}
		if err := c.Terminate(ctx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	}
	if net != nil {
		if err := net.Remove(ctx); err != nil {
			t.Logf("remove network: %v", err)
		}
	}
}

// Endpoint resolves the mapped host:port for a container's internal port.
func Endpoint(ctx context.Context, t *testing.T, c testcontainers.Container, internal string) string {
	t.Helper()
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, nat.Port(internal))
	if err != nil {
		t.Fatalf("mapped port %s: %v", internal, err)
	}
	return fmt.Sprintf("%s:%s", host, port.Port())
}
