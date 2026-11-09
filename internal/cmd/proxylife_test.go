package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/proxy"
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

// removeRecorder reports the containers in running as existing and records
// `docker rm` targets.
type removeRecorder struct{ running, removed []string }

func (r *removeRecorder) Output(args ...string) (string, error) {
	if len(args) > 0 && args[0] == "ps" {
		return strings.Join(r.running, "\n"), nil
	}
	return "", nil
}
func (r *removeRecorder) Run(...string) error { return nil }
func (r *removeRecorder) RunSilent(args ...string) error {
	if len(args) > 2 && args[0] == "rm" {
		r.removed = append(r.removed, args[len(args)-1])
	}
	return nil
}
func (r *removeRecorder) Replace(...string) error              { return nil }
func (r *removeRecorder) RunWithStdin([]byte, ...string) error { return nil }

// The proxy's name (container, and the host name sandboxes reach it by) is
// "aisb-proxy" for the default config, can be set with AISB_PROXY_NAME, and
// otherwise differs per config directory, so two setups (separate secret
// stores) never share, stop or reconfigure one another's proxy.
func TestProxyName_DefaultOverrideAndPerConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: "/sh",
		Proxy: &cfg.ProxyBlock{Allow: []string{"example.com"}}}
	nameUsed := func() (spec, stopped string) {
		t.Helper()
		s := buildSpec("aisb-foo", c, home, "/work", nil, "/host/ca.crt", noopWarner{})
		spec = strings.TrimSuffix(strings.TrimPrefix(s.Env["HTTPS_PROXY"], "http://"), ":8080")
		// Another setup's proxy runs alongside; stop must leave it alone.
		r := &removeRecorder{running: []string{spec, "aisb-proxy-other-setup"}}
		if err := (Handler{Log: &captureLog{}, Docker: r}).ProxyStop(); err != nil {
			t.Fatal(err)
		}
		return spec, strings.Join(r.removed, ",")
	}

	t.Setenv("AISB_CONFIG_FILE", "")
	t.Setenv("AISB_PROXY_NAME", "")
	if spec, stopped := nameUsed(); spec != "aisb-proxy" || stopped != "aisb-proxy" {
		t.Fatalf("default config: sandbox uses %q, stop removes %q, want aisb-proxy for both", spec, stopped)
	}

	t.Setenv("AISB_CONFIG_FILE", filepath.Join(home, ".config", "ai-sandbox", "config.json"))
	if spec, _ := nameUsed(); spec != "aisb-proxy" {
		t.Fatalf("default config named explicitly: sandbox uses %q, want aisb-proxy", spec)
	}

	t.Setenv("AISB_CONFIG_FILE", filepath.Join(t.TempDir(), "config.json"))
	spec, stopped := nameUsed()
	if spec == "aisb-proxy" || !strings.HasPrefix(spec, "aisb-proxy-") || stopped != spec {
		t.Fatalf("other config dir: sandbox uses %q, stop removes %q, want one shared aisb-proxy-<id> name", spec, stopped)
	}

	t.Setenv("AISB_CONFIG_FILE", filepath.Join(t.TempDir(), "config.json"))
	if other, _ := nameUsed(); other == spec || !strings.HasPrefix(other, "aisb-proxy-") {
		t.Fatalf("two config dirs share proxy name %q", other)
	}

	t.Setenv("AISB_PROXY_NAME", "aisb-proxy-alt")
	if spec, stopped := nameUsed(); spec != "aisb-proxy-alt" || stopped != "aisb-proxy-alt" {
		t.Fatalf("AISB_PROXY_NAME: sandbox uses %q, stop removes %q, want aisb-proxy-alt", spec, stopped)
	}
}

// An "oauth" block in config.json reaches the proxy's rules: the token
// endpoint host is handled (and so reachable) even in a "*" project.
func TestRulesForProject_OAuthFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"default": {"proxy": {"allow": ["*"],
		"oauth": {"auth.openai.com": {"path": "/oauth/token", "access": "codex", "refresh": "codex_refresh"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := cfg.Resolve(path, "/p", cfg.Effective{})
	rs, err := rulesForProject(c, "10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	want := proxy.OAuthRule{Path: "/oauth/token", Access: "codex", Refresh: "codex_refresh"}
	if rs.OAuth["auth.openai.com"] != want {
		t.Fatalf("oauth rules = %#v, want %#v for auth.openai.com", rs.OAuth, want)
	}
	if !proxy.NewAllowlist(rs.Projects[0].Allow).Explicit("auth.openai.com:443") {
		t.Fatalf("allow %#v does not name the token host", rs.Projects[0].Allow)
	}
}
