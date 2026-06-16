// Package mountresolver turns a project's declarative mount config into
// the final, ordered, deduped, existence-filtered list of "src:dest" specs
// used for `-v` arguments to docker. Entries are "src" (dest defaults to
// src) or "src:dest" to remap the path inside the container.
//
// One entry point: Resolve.
package mountresolver

import (
	"os"
	"path/filepath"
	"strings"
)

// CopySpec describes one one-way file copy operation.
// Source is the expanded host path, Dest is the expanded container path.
type CopySpec struct {
	Source string
	Dest   string
}

// Env holds the environment values the resolver substitutes into mount
// strings. Values are pre-resolved by the caller — the resolver never
// reads os.Getenv itself.
type Env struct {
	Home      string
	CWD       string
	SharedDir string
}

// Warner receives one warning per missing mount path. May be nil.
type Warner interface {
	Warn(string)
}

// Resolve expands placeholders ({{HOME}}, {{CWD}}, {{SHARED_DIR}}, ~/, $VAR),
// dedupes each bucket independently, filters via os.Stat on the source, and
// returns "src:dest" strings ready for `docker -v`. log may be nil.
//
// Each config entry is either "src" (dest defaults to src — the historical
// behavior) or "src:dest" to mount the host path at a different path inside
// the container. Both sides are expanded independently.
//
// Mounts and extras are concatenated in that order. The full mount list is
// expected to come from config; this package does not supply defaults.
func Resolve(mounts, extraMounts []string, env Env, log Warner) []string {
	all := append(expandAll(mounts, env), expandAll(extraMounts, env)...)
	return filterExisting(dedupeByDest(all), log)
}

func expandAll(in []string, env Env) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, expand(s, env))
	}
	return out
}

// ResolveCopies expands placeholders in copy entries, deduplicates by source
// path, and filters out missing sources. Each entry follows the same syntax
// as mount entries: "src" (same path in the container) or "src:dest" (remap).
func ResolveCopies(copies []string, env Env, log Warner) []CopySpec {
	expanded := expandAll(copies, env)

	// Dedupe by source path (keep first occurrence).
	seen := make(map[string]bool, len(expanded))
	var specs []CopySpec
	for _, spec := range expanded {
		src, dst, _ := strings.Cut(spec, ":")
		if seen[src] {
			continue
		}
		seen[src] = true
		specs = append(specs, CopySpec{Source: src, Dest: dst})
	}

	// Filter missing sources.
	out := make([]CopySpec, 0, len(specs))
	for _, cp := range specs {
		if _, err := os.Stat(cp.Source); err != nil {
			if log != nil {
				log.Warn("skip missing copy source: " + cp.Source)
			}
			continue
		}
		out = append(out, cp)
	}
	return out
}

// expand turns one raw config entry into a "src:dest" spec. An entry without
// a colon mounts the source at the same path inside the container; "src:dest"
// remaps it. Each side is expanded independently — host paths never contain a
// colon on the platforms aisb targets, so the first colon delimits the two.
func expand(s string, env Env) string {
	src, dst, hasDst := strings.Cut(s, ":")
	src = expandPath(src, env)
	if !hasDst {
		return src + ":" + src
	}
	return src + ":" + expandPath(dst, env)
}

func expandPath(s string, env Env) string {
	s = strings.ReplaceAll(s, "{{HOME}}", env.Home)
	s = strings.ReplaceAll(s, "{{SHARED_DIR}}", env.SharedDir)
	s = strings.ReplaceAll(s, "{{CWD}}", env.CWD)
	if strings.HasPrefix(s, "~/") {
		s = filepath.Join(env.Home, s[2:])
	} else if s == "~" {
		s = env.Home
	}
	return os.ExpandEnv(s)
}

// dedupeByDest keeps one spec per container destination, preferring the LAST
// occurrence so a project (or extra) mount overrides an earlier default mount
// that targets the same path inside the container. Relative order is preserved.
func dedupeByDest(list []string) []string {
	lastAt := make(map[string]int, len(list))
	for i, m := range list {
		_, dst, _ := strings.Cut(m, ":") // src has no colon; dest is the remainder
		lastAt[dst] = i
	}
	out := make([]string, 0, len(list))
	for i, m := range list {
		_, dst, _ := strings.Cut(m, ":")
		if lastAt[dst] == i {
			out = append(out, m)
		}
	}
	return out
}

func filterExisting(specs []string, log Warner) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		src, _, _ := strings.Cut(spec, ":")
		if _, err := os.Stat(src); err != nil {
			if log != nil {
				log.Warn("skip missing mount: " + src)
			}
			continue
		}
		out = append(out, spec)
	}
	return out
}
