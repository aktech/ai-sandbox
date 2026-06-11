package cmd

import (
	"encoding/json"
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
	spec := buildSpec("psb-foo", c, "/home/u", "/work", nil, "", noopWarner{})

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

func TestBuildSpec_Proxy_UsesSentinelAndInternalNet(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "real-key")
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: "/sh",
		Mounts: []string{"/work"},
		Proxy: &cfg.ProxyBlock{
			Allow:  []string{"api.anthropic.com"},
			Inject: rawInject(`{"api.anthropic.com":{"header":"x-api-key","secret":"anthropic"}}`),
		}}
	spec := buildSpec("psb-foo", c, "/home/u", "/work", nil, "/host/ca.crt", noopWarner{})

	if spec.Env["ANTHROPIC_API_KEY"] != "__psb_anthropic__" {
		t.Fatalf("proxy must use sentinel, got %q", spec.Env["ANTHROPIC_API_KEY"])
	}
	if spec.Network != "psb-net-foo" {
		t.Fatalf("Network = %q, want psb-net-foo", spec.Network)
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
