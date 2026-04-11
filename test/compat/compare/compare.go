// Package compare performs semantic equivalence checks between drps and frps
// captured responses. Practical-minimum version: status, body hash, Content-Type.
package compare

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kangheeyong/drp/test/compat/capture"
	"github.com/kangheeyong/drp/test/compat/schema"
)

// Diff represents one difference between two responses.
type Diff struct {
	Kind string // status | body | content_type | ws_frame_count | ws_frame | capture_err
	Name string // field label (e.g. "Content-Type", "frame[2]")
	Drps string
	Frps string
}

// Report is the comparator output for one scenario.
type Report struct {
	Scenario string
	Mode     string
	Diffs    []Diff
	Matched  []schema.Divergence // allow-list entries that consumed a diff
	Missing  []schema.Divergence // allow-list entries that were declared but not seen
}

// Pass reports whether the comparison is clean after allow-list application.
func (r *Report) Pass() bool {
	return len(r.Diffs) == 0 && len(r.Missing) == 0
}

// Summary returns a one-line human summary.
func (r *Report) Summary() string {
	if r.Pass() {
		return fmt.Sprintf("%s: PASS (mode=%s)", r.Scenario, r.Mode)
	}
	return fmt.Sprintf("%s: FAIL (mode=%s) diffs=%d missing=%d",
		r.Scenario, r.Mode, len(r.Diffs), len(r.Missing))
}

// Render returns a multi-line human-readable report.
func (r *Report) Render() string {
	var b strings.Builder
	b.WriteString(r.Summary())
	b.WriteString("\n")
	for _, d := range r.Diffs {
		fmt.Fprintf(&b, "  - DIFF  [%s] %s: drps=%q frps=%q\n", d.Kind, d.Name, truncate(d.Drps, 80), truncate(d.Frps, 80))
	}
	for _, m := range r.Matched {
		fmt.Fprintf(&b, "  - ALLOW [%s] %s (reason: %s)\n", m.Kind, m.Name, m.Reason)
	}
	for _, m := range r.Missing {
		fmt.Fprintf(&b, "  - MISS  [%s] %s declared but not seen\n", m.Kind, m.Name)
	}
	return b.String()
}

// HTTP compares two non-streaming HTTP captures.
func HTTP(scenarioName string, drps, frps *capture.CapturedResponse, allow []schema.Divergence) *Report {
	r := &Report{Scenario: scenarioName, Mode: "http"}

	if drps.Err != nil || frps.Err != nil {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "capture_err",
			Name: "capture",
			Drps: errString(drps.Err),
			Frps: errString(frps.Err),
		})
		return applyAllowList(r, allow)
	}

	if drps.Status != frps.Status {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "status",
			Name: "status",
			Drps: fmt.Sprintf("%d", drps.Status),
			Frps: fmt.Sprintf("%d", frps.Status),
		})
	}

	drpsHash := sha256.Sum256(drps.Body)
	frpsHash := sha256.Sum256(frps.Body)
	if drpsHash != frpsHash {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "body",
			Name: fmt.Sprintf("body(%d/%d bytes)", len(drps.Body), len(frps.Body)),
			Drps: hex.EncodeToString(drpsHash[:8]),
			Frps: hex.EncodeToString(frpsHash[:8]),
		})
	}

	drpsCT := firstHeader(drps.Headers, "Content-Type")
	frpsCT := firstHeader(frps.Headers, "Content-Type")
	if drpsCT != frpsCT {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "content_type",
			Name: "Content-Type",
			Drps: drpsCT,
			Frps: frpsCT,
		})
	}

	return applyAllowList(r, allow)
}

// WebSocket compares two WS captures: upgrade status + frame list.
func WebSocket(scenarioName string, drps, frps *capture.CapturedResponse, allow []schema.Divergence) *Report {
	r := &Report{Scenario: scenarioName, Mode: "websocket"}

	if drps.Err != nil || frps.Err != nil {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "capture_err",
			Name: "capture",
			Drps: errString(drps.Err),
			Frps: errString(frps.Err),
		})
		return applyAllowList(r, allow)
	}

	if drps.Status != frps.Status {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "status",
			Name: "upgrade",
			Drps: fmt.Sprintf("%d", drps.Status),
			Frps: fmt.Sprintf("%d", frps.Status),
		})
	}

	if len(drps.WSFrames) != len(frps.WSFrames) {
		r.Diffs = append(r.Diffs, Diff{
			Kind: "ws_frame_count",
			Name: "frames",
			Drps: fmt.Sprintf("%d", len(drps.WSFrames)),
			Frps: fmt.Sprintf("%d", len(frps.WSFrames)),
		})
	}

	n := len(drps.WSFrames)
	if len(frps.WSFrames) < n {
		n = len(frps.WSFrames)
	}
	for i := 0; i < n; i++ {
		d := drps.WSFrames[i]
		f := frps.WSFrames[i]
		if d.Opcode != f.Opcode || string(d.Payload) != string(f.Payload) {
			r.Diffs = append(r.Diffs, Diff{
				Kind: "ws_frame",
				Name: fmt.Sprintf("frame[%d]", i),
				Drps: fmt.Sprintf("op=0x%02x %q", d.Opcode, string(d.Payload)),
				Frps: fmt.Sprintf("op=0x%02x %q", f.Opcode, string(f.Payload)),
			})
		}
	}

	return applyAllowList(r, allow)
}

// applyAllowList consumes diffs that match declared divergence entries and
// records entries that were declared but never seen (missing = silent drift).
func applyAllowList(r *Report, allow []schema.Divergence) *Report {
	if len(allow) == 0 {
		return r
	}
	remaining := make([]Diff, 0, len(r.Diffs))
	used := make([]bool, len(allow))

	for _, d := range r.Diffs {
		matched := false
		for i, a := range allow {
			if used[i] {
				continue
			}
			if divergenceMatches(a, d) {
				r.Matched = append(r.Matched, a)
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			remaining = append(remaining, d)
		}
	}
	for i, a := range allow {
		if !used[i] {
			r.Missing = append(r.Missing, a)
		}
	}
	r.Diffs = remaining
	return r
}

func divergenceMatches(a schema.Divergence, d Diff) bool {
	switch a.Kind {
	case "status_code":
		return d.Kind == "status" && d.Drps == a.Drps && d.Frps == a.Frps
	case "body":
		return d.Kind == "body"
	case "content_type", "http_header":
		if d.Kind != "content_type" {
			return false
		}
		if a.Name != "" && !strings.EqualFold(a.Name, d.Name) {
			return false
		}
		return d.Drps == a.Drps && d.Frps == a.Frps
	}
	return false
}

func firstHeader(h map[string][]string, key string) string {
	if vs, ok := h[key]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func errString(e error) string {
	if e == nil {
		return "<nil>"
	}
	return e.Error()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
