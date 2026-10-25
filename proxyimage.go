package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aktech/ai-sandbox/internal/cmd"
)

// proxySource is everything `docker build -f Dockerfile.proxy` needs, carried
// inside the aisb binary so the proxy image can be built without a checkout
// of this repository. TestProxyBuildContext_Compiles guards that it is complete.
//
//go:embed Dockerfile.proxy go.mod go.sum cmd/aisb-proxy/*.go internal/proxy/*.go
var proxySource embed.FS

// writeProxyContext writes the embedded proxy build context into dir.
func writeProxyContext(dir string) error {
	return fs.WalkDir(proxySource, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := proxySource.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

// proxySourceHash identifies the embedded proxy source. It is stamped on the
// proxy image as cmd.ProxySourceLabel when aisb builds it, and compared before
// starting the proxy, so an image built by a different aisb is caught.
func proxySourceHash() string {
	h := sha256.New()
	_ = fs.WalkDir(proxySource, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := proxySource.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildProxyImage builds the proxy image from the embedded source, streaming
// docker's output so the user sees progress.
func buildProxyImage(log *logger) error {
	tmp, err := os.MkdirTemp("", "aisb-proxy-build-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := writeProxyContext(tmp); err != nil {
		return fmt.Errorf("write proxy build context: %w", err)
	}
	image := cmd.ProxyImage()
	log.Step("building " + image + " (the credential proxy)")
	build := exec.Command("docker", "build",
		"-f", filepath.Join(tmp, "Dockerfile.proxy"),
		"--label", cmd.ProxySourceLabel+"="+proxySourceHash(),
		"-t", image, tmp)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return err
	}
	log.OK("built " + image)
	return nil
}

// proxyImageExists reports whether any proxy image is present locally, so
// `aisb build` refreshes one left behind by an older aisb.
func proxyImageExists() bool {
	return exec.Command("docker", "image", "inspect", cmd.ProxyImage()).Run() == nil
}

// proxyNextSteps tells a user who just ran `aisb proxy init` what to do next.
func proxyNextSteps(log *logger, configFile string) {
	log.OK("proxy mode is set up. Next:")
	fmt.Println(`  1. Store each API key under a name you choose (you will be asked for the
     master password, then the value):
       aisb secret set github
  2. Add a "proxy" block to ` + configFile + `
     listing the hosts sandboxes may reach and which header each key goes in.
     Example and reference:
       https://github.com/aktech/ai-sandbox#keeping-credentials-out-of-the-sandbox
  3. Run aisb in your project. It starts the proxy and asks for the master
     password once (set AISB_MASTER_PASSWORD to skip the prompt).`)
}
