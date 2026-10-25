package cmd

import (
	"reflect"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

func TestNetworkName(t *testing.T) {
	if got := networkName("aisb-foo"); got != "aisb-net-foo" {
		t.Fatalf("networkName = %q", got)
	}
}

func TestProxyRunArgs(t *testing.T) {
	got := proxyRunArgs("aisb-proxy", "ai-sandbox-proxy:latest")
	want := []string{"run", "-d", "--name", "aisb-proxy", "--network", "bridge",
		"--tmpfs", "/run/aisb:rw,mode=1777,size=1m", "ai-sandbox-proxy:latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxyRunArgs = %#v\nwant %#v", got, want)
	}
}

func TestWritePayloadExecArgs(t *testing.T) {
	got := writePayloadExecArgs("aisb-proxy")
	want := []string{"exec", "-i", "aisb-proxy", "/aisb-proxy", "--write-payload"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writePayloadExecArgs = %#v\nwant %#v", got, want)
	}
}

func TestWriteRulesExecArgs(t *testing.T) {
	got := writeRulesExecArgs("aisb-proxy", "aisb-proja")
	want := []string{"exec", "-i", "aisb-proxy", "/aisb-proxy", "--write-rules", "aisb-proja"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writeRulesExecArgs = %#v\nwant %#v", got, want)
	}
}

func TestRulesForProject(t *testing.T) {
	c := cfg.Effective{Proxy: &cfg.ProxyBlock{
		Allow:  []string{"api.anthropic.com"},
		Inject: rawInject(`{"api.github.com":{"header":"Authorization","secret":"github","format":"Bearer %s"}}`),
	}}
	rs, err := rulesForProject(c, "172.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Projects) != 1 || rs.Projects[0].CIDR != "172.20.0.0/16" {
		t.Fatalf("subnet wrong: %#v", rs.Projects)
	}
	// EffectiveAllow includes the inject host (api.github.com) plus api.anthropic.com.
	allow := rs.Projects[0].Allow
	hasAnthropic, hasGithub := false, false
	for _, h := range allow {
		if h == "api.anthropic.com" {
			hasAnthropic = true
		}
		if h == "api.github.com" {
			hasGithub = true
		}
	}
	if !hasAnthropic || !hasGithub {
		t.Fatalf("allow missing entries: %#v", allow)
	}
	if _, ok := rs.Inject["api.github.com"]; !ok {
		t.Fatalf("inject rule missing: %#v", rs.Inject)
	}
}

// An unrestricted project ("*") must produce a subnet allow containing "*".
func TestRulesForProject_Unrestricted(t *testing.T) {
	c := cfg.Effective{Proxy: &cfg.ProxyBlock{Allow: []string{"*"}}}
	rs, err := rulesForProject(c, "10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range rs.Projects[0].Allow {
		if h == "*" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '*' in allow, got %#v", rs.Projects[0].Allow)
	}
}

func TestInternalNetCreateArgs(t *testing.T) {
	got := internalNetCreateArgs("aisb-net-foo")
	want := []string{"network", "create", "--internal", "aisb-net-foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("internalNetCreateArgs = %#v\nwant %#v", got, want)
	}
}

func TestNetConnectDisconnectArgs(t *testing.T) {
	if got := netConnectArgs("aisb-net-foo", "aisb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "connect", "aisb-net-foo", "aisb-proxy"}) {
		t.Fatalf("netConnectArgs = %#v", got)
	}
	if got := netDisconnectArgs("aisb-net-foo", "aisb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "disconnect", "aisb-net-foo", "aisb-proxy"}) {
		t.Fatalf("netDisconnectArgs = %#v", got)
	}
}
