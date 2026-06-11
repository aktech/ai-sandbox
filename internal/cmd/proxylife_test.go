package cmd

import (
	"reflect"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

func TestNetworkName(t *testing.T) {
	if got := networkName("psb-foo"); got != "psb-net-foo" {
		t.Fatalf("networkName = %q", got)
	}
}

func TestProxyRunArgs(t *testing.T) {
	got := proxyRunArgs("psb-proxy", "ai-sandbox-proxy:latest")
	want := []string{"run", "-d", "--name", "psb-proxy", "--network", "bridge",
		"--tmpfs", "/run/psb:rw,mode=1777,size=1m", "ai-sandbox-proxy:latest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxyRunArgs = %#v\nwant %#v", got, want)
	}
}

func TestWritePayloadExecArgs(t *testing.T) {
	got := writePayloadExecArgs("psb-proxy")
	want := []string{"exec", "-i", "psb-proxy", "/psb-proxy", "--write-payload"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("writePayloadExecArgs = %#v\nwant %#v", got, want)
	}
}

func TestWriteRulesExecArgs(t *testing.T) {
	got := writeRulesExecArgs("psb-proxy")
	want := []string{"exec", "-i", "psb-proxy", "/psb-proxy", "--write-rules"}
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
	got := internalNetCreateArgs("psb-net-foo")
	want := []string{"network", "create", "--internal", "psb-net-foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("internalNetCreateArgs = %#v\nwant %#v", got, want)
	}
}

func TestNetConnectDisconnectArgs(t *testing.T) {
	if got := netConnectArgs("psb-net-foo", "psb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "connect", "psb-net-foo", "psb-proxy"}) {
		t.Fatalf("netConnectArgs = %#v", got)
	}
	if got := netDisconnectArgs("psb-net-foo", "psb-proxy"); !reflect.DeepEqual(got,
		[]string{"network", "disconnect", "psb-net-foo", "psb-proxy"}) {
		t.Fatalf("netDisconnectArgs = %#v", got)
	}
}
