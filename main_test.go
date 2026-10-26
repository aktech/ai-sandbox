package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// Agent versions live only in the Dockerfile. aisb build must not pass its own
// default (a stale copy there is what kept pi old); it passes a version only
// when the user sets one, e.g. PI_VERSION=v1.5.0 aisb build.
func TestAgentVersionBuildArgs(t *testing.T) {
	t.Setenv("PI_VERSION", "")
	t.Setenv("CODEX_VERSION", "")
	if got := agentVersionBuildArgs(); len(got) != 0 {
		t.Fatalf("no overrides: got %v, want none (Dockerfile defaults apply)", got)
	}

	t.Setenv("PI_VERSION", "v9.9.9")
	t.Setenv("CODEX_VERSION", "1.2.3")
	got := strings.Join(agentVersionBuildArgs(), " ")
	want := "--build-arg PI_VERSION=v9.9.9 --build-arg CODEX_VERSION=1.2.3"
	if got != want {
		t.Fatalf("overrides: got %q, want %q", got, want)
	}
}
