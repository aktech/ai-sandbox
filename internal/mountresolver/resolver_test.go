package mountresolver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ---------- ResolveCopies tests ----------

// A bare copy entry (no colon) copies from the expanded source to the same
// path inside the container.
func TestResolveCopies_BareEntry_SrcEqualsDest(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "cache")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveCopies([]string{"{{HOME}}/cache"}, Env{Home: home}, nil)
	want := []CopySpec{{Source: src, Dest: src}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// "src:dest" remaps the path inside the container and expands both sides
// independently. Only the source must exist on the host.
func TestResolveCopies_SrcDest_RemapsPath(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "pip-cache")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveCopies([]string{"{{HOME}}/pip-cache:{{HOME}}/.cache/pip"},
		Env{Home: home}, nil)
	want := []CopySpec{{
		Source: src,
		Dest:   filepath.Join(home, ".cache", "pip"),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// A missing source path must be skipped with a warning rather than causing
// an error.
func TestResolveCopies_MissingSource_Skipped(t *testing.T) {
	got := ResolveCopies([]string{"/no/such/path:/dest"}, Env{}, nil)
	if len(got) != 0 {
		t.Fatalf("got %#v, want empty", got)
	}
}

// When the same source appears twice, only the first occurrence is kept.
func TestResolveCopies_DedupeBySource(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "cache")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveCopies([]string{"{{HOME}}/cache", "{{HOME}}/cache:{{HOME}}/other"},
		Env{Home: home}, nil)
	if len(got) != 1 {
		t.Fatalf("got %d specs, want 1 (deduped by source)", len(got))
	}
	// First occurrence's dest is kept.
	if got[0].Dest != src {
		t.Fatalf("Dest = %q, want %q (first occurrence's dest)", got[0].Dest, src)
	}
}

// Multiple different sources all targeting the same destination must all be
// included (different sources, not deduped).
func TestResolveCopies_DifferentSources_AllKept(t *testing.T) {
	home := t.TempDir()
	src1 := filepath.Join(home, "cache1")
	src2 := filepath.Join(home, "cache2")
	if err := os.MkdirAll(src1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src2, 0o755); err != nil {
		t.Fatal(err)
	}

	got := ResolveCopies([]string{"{{HOME}}/cache1", "{{HOME}}/cache2"},
		Env{Home: home}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d specs, want 2 (different sources)", len(got))
	}
}

// ---------- existing mount-resolver tests below ----------

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A bare entry (no colon) mounts the host path at the same path in the
// container — the historical behavior — after placeholder expansion.
func TestResolve_BareEntry_MountsSrcToSrc(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "file")
	touch(t, src)

	got := Resolve([]string{"{{HOME}}/file"}, nil, Env{Home: home}, nil)
	want := []string{src + ":" + src}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// "src:dest" remaps the path inside the container and expands both sides
// independently. Only the source must exist on the host.
func TestResolve_SrcDest_RemapsAndExpandsBothSides(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	src := filepath.Join(cwd, "sandbox.gitconfig")
	touch(t, src)

	got := Resolve(
		[]string{"{{CWD}}/sandbox.gitconfig:{{HOME}}/.gitconfig"},
		nil, Env{Home: home, CWD: cwd}, nil,
	)
	want := []string{src + ":" + filepath.Join(home, ".gitconfig")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// Existence is checked against the source only; a missing source drops the
// whole entry even when a dest is given.
func TestResolve_MissingSrc_Skipped(t *testing.T) {
	got := Resolve([]string{"/no/such/path:/dest"}, nil, Env{}, nil)
	if len(got) != 0 {
		t.Fatalf("got %#v, want empty", got)
	}
}

// A later entry that targets the same destination overrides an earlier one,
// so a project's mount can replace a default mount (e.g. a different Claude
// account) without docker rejecting a duplicate mount point.
func TestResolve_LaterDestOverridesEarlier(t *testing.T) {
	home := t.TempDir()
	personal := filepath.Join(home, "personal")
	work := filepath.Join(home, "work")
	touch(t, personal)
	touch(t, work)

	got := Resolve(
		[]string{personal + ":/h/.creds"}, // default mount
		[]string{work + ":/h/.creds"},      // project extra_mount, same dest -> wins
		Env{Home: home}, nil)
	want := []string{work + ":/h/.creds"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
