// Package compat contains the drps ↔ frps functional-equivalence test suite.
//
// Each scenario in scenarios/*.yaml is run against both drps and frps
// (process-based, no Docker required) and their responses are compared for
// semantic equivalence: status code, body hash, headers, WebSocket frames.
//
// The test infrastructure lives in framework/ and uses patterns adopted from
// frp's own E2E suite: exec.Command process management, mod-partitioned
// port allocation, in-process mock backends, and stdout readiness polling.
//
// Adding a scenario: drop a YAML file in scenarios/ and (if needed) add a
// backend kind in framework/backend.go. No runner code changes required.
package compat
