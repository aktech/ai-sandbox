package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

// noopWarner satisfies mountresolver.Warner for tests.
type noopWarner struct{}

func (noopWarner) Warn(string) {}

// rawInject parses a JSON object into the inject map shape cfg holds.
func rawInject(s string) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic(err)
	}
	return m
}

func TestBuildSpec_NoProxy_PassesRealKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "real-key")
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: "/sh",
		Mounts: []string{"/work"}}
	spec := buildSpec("aisb-foo", c, "/home/u", "/work", nil, "", noopWarner{})

	if spec.Env["ANTHROPIC_API_KEY"] != "real-key" {
		t.Fatalf("no-proxy must pass real key, got %q", spec.Env["ANTHROPIC_API_KEY"])
	}
	if spec.Network != "" {
		t.Fatalf("no-proxy must not set a network, got %q", spec.Network)
	}
	if spec.Env["HTTPS_PROXY"] != "" {
		t.Fatalf("no-proxy must not set HTTPS_PROXY, got %q", spec.Env["HTTPS_PROXY"])
	}
}

// The configured gpus value must reach the container spec so `docker run`
// gets --gpus, in both plain and proxy mode.
func TestBuildSpec_PassesGPUs(t *testing.T) {
	for _, proxy := range []*cfg.ProxyBlock{nil, {Allow: []string{"example.com"}}} {
		c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", GPUs: "all",
			SharedDir: "/sh", Proxy: proxy}
		spec := buildSpec("aisb-foo", c, "/home/u", "/work", nil, "/host/ca.crt", noopWarner{})

		if spec.GPUs != "all" {
			t.Fatalf("proxy=%v: GPUs = %q, want %q", proxy != nil, spec.GPUs, "all")
		}
	}
}

func TestBuildSpec_Proxy_UsesSentinelAndInternalNet(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "real-key")
	t.Setenv("MY_CUSTOM_TOKEN", "real-custom")
	// The sentinel env vars are declared in config (proxy.env), not hardcoded:
	// secret name -> the env var the sandbox sets to the sentinel.
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: "/sh",
		Mounts: []string{"/work"},
		Proxy: &cfg.ProxyBlock{
			Allow:  []string{"api.anthropic.com"},
			Env:    map[string]string{"anthropic": "ANTHROPIC_API_KEY", "custom": "MY_CUSTOM_TOKEN"},
			Inject: rawInject(`{"api.anthropic.com":{"header":"x-api-key","secret":"anthropic"}}`),
		}}
	spec := buildSpec("aisb-foo", c, "/home/u", "/work", nil, "/host/ca.crt", noopWarner{})

	if spec.Env["ANTHROPIC_API_KEY"] != "__aisb_anthropic__" {
		t.Fatalf("proxy must use sentinel, got %q", spec.Env["ANTHROPIC_API_KEY"])
	}
	// A wholly custom secret name maps to its declared env var, no hardcoding.
	if spec.Env["MY_CUSTOM_TOKEN"] != "__aisb_custom__" {
		t.Fatalf("custom secret sentinel = %q, want __aisb_custom__", spec.Env["MY_CUSTOM_TOKEN"])
	}
	if spec.Network != "aisb-net-foo" {
		t.Fatalf("Network = %q, want aisb-net-foo", spec.Network)
	}
	if spec.Env["HTTPS_PROXY"] == "" {
		t.Fatal("HTTPS_PROXY must be set under proxy mode")
	}
	caMounted := false
	for _, m := range spec.Mounts {
		if strings.Contains(m, "/host/ca.crt") {
			caMounted = true
		}
	}
	if !caMounted {
		t.Fatalf("CA cert must be mounted, mounts=%#v", spec.Mounts)
	}
	if spec.Env["SSL_CERT_FILE"] == "" || spec.Env["NODE_EXTRA_CA_CERTS"] == "" {
		t.Fatal("CA env vars must point at the mounted cert")
	}
}

// runFails is a dx.Executor where every image exists, no container exists yet
// and `docker run` fails.
type runFails struct{}

func (runFails) Output(...string) (string, error)     { return "", nil }
func (runFails) Run(...string) error                  { return errors.New("exit status 125") }
func (runFails) RunSilent(...string) error            { return nil }
func (runFails) Replace(...string) error              { return nil }
func (runFails) RunWithStdin([]byte, ...string) error { return nil }

// A project outside $HOME (e.g. under /private/tmp or /Volumes) is mounted
// like any other. When creating the container fails, the error names the
// mounts outside $HOME and says how to share them with a VM-based Docker
// (colima shares only $HOME by default), since that is the usual cause.
func TestCreate_FailureExplainsMountsOutsideHome(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: filepath.Join(home, "sh"),
		Mounts: []string{"{{CWD}}"}}
	h := Handler{Log: &captureLog{}, Docker: runFails{}}

	err := h.Create("aisb-x", c, home, outside, nil)
	if err == nil {
		t.Fatal("Create succeeded though docker run failed")
	}
	for _, want := range []string{outside, "outside your home", "colima"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	inside := filepath.Join(home, "proj")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	err = h.Create("aisb-y", c, home, inside, nil)
	if err == nil || strings.Contains(err.Error(), "outside your home") {
		t.Fatalf("project inside $HOME: error = %v, want docker's failure without the sharing hint", err)
	}
}
