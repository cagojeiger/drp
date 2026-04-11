package schema

import (
	"strings"
	"testing"
)

func TestParse_HappyPath(t *testing.T) {
	data := []byte(`name: test
upstream:
  kind: nginx
frpc:
  proxies:
    - name: web
      type: http
      localIP: backend
      localPort: 80
      customDomains: ["test.local"]
request:
  method: GET
  host: test.local
  path: /
expect:
  mode: http
  status: 200
`)
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "test" {
		t.Errorf("name = %q, want %q", s.Name, "test")
	}
	if s.Upstream.Kind != "nginx" {
		t.Errorf("upstream.kind = %q, want nginx", s.Upstream.Kind)
	}
	if len(s.Frpc.Proxies) != 1 {
		t.Fatalf("proxies len = %d, want 1", len(s.Frpc.Proxies))
	}
	if s.Frpc.Proxies[0].LocalIP != "backend" {
		t.Errorf("localIP = %q, want backend", s.Frpc.Proxies[0].LocalIP)
	}
	if s.Expect.Mode != "http" {
		t.Errorf("expect.mode = %q, want http", s.Expect.Mode)
	}
}

func TestParse_UnknownFieldRejected(t *testing.T) {
	data := []byte(`name: test
unknown_field: should-fail
upstream:
  kind: nginx
frpc:
  proxies: []
request:
  method: GET
  host: test.local
  path: /
expect:
  mode: http
`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected parse error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "unknown_field") && !strings.Contains(err.Error(), "field unknown_field") {
		t.Logf("error: %v", err)
		// Strict mode varies in error wording; just require an error.
	}
}

func TestParse_BasicAuth(t *testing.T) {
	data := []byte(`name: auth-test
upstream:
  kind: nginx
frpc:
  proxies: []
request:
  method: GET
  host: test.local
  path: /
  basic_auth:
    user: admin
    pass: secret
expect:
  mode: http
`)
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Request.BasicAuth == nil {
		t.Fatal("basic_auth nil")
	}
	if s.Request.BasicAuth.User != "admin" || s.Request.BasicAuth.Pass != "secret" {
		t.Errorf("basic_auth = %+v, want {admin secret}", s.Request.BasicAuth)
	}
}

func TestParse_ExpectedDivergence(t *testing.T) {
	data := []byte(`name: div-test
upstream:
  kind: nginx
frpc:
  proxies: []
request:
  method: GET
  host: test.local
  path: /
expect:
  mode: http
expected_divergence:
  - kind: body
    drps: "x"
    frps: "y"
    reason: test
`)
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.ExpectedDivergence) != 1 {
		t.Fatalf("divergence len = %d, want 1", len(s.ExpectedDivergence))
	}
	if s.ExpectedDivergence[0].Kind != "body" {
		t.Errorf("divergence kind = %q, want body", s.ExpectedDivergence[0].Kind)
	}
}
