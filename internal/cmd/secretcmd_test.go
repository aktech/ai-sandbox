package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/secret"
)

func TestSecretsPayload(t *testing.T) {
	payload, err := secretsPayload(map[string]string{"anthropic": "real"}, "CERTPEM", "KEYPEM")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CACert  string            `json:"ca_cert"`
		CAKey   string            `json:"ca_key"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.CACert != "CERTPEM" || got.CAKey != "KEYPEM" || got.Secrets["anthropic"] != "real" {
		t.Fatalf("payload wrong: %s", payload)
	}
	// The secrets payload must NOT carry allow/inject config (that goes in the
	// separate, secret-free rules payload).
	if json.Valid(payload) && string(payload) != "" {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(payload, &raw)
		if _, ok := raw["config"]; ok {
			t.Fatalf("secrets payload must not contain config: %s", payload)
		}
	}
}

// A wrong master password must say which store it failed to unlock and how to
// start over, not just that decryption failed.
func TestSecretLS_WrongPasswordExplainsRecovery(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AISB_CONFIG_FILE", filepath.Join(dir, "config.json"))
	store := filepath.Join(dir, "secrets.enc")
	if err := secret.Save(store, []byte("right"), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AISB_MASTER_PASSWORD", "wrong")

	err := Handler{}.SecretLS()
	if err == nil {
		t.Fatal("SecretLS with wrong password succeeded")
	}
	for _, want := range []string{"wrong master password", store, "aisb proxy init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Proxy mode with no `aisb proxy init` yet must say so and name the command,
// before anything else (image checks, password prompts) is attempted.
func TestCheckProxySetUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AISB_CONFIG_FILE", filepath.Join(dir, "config.json"))

	err := checkProxySetUp()
	if err == nil || !strings.Contains(err.Error(), "aisb proxy init") {
		t.Fatalf("not set up: error = %v, want it to name `aisb proxy init`", err)
	}
	if ProxySetUp() {
		t.Fatal("ProxySetUp() = true with no CA")
	}

	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkProxySetUp(); err != nil {
		t.Fatalf("set up: unexpected error %v", err)
	}
	if !ProxySetUp() {
		t.Fatal("ProxySetUp() = false with a CA present")
	}
}

// proxyStub is a dx.Executor that reports a running proxy container (when
// running is set) and records what is piped to it.
type proxyStub struct {
	running bool
	stdin   [][]byte
	argv    [][]string
}

func (s *proxyStub) Output(args ...string) (string, error) {
	if s.running && len(args) > 0 && args[0] == "ps" {
		return proxyContainer + "\n", nil
	}
	return "", nil
}
func (s *proxyStub) Run(...string) error       { return nil }
func (s *proxyStub) RunSilent(...string) error { return nil }
func (s *proxyStub) Replace(...string) error   { return nil }
func (s *proxyStub) RunWithStdin(in []byte, args ...string) error {
	s.stdin = append(s.stdin, in)
	s.argv = append(s.argv, args)
	return nil
}

// withStdin points os.Stdin at a pipe carrying data for the test's duration.
func withStdin(t *testing.T, data string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { w.WriteString(data); w.Close() }()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old; r.Close() })
}

// Changing a secret while the proxy runs hands the new values to it at once,
// so a refreshed token is used without `aisb proxy stop`. The update carries
// secrets only, not the CA key. With no proxy running nothing is sent.
func TestSecretSetAndRM_RefreshRunningProxy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AISB_CONFIG_FILE", filepath.Join(dir, "config.json"))
	t.Setenv("AISB_MASTER_PASSWORD", "pw-for-test")
	if err := secret.Save(filepath.Join(dir, "secrets.enc"), []byte("pw-for-test"),
		map[string]string{"github": "old-value"}); err != nil {
		t.Fatal(err)
	}
	log := &captureLog{}

	stopped := &proxyStub{}
	withStdin(t, "first-value\n")
	if err := (Handler{Log: log, Docker: stopped}).SecretSet("openai"); err != nil {
		t.Fatal(err)
	}
	if len(stopped.stdin) != 0 {
		t.Fatalf("no proxy running, but %d payload(s) were sent", len(stopped.stdin))
	}

	running := &proxyStub{running: true}
	withStdin(t, "new-value\n")
	if err := (Handler{Log: log, Docker: running}).SecretSet("github"); err != nil {
		t.Fatal(err)
	}
	if err := (Handler{Log: log, Docker: running}).SecretRM("openai"); err != nil {
		t.Fatal(err)
	}
	if len(running.stdin) != 2 {
		t.Fatalf("want one payload per change sent to the running proxy, got %d", len(running.stdin))
	}
	want := []map[string]string{
		{"github": "new-value", "openai": "first-value"},
		{"github": "new-value"},
	}
	for i, raw := range running.stdin {
		if !reflect.DeepEqual(running.argv[i], writeSecretsExecArgs(proxyContainer)) {
			t.Errorf("payload %d sent with argv %v", i, running.argv[i])
		}
		var p map[string]json.RawMessage
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if _, ok := p["ca_key"]; ok {
			t.Errorf("payload %d carries the CA key; a refresh needs secrets only", i)
		}
		var got map[string]string
		_ = json.Unmarshal(p["secrets"], &got)
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("payload %d secrets = %v, want %v", i, got, want[i])
		}
	}
}

type captureLog struct{ lines []string }

func (l *captureLog) Log(s string)  { l.lines = append(l.lines, s) }
func (l *captureLog) Step(s string) { l.lines = append(l.lines, s) }
func (l *captureLog) OK(s string)   { l.lines = append(l.lines, s) }
func (l *captureLog) Warn(s string) { l.lines = append(l.lines, s) }
