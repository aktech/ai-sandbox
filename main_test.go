package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
