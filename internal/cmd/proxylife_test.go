package cmd

import (
	"errors"
	"reflect"
	"strings"
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

// inspectStub is a dx.Executor whose Output answers `docker image inspect`
// with a fixed aisb.proxy-source label, or fails as if the image did not exist.
type inspectStub struct {
	label   string
	missing bool
}

func (s inspectStub) Output(...string) (string, error) {
	if s.missing {
		return "", errors.New("No such image")
	}
	return s.label + "\n", nil
}
func (inspectStub) Run(...string) error                  { return nil }
func (inspectStub) RunSilent(...string) error            { return nil }
func (inspectStub) Replace(...string) error              { return nil }
func (inspectStub) RunWithStdin([]byte, ...string) error { return nil }

// The proxy image is checked before the master password is asked for. An image
// built from different proxy source than this aisb carries (an older aisb, or
// none recorded) is out of date; a missing one is reported as such. Both say
// to run `aisb build`, which needs no repository checkout.
func TestCheckProxyImage(t *testing.T) {
	cases := []struct {
		name    string
		stub    inspectStub
		wantErr string
	}{
		{"current", inspectStub{label: "abc123"}, ""},
		{"missing", inspectStub{missing: true}, "not found"},
		{"other version", inspectStub{label: "old999"}, "out of date"},
		{"unlabelled", inspectStub{label: "<no value>"}, "out of date"},
	}
	for _, c := range cases {
		err := checkProxyImage(c.stub, "ai-sandbox-proxy:latest", "abc123")
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), "aisb build") {
			t.Errorf("%s: error = %v, want it to say %q and `aisb build`", c.name, err, c.wantErr)
		}
	}
}
