package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/proxy"
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
// pending is what the proxy's pending-token file holds; silent records
// RunSilent argv (acks, removal).
type proxyStub struct {
	running bool
	pending string
	oldPx   bool // a proxy too old to know --read-pending
	stdin   [][]byte
	argv    [][]string
	silent  [][]string
}

func (s *proxyStub) Output(args ...string) (string, error) {
	if s.running && len(args) > 0 && args[0] == "ps" {
		return proxyName() + "\n", nil
	}
	if s.running && reflect.DeepEqual(args, readPendingExecArgs(proxyName())) {
		if s.oldPx {
			return "", errors.New("exit status 1")
		}
		return s.pending, nil
	}
	return "", nil
}
func (s *proxyStub) Run(...string) error { return nil }
func (s *proxyStub) RunSilent(args ...string) error {
	s.silent = append(s.silent, args)
	return nil
}
func (s *proxyStub) Replace(...string) error { return nil }
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
		if !reflect.DeepEqual(running.argv[i], writeSecretsExecArgs(proxyName())) {
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

// storeWith creates a secret store holding secrets and points aisb at it.
func storeWith(t *testing.T, secrets map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AISB_CONFIG_FILE", filepath.Join(dir, "config.json"))
	t.Setenv("AISB_MASTER_PASSWORD", "pw-for-test")
	path := filepath.Join(dir, "secrets.enc")
	if err := secret.Save(path, []byte("pw-for-test"), secrets); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadStore(t *testing.T, path string) map[string]string {
	t.Helper()
	got, err := secret.Load(path, []byte("pw-for-test"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (s *proxyStub) acked() bool {
	want := ackPendingExecArgs(proxyName(), proxy.PendingSum([]byte(s.pending)))
	for _, a := range s.silent {
		if reflect.DeepEqual(a, want) {
			return true
		}
	}
	return false
}

// Tokens the proxy obtained by refreshing are saved in the encrypted store by
// `aisb secret sync`, and the proxy is told to drop exactly what was saved.
// No token value is printed.
func TestSecretSync_SavesRefreshedTokens(t *testing.T) {
	path := storeWith(t, map[string]string{"codex": "old-a", "codex_refresh": "old-r", "github": "gh"})
	stub := &proxyStub{running: true, pending: `{"codex":"new-a","codex_refresh":"new-r"}`}
	log := &captureLog{}
	if err := (Handler{Log: log, Docker: stub}).SecretSync(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"codex": "new-a", "codex_refresh": "new-r", "github": "gh"}
	if got := loadStore(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("store = %v, want %v", got, want)
	}
	if !stub.acked() {
		t.Fatalf("proxy was not told to drop the saved tokens; ran %v", stub.silent)
	}
	for _, l := range log.lines {
		if strings.Contains(l, "new-a") || strings.Contains(l, "new-r") {
			t.Fatalf("a token value was printed: %q", l)
		}
	}
}

// `aisb secret set` pulls refreshed tokens in first, so the full store it
// saves and hands back to the proxy does not roll them back.
func TestSecretSet_KeepsRefreshedTokens(t *testing.T) {
	path := storeWith(t, map[string]string{"codex_refresh": "old-r"})
	stub := &proxyStub{running: true, pending: `{"codex_refresh":"new-r"}`}
	withStdin(t, "gh-value\n")
	if err := (Handler{Log: &captureLog{}, Docker: stub}).SecretSet("github"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"codex_refresh": "new-r", "github": "gh-value"}
	if got := loadStore(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("store = %v, want %v", got, want)
	}
	var pushed struct{ Secrets map[string]string }
	_ = json.Unmarshal(stub.stdin[0], &pushed)
	if !reflect.DeepEqual(pushed.Secrets, want) {
		t.Fatalf("proxy was handed %v, want %v", pushed.Secrets, want)
	}
	if !stub.acked() {
		t.Fatal("proxy was not told to drop the saved tokens")
	}
}

// Stopping the proxy would lose tokens it refreshed (they live in its RAM),
// so `aisb proxy stop` saves them first.
func TestProxyStop_SavesRefreshedTokensFirst(t *testing.T) {
	path := storeWith(t, map[string]string{"codex_refresh": "old-r"})
	stub := &proxyStub{running: true, pending: `{"codex_refresh":"new-r"}`}
	if err := (Handler{Log: &captureLog{}, Docker: stub}).ProxyStop(); err != nil {
		t.Fatal(err)
	}
	if got := loadStore(t, path)["codex_refresh"]; got != "new-r" {
		t.Fatalf("store codex_refresh = %q, want the refreshed token", got)
	}
}

// A running proxy from an older aisb cannot report refreshed tokens (it holds
// none); that must not block changing secrets or stopping it.
func TestOldProxyDoesNotBlockSetOrStop(t *testing.T) {
	path := storeWith(t, map[string]string{})
	stub := &proxyStub{running: true, oldPx: true}
	withStdin(t, "v\n")
	if err := (Handler{Log: &captureLog{}, Docker: stub}).SecretSet("github"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if loadStore(t, path)["github"] != "v" {
		t.Fatal("secret not stored")
	}
	if err := (Handler{Log: &captureLog{}, Docker: stub}).ProxyStop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
