// Package schema defines the compat test scenario YAML schema v1.
//
// Schema v1 covers HTTP/1.1 + WebSocket + SSE + chunked HTTP. HTTP/2, gRPC,
// QUIC, and raw TCP require schema v2 (new top-level `protocol:` discriminator).
//
// This package has NO dependencies on testcontainers-go, filesystem access, or
// any network code. It is a pure types + parser module — deliberately leaf-only
// to prevent import cycles.
package schema

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario is the top-level YAML structure for one compat test case.
type Scenario struct {
	Name               string       `yaml:"name"`
	Description        string       `yaml:"description,omitempty"`
	Protocol           string       `yaml:"protocol,omitempty"` // reserved for v2; default "http1"
	Upstream           UpstreamSpec `yaml:"upstream"`
	Frpc               FrpcSpec     `yaml:"frpc"`
	FrpsOverride       string       `yaml:"frps_config_override,omitempty"`
	Request            RequestSpec  `yaml:"request"`
	Expect             ExpectSpec   `yaml:"expect"`
	ExpectedDivergence []Divergence `yaml:"expected_divergence,omitempty"`
}

// UpstreamSpec identifies the backend kind and optional nginx config.
type UpstreamSpec struct {
	Kind      string `yaml:"kind"` // nginx | nginx-chunked | nginx-basic-auth | ws-echo | sse-echo | http-echo
	NginxConf string `yaml:"nginx_conf,omitempty"`
}

// FrpcSpec describes the frpc configuration for a scenario.
type FrpcSpec struct {
	AuthToken string      `yaml:"auth_token,omitempty"` // default "compat-token"
	Proxies   []FrpcProxy `yaml:"proxies"`
}

// FrpcProxy is a single [[proxies]] block in frpc.toml.
type FrpcProxy struct {
	Name          string   `yaml:"name"`
	Type          string   `yaml:"type"` // "http" only in v1
	LocalIP       string   `yaml:"localIP"`
	LocalPort     int      `yaml:"localPort"`
	CustomDomains []string `yaml:"customDomains"`
}

// RequestSpec describes the HTTP request to execute against both targets.
type RequestSpec struct {
	Method    string            `yaml:"method"`
	Host      string            `yaml:"host"`
	Path      string            `yaml:"path"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Body      string            `yaml:"body,omitempty"`
	BasicAuth *BasicAuth        `yaml:"basic_auth,omitempty"`
	Streaming string            `yaml:"streaming,omitempty"` // "" | chunked | sse | websocket
	WSFrames  []string          `yaml:"ws_frames,omitempty"`
}

// BasicAuth holds Basic Auth credentials applied via http.Request.SetBasicAuth.
type BasicAuth struct {
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

// ExpectSpec describes the expected response mode and sanity-check thresholds.
type ExpectSpec struct {
	Mode          string `yaml:"mode"` // http | chunked | sse | websocket
	Status        int    `yaml:"status,omitempty"`
	MinBodyBytes  int    `yaml:"min_body_bytes,omitempty"`
	SSEEventCount int    `yaml:"sse_event_count,omitempty"`
	WSFrameCount  int    `yaml:"ws_frame_count,omitempty"`
}

// Divergence is a declared allow-list entry: a known, intentional difference
// between drps and frps responses that should NOT fail the compat check.
type Divergence struct {
	Kind   string `yaml:"kind"` // http_header | body | status_code | content_type
	Name   string `yaml:"name,omitempty"`
	Drps   string `yaml:"drps"`
	Frps   string `yaml:"frps"`
	Reason string `yaml:"reason"`
}

// Parse decodes a YAML byte slice into a Scenario, with strict unknown-field
// rejection. This keeps the schema locked: a typo or a field from schema v2
// (before it exists) causes an immediate parse error rather than silent skip.
func Parse(data []byte) (Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("parse scenario: %w", err)
	}
	return s, nil
}
