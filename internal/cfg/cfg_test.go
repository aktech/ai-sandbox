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

func TestResolve_ProxyBlockCarried(t *testing.T) {
	path := writeCfg(t, `{
		"default": {"proxy": {"allow": ["api.anthropic.com"]}},
		"projects": {"/work/foo": {"extra_allow": ["pypi.org"]}}
	}`)
	got := Resolve(path, "/work/foo", Effective{})

	if got.Proxy == nil {
		t.Fatal("Proxy should be set")
	}
	want := []string{"api.anthropic.com", "pypi.org"}
	if !reflect.DeepEqual(got.Proxy.Allow, want) {
		t.Fatalf("Proxy.Allow = %#v, want %#v", got.Proxy.Allow, want)
	}
}

// A project's own proxy.allow REPLACES the universal allow (so a project can
// be narrowed below a permissive default like ["*"]). extra_allow still appends.
func TestResolve_ProjectAllowReplacesUniversal(t *testing.T) {
	path := writeCfg(t, `{
		"default":  {"proxy": {"allow": ["*"]}},
		"projects": {"/work/locked": {"proxy": {"allow": ["only.com"]}, "extra_allow": ["plus.com"]}}
	}`)
	got := Resolve(path, "/work/locked", Effective{})
	want := []string{"only.com", "plus.com"}
	if !reflect.DeepEqual(got.Proxy.Allow, want) {
		t.Fatalf("Proxy.Allow = %#v, want %#v (project allow must replace universal)", got.Proxy.Allow, want)
	}
}

// A project with only extra_allow keeps the universal allow and appends to it.
func TestResolve_ExtraAllowAppendsToUniversal(t *testing.T) {
	path := writeCfg(t, `{
		"default":  {"proxy": {"allow": ["base.com"]}},
		"projects": {"/work/x": {"extra_allow": ["more.com"]}}
	}`)
	got := Resolve(path, "/work/x", Effective{})
	want := []string{"base.com", "more.com"}
	if !reflect.DeepEqual(got.Proxy.Allow, want) {
		t.Fatalf("Proxy.Allow = %#v, want %#v", got.Proxy.Allow, want)
	}
}

func TestResolve_NoProxyBlock_NilProxy(t *testing.T) {
	path := writeCfg(t, `{"default": {"mounts": ["/x"]}}`)
	got := Resolve(path, "/x", Effective{})
	if got.Proxy != nil {
		t.Fatalf("Proxy should be nil when unconfigured, got %#v", got.Proxy)
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
