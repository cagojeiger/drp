package compat

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kangheeyong/drp/test/compat/schema"
)

//go:embed scenarios/*.yaml
var scenariosFS embed.FS

// FrpsBaseline is the baseline frps.toml content, embedded at compile time.
//
// This embed directive lives here (package compat) — not under launcher/ —
// because Go's //go:embed cannot traverse parent directories (`..`). The
// launcher package accepts the baseline via MustFrpsConfig(s, baseline)
// parameter injection.
//
//go:embed fixtures/frps.toml
var FrpsBaseline []byte

// LoadAllScenarios reads all embedded scenario YAMLs and parses them into
// the typed Scenario schema. Returns scenarios in lexicographic filename order
// so test subtest names are deterministic.
func LoadAllScenarios() ([]schema.Scenario, error) {
	entries, err := scenariosFS.ReadDir("scenarios")
	if err != nil {
		return nil, fmt.Errorf("read embedded scenarios: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]schema.Scenario, 0, len(names))
	for _, name := range names {
		data, err := scenariosFS.ReadFile(filepath.Join("scenarios", name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		s, err := schema.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out = append(out, s)
	}
	return out, nil
}
