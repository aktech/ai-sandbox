package mountresolver

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type warnings []string

func (w *warnings) Warn(s string) { *w = append(*w, s) }

// A project or mount may live in any existing directory, inside or outside
// $HOME, but never the host's root or a system directory: those are skipped
// with a warning that says why.
func TestResolve_RefusesRootAndSystemDirs(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // a temp dir, not under home
	var w warnings

	got := Resolve([]string{"/", "/etc", "/usr", "/dev", "/proc", "{{CWD}}"}, nil,
		Env{Home: home, CWD: outside}, &w)
	want := []string{outside + ":" + outside}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want only the project dir %v", got, want)
	}
	for _, p := range []string{"/", "/etc", "/usr", "/dev"} {
		found := false
		for _, msg := range w {
			if strings.Contains(msg, "system") && strings.HasSuffix(msg, " "+p) {
				found = true
			}
		}
		if _, err := os.Stat(p); err == nil && !found {
			t.Errorf("no warning naming %s as a system path; warnings: %v", p, w)
		}
	}
}

// A symlink that points at a system directory is refused like the directory.
func TestResolve_RefusesSymlinkToSystemDir(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(home, "etc-link")
	if err := os.Symlink("/etc", link); err != nil {
		t.Skip("cannot create symlink:", err)
	}
	if got := Resolve([]string{link}, nil, Env{Home: home}, nil); len(got) != 0 {
		t.Fatalf("symlink to /etc mounted: %v", got)
	}
}
