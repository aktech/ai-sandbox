// Package cfg loads the per-project aisb config and merges it with
// caller-provided defaults into a single Effective config.
//
// File schema (~/.config/ai-sandbox/config.json):
//
//	{
//	  "default":  { "memory": "8g", "mounts": ["{{CWD}}"], "extra_mounts": [...] },
//	  "projects": { "/path/to/project": { "memory": "16g", "extra_mounts": [...] } }
//	}
//
// Resolve returns the Effective config used to create a sandbox. If the
// file is missing or its `mounts` array is empty after merging Default
// and the project override, Resolve falls back to mounting the current
// working directory — without it a fresh install would create a
// container with zero bind mounts and an empty workdir.
package cfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project mirrors one entry under "default" or "projects.<path>" in the
// JSON file. CPUs is `any` because users write either 4 or "4".
type Project struct {
	Memory      string   `json:"memory,omitempty"`
	CPUs        any      `json:"cpus,omitempty"`
	Image       string   `json:"image,omitempty"`
	Mounts      []string `json:"mounts,omitempty"`       // declarative mount list (replaces defaults)
	ExtraMounts []string `json:"extra_mounts,omitempty"` // appended after mounts
	Copies      []string `json:"copies,omitempty"`       // one-way copies (like mounts, but with no back-propagation)
	ExtraCopies []string `json:"extra_copies,omitempty"` // appended after copies
	Ports       []string `json:"ports,omitempty"`        // "host:container" port publishes
}

// File is the top-level JSON document.
type File struct {
	Default  Project            `json:"default"`
	Projects map[string]Project `json:"projects"`
}

// Effective is the merged config a caller actually uses. Mounts/ExtraMounts
// hold raw strings (with placeholders); expansion is the mountresolver's job.
type Effective struct {
	Image       string
	Memory      string
	CPUs        string
	SharedDir   string
	Mounts      []string
	ExtraMounts []string
	Copies      []string
	Ports       []string
}

// Resolve loads `path`, merges Default and the project entry keyed by
// `project` into `base`, and returns the result. A missing or unreadable
// file is treated as empty config (base wins). A malformed file logs a
// warning to stderr and is treated as empty.
func Resolve(path, project string, base Effective) Effective {
	cfg := base
	data, err := os.ReadFile(path)
	if err != nil {
		return withDefaults(cfg) // missing file is fine
	}
	var raw File
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "warn: %s parse failed: %v\n", path, err)
		return withDefaults(cfg)
	}
	apply := func(p Project) {
		if p.Memory != "" {
			cfg.Memory = p.Memory
		}
		if p.Image != "" {
			cfg.Image = p.Image
		}
		if p.CPUs != nil {
			cfg.CPUs = fmt.Sprintf("%v", p.CPUs)
		}
		cfg.Mounts = append(cfg.Mounts, p.Mounts...)
		cfg.ExtraMounts = append(cfg.ExtraMounts, p.ExtraMounts...)
		cfg.Copies = append(cfg.Copies, p.Copies...)
		cfg.Copies = append(cfg.Copies, p.ExtraCopies...)
		cfg.Ports = append(cfg.Ports, p.Ports...)
	}
	apply(raw.Default)
	// Apply every project entry whose key matches `project`, least-specific
	// (shortest key) first so a more-specific rule's mounts come later and win
	// the by-destination dedupe in the mountresolver.
	for _, key := range matchingKeys(raw.Projects, project) {
		apply(raw.Projects[key])
	}
	return withDefaults(cfg)
}

// matchingKeys returns the project keys that match path, sorted by key length
// ascending (specificity). A key may be an exact path, a "src/*" glob
// (filepath.Match: one segment), or a "src/**" recursive prefix matching any
// descendant. "~" at the start of a key expands to the user's home.
func matchingKeys(projects map[string]Project, path string) []string {
	var matched []string
	for key := range projects {
		if matchProject(expandHome(key), path) {
			matched = append(matched, key)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return len(matched[i]) < len(matched[j]) })
	return matched
}

func matchProject(pattern, path string) bool {
	if base, ok := strings.CutSuffix(pattern, "/**"); ok {
		return path == base || strings.HasPrefix(path, base+"/")
	}
	ok, _ := filepath.Match(pattern, path) // exact when the pattern has no glob metachars
	return ok
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// withDefaults guarantees the resolved config has at least one bind mount.
// Empty Mounts after merging Default + project means the user did not list
// any — fall back to the current working directory so the project itself
// is visible inside the sandbox.
func withDefaults(c Effective) Effective {
	if len(c.Mounts) == 0 {
		c.Mounts = []string{"{{CWD}}"}
	}
	return c
}
