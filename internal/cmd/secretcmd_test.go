package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
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
