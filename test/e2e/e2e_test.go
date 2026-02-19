package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cagojeiger/drp/internal/client"
	"github.com/cagojeiger/drp/internal/server"
	"github.com/cagojeiger/drp/internal/transport"
)

func TestMeshRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen: %v", err)
	}
	defer backendLn.Close()
	backendAddr := backendLn.Addr().String()

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprint(w, "hello from backend")
		})
		srv := &http.Server{Handler: mux}
		srv.Serve(backendLn)
	}()

	srvA := server.New(server.Config{NodeID: "A", HTTPPort: 0, ControlPort: 0})
	go srvA.Run(ctx)
	waitReady(t, srvA.Ready(), "drps-A")
	httpA, ctrlA := srvA.Addr()

	srvB := server.New(server.Config{NodeID: "B", HTTPPort: 0, ControlPort: 0, Peers: ctrlA})
	go srvB.Run(ctx)
	waitReady(t, srvB.Ready(), "drps-B")
	httpB, ctrlB := srvB.Addr()

	srvC := server.New(server.Config{NodeID: "C", HTTPPort: 0, ControlPort: 0, Peers: ctrlB})
	go srvC.Run(ctx)
	waitReady(t, srvC.Ready(), "drps-C")
	httpC, _ := srvC.Addr()

	drpc := client.New(client.Config{
		ServerAddr: ctrlA,
		Alias:      "myapp",
		Hostname:   "myapp.example.com",
		LocalAddr:  backendAddr,
	}, transport.TCP{})
	go drpc.Run(ctx)
	waitReady(t, drpc.Ready(), "drpc")

	time.Sleep(500 * time.Millisecond)

	tests := []struct {
		name       string
		httpAddr   string
		host       string
		wantStatus int
	}{
		{"H1_LocalHit", httpA, "myapp.example.com", 200},
		{"H2_OneHopRelay", httpB, "myapp.example.com", 200},
		{"H3_TwoHopRelay", httpC, "myapp.example.com", 200},
		{"F1_UnknownHostA", httpA, "unknown.example.com", 502},
		{"F2a_UnknownHostB", httpB, "unknown.example.com", 502},
		{"F2b_UnknownHostC", httpC, "unknown.example.com", 502},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := &http.Client{
				Timeout: 5 * time.Second,
				Transport: &http.Transport{
					DisableKeepAlives: true,
				},
			}
			url := fmt.Sprintf("http://%s/", tt.httpAddr)
			req, err := http.NewRequest("GET", url, http.NoBody)
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			req.Host = tt.host

			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatalf("HTTP request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("got status %d, want %d (body: %s)", resp.StatusCode, tt.wantStatus, string(body))
			}

			if tt.wantStatus == 200 {
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), "hello from backend") {
					t.Errorf("unexpected body: %s", string(body))
				}
			}
		})
	}
}

func waitReady(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not become ready within 5s", name)
	}
}
