package cfg

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeCfg writes JSON to a temp file and returns its path.
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Resolve must fall back to mounting the current working directory when
// no config file exists. Without this, a fresh install creates a
// container with zero bind mounts and the user sees an empty workdir.
func TestResolve_MissingFile_FallsBackToCWD(t *testing.T) {
	got := Resolve(filepath.Join(t.TempDir(), "absent.json"), "/anywhere", Effective{})

	want := []string{"{{CWD}}"}
	if !reflect.DeepEqual(got.Mounts, want) {
		t.Fatalf("Mounts = %#v, want %#v", got.Mounts, want)
	}
}

// A config that exists but lists no mounts (after merging Default + the
// project entry) must still fall back to CWD. Without this, a user who
// defines `extra_mounts` only would silently lose the project mount.
func TestResolve_EmptyMountsInConfig_FallsBackToCWD(t *testing.T) {
	path := writeCfg(t, `{"default": {"extra_mounts": ["/etc"]}}`)
	got := Resolve(path, "/anywhere", Effective{})

	want := []string{"{{CWD}}"}
	if !reflect.DeepEqual(got.Mounts, want) {
		t.Fatalf("Mounts = %#v, want %#v", got.Mounts, want)
	}
}

// When the user lists explicit mounts, the fallback must not fire — even
// if {{CWD}} is absent. Adding it would change a user-authored list and
// surprise people who deliberately scope their sandbox.
func TestResolve_ExplicitMounts_NoFallback(t *testing.T) {
	path := writeCfg(t, `{"default": {"mounts": ["/srv/data"]}}`)
	got := Resolve(path, "/anywhere", Effective{})

	want := []string{"/srv/data"}
	if !reflect.DeepEqual(got.Mounts, want) {
		t.Fatalf("Mounts = %#v, want %#v", got.Mounts, want)
	}
}

// gpus set in the global default block must flow through to Effective so a
// single config entry exposes GPUs to every project's sandbox.
func TestResolve_GPUs_DefaultBlock(t *testing.T) {
	path := writeCfg(t, `{"default": {"gpus": "all"}}`)
	got := Resolve(path, "/anywhere", Effective{})

	if got.GPUs != "all" {
		t.Fatalf("GPUs = %q, want %q", got.GPUs, "all")
	}
}

// A project-level gpus entry must override the default — same precedence as
// memory/cpus/image.
func TestResolve_GPUs_ProjectOverridesDefault(t *testing.T) {
	path := writeCfg(t, `{
		"default":  {"gpus": "all"},
		"projects": {"/work/foo": {"gpus": "device=0"}}
	}`)
	got := Resolve(path, "/work/foo", Effective{})

	if got.GPUs != "device=0" {
		t.Fatalf("GPUs = %q, want %q", got.GPUs, "device=0")
	}
}

// An empty/absent gpus must leave the base value untouched (so AISB_GPUS or the
// no-GPU default survives a config that doesn't mention gpus).
func TestResolve_GPUs_AbsentKeepsBase(t *testing.T) {
	path := writeCfg(t, `{"default": {"memory": "8g"}}`)
	got := Resolve(path, "/anywhere", Effective{GPUs: "all"})

	if got.GPUs != "all" {
		t.Fatalf("GPUs = %q, want %q (base must survive)", got.GPUs, "all")
	}
}

// Project-keyed mounts count too — fallback must not fire when only the
// project entry supplies mounts.
func TestResolve_ProjectOnlyMounts_NoFallback(t *testing.T) {
	path := writeCfg(t, `{
		"default":  {},
		"projects": {"/work/foo": {"mounts": ["/work/foo/src"]}}
	}`)
	got := Resolve(path, "/work/foo", Effective{})

	want := []string{"/work/foo/src"}
	if !reflect.DeepEqual(got.Mounts, want) {
		t.Fatalf("Mounts = %#v, want %#v", got.Mounts, want)
	}
}

// A project key may be a glob: "**" matches any descendant path so a single
// rule can route everything under a directory (e.g. all of ~/work).
func TestResolve_GlobProjectKey_MatchesDescendants(t *testing.T) {
	path := writeCfg(t, `{
      "default":  {"mounts": ["/base"]},
      "projects": {"/srv/work/**": {"extra_mounts": ["/work-creds"]}}
    }`)
	got := Resolve(path, "/srv/work/foo/bar", Effective{})
	if !contains(got.ExtraMounts, "/work-creds") {
		t.Fatalf("expected /work-creds for a descendant, got %#v", got.ExtraMounts)
	}
	// A non-matching path must not pick up the work rule.
	got2 := Resolve(path, "/srv/personal/x", Effective{})
	if contains(got2.ExtraMounts, "/work-creds") {
		t.Fatalf("did not expect /work-creds for /srv/personal/x, got %#v", got2.ExtraMounts)
	}
}

// A single-star glob matches one path segment (filepath.Match semantics), so
// "~/darb-clones/nebari-*" matches a timestamped clone but not a nested path.
func TestResolve_StarProjectKey_MatchesOneSegment(t *testing.T) {
	path := writeCfg(t, `{
      "projects": {"/clones/nebari-*": {"extra_mounts": ["/work-creds"]}}
    }`)
	if got := Resolve(path, "/clones/nebari-claude-20260604", Effective{}); !contains(got.ExtraMounts, "/work-creds") {
		t.Fatalf("expected match, got %#v", got.ExtraMounts)
	}
	if got := Resolve(path, "/clones/cirun-claude-1", Effective{}); contains(got.ExtraMounts, "/work-creds") {
		t.Fatalf("did not expect match for cirun, got %#v", got.ExtraMounts)
	}
}

// Ports declared at the default level and the matching project entry must
// both reach Effective.Ports so a server inside the sandbox can be published
// to the host. Default ports come first, project ports appended after.
func TestResolve_PortsMergeDefaultAndProject(t *testing.T) {
	path := writeCfg(t, `{
		"default":  {"ports": ["9000:9000"]},
		"projects": {"/work/foo": {"ports": ["3000:3000"]}}
	}`)
	got := Resolve(path, "/work/foo", Effective{})

	want := []string{"9000:9000", "3000:3000"}
	if !reflect.DeepEqual(got.Ports, want) {
		t.Fatalf("Ports = %#v, want %#v", got.Ports, want)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
