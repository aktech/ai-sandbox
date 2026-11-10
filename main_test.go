package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/integrity"
)

// The proxy build context embedded in the aisb binary must be complete: a new
// user builds the proxy image from it with no repository checkout, so if the
// proxy gains an import that the embed list misses, this fails here instead of
// in their `docker build`.
func TestProxyBuildContext_Compiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeProxyContext(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile.proxy")); err != nil {
		t.Fatalf("context has no Dockerfile.proxy: %v", err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "aisb-proxy"), "./cmd/aisb-proxy")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("embedded proxy source does not build: %v\n%s", err, out)
	}
}

// emptySandbox holds no files, so Verify can only tell pinned from unpinned.
type emptySandbox struct{ arch string }

func (e emptySandbox) Arch() (string, error) { return e.arch, nil }
func (emptySandbox) Open(string) (io.ReadCloser, error) {
	return nil, errors.New("not found")
}
func (emptySandbox) Lookup([]string) (map[string]string, error) { return nil, nil }

// `aisb verify` reads its pins from the Dockerfile embedded in aisb. Every
// agent it checks must be pinned there for both image architectures and
// installed at the path it reads, or verify would report every sandbox as
// unpinned or missing.
func TestEmbeddedDockerfilePinsEveryVerifiedAgent(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		rep, err := integrity.Verify(embeddedDockerfile, emptySandbox{arch: arch})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			if r.Status == integrity.Unpinned {
				t.Errorf("%s/%s: %s", r.Agent, arch, r.Detail)
			}
		}
	}
	for _, a := range integrity.Agents {
		if !strings.Contains(string(embeddedDockerfile), a.Path) {
			t.Errorf("Dockerfile never mentions %s, where verify reads %s", a.Path, a.Name)
		}
	}
}
