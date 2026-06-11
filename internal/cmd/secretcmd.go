package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/aktech/ai-sandbox/internal/ca"
	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/dx"
	"github.com/aktech/ai-sandbox/internal/proxy"
	"github.com/aktech/ai-sandbox/internal/secret"
	"golang.org/x/term"
)

// configDir is where psb keeps the secret store and CA. It follows the config
// file's directory (PSB_CONFIG_FILE) so everything lives together.
func configDir() string {
	if p := os.Getenv("PSB_CONFIG_FILE"); p != "" {
		return filepath.Dir(p)
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "ai-sandbox")
}

func secretsPath() string { return filepath.Join(configDir(), "secrets.enc") }
func caCertPath() string  { return filepath.Join(configDir(), "ca.crt") }
func caKeyPath() string   { return filepath.Join(configDir(), "ca.key") }

// masterPassword returns the store password from PSB_MASTER_PASSWORD if set
// (the non-interactive path), otherwise prompts without echoing.
func masterPassword(prompt string) ([]byte, error) {
	if v := os.Getenv("PSB_MASTER_PASSWORD"); v != "" {
		return []byte(v), nil
	}
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return pw, err
}

// assemblePayload builds the JSON the proxy reads: CA cert+key, the decrypted
// secrets, and the allow/inject config. config.allow is the effective allow
// list (inject hosts are auto-allowed) so a host you inject into is reachable.
func assemblePayload(c cfg.Effective, secrets map[string]string, caCert, caKey string) ([]byte, error) {
	if c.Proxy == nil {
		return nil, fmt.Errorf("no proxy config")
	}
	// Compute the effective allow list via the proxy parser.
	rawCfg, err := json.Marshal(map[string]any{"allow": c.Proxy.Allow, "inject": c.Proxy.Inject})
	if err != nil {
		return nil, err
	}
	pc, err := proxy.ParseConfig(rawCfg)
	if err != nil {
		return nil, err
	}
	outCfg := map[string]any{
		"allow":  pc.EffectiveAllow(),
		"inject": c.Proxy.Inject,
	}
	return json.Marshal(map[string]any{
		"ca_cert": caCert,
		"ca_key":  caKey,
		"secrets": secrets,
		"config":  outCfg,
	})
}

// proxyPayload loads the secret store and CA, then assembles the proxy payload.
// Returns the payload bytes and the host path of the CA cert (to mount into the
// sandbox).
func (h Handler) proxyPayload(c cfg.Effective) (payload []byte, caHostPath string, err error) {
	certPEM, err := os.ReadFile(caCertPath())
	if err != nil {
		return nil, "", fmt.Errorf("read CA cert (run `psb proxy init`): %w", err)
	}
	keyPEM, err := os.ReadFile(caKeyPath())
	if err != nil {
		return nil, "", fmt.Errorf("read CA key (run `psb proxy init`): %w", err)
	}
	pw, err := masterPassword("master password: ")
	if err != nil {
		return nil, "", err
	}
	secrets, err := secret.Load(secretsPath(), pw)
	if err != nil {
		return nil, "", err
	}
	payload, err = assemblePayload(c, secrets, string(certPEM), string(keyPEM))
	if err != nil {
		return nil, "", err
	}
	return payload, caCertPath(), nil
}

// ProxyInit generates the CA and an empty secret store. Idempotent guard: it
// refuses to overwrite an existing CA so secrets already shared with sandboxes
// stay valid.
func (h Handler) ProxyInit() error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(caCertPath()); err == nil {
		return fmt.Errorf("CA already exists at %s; remove it to regenerate", caCertPath())
	}
	certPEM, keyPEM, err := ca.Generate("psb proxy CA")
	if err != nil {
		return err
	}
	if err := os.WriteFile(caCertPath(), certPEM, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(caKeyPath(), keyPEM, 0o600); err != nil {
		return err
	}
	pw, err := masterPassword("set master password: ")
	if err != nil {
		return err
	}
	if err := secret.Save(secretsPath(), pw, map[string]string{}); err != nil {
		return err
	}
	h.Log.OK("proxy initialized: CA + empty secret store in " + configDir())
	return nil
}

// SecretSet reads a value for name (no echo) and stores it.
func (h Handler) SecretSet(name string) error {
	pw, err := masterPassword("master password: ")
	if err != nil {
		return err
	}
	secrets, err := secret.Load(secretsPath(), pw)
	if err != nil {
		return err
	}
	val, err := masterPassword(fmt.Sprintf("value for %q: ", name))
	if err != nil {
		return err
	}
	secrets[name] = string(val)
	if err := secret.Save(secretsPath(), pw, secrets); err != nil {
		return err
	}
	h.Log.OK("stored secret " + name)
	return nil
}

// SecretRM deletes a secret by name.
func (h Handler) SecretRM(name string) error {
	pw, err := masterPassword("master password: ")
	if err != nil {
		return err
	}
	secrets, err := secret.Load(secretsPath(), pw)
	if err != nil {
		return err
	}
	delete(secrets, name)
	if err := secret.Save(secretsPath(), pw, secrets); err != nil {
		return err
	}
	h.Log.OK("removed secret " + name)
	return nil
}

// SecretLS prints stored secret names (never values).
func (h Handler) SecretLS() error {
	pw, err := masterPassword("master password: ")
	if err != nil {
		return err
	}
	secrets, err := secret.Load(secretsPath(), pw)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// ProxyStop removes the shared proxy container.
func (h Handler) ProxyStop() error {
	if !dx.ContainerExists(h.Docker, proxyContainer) {
		h.Log.Warn("proxy not running")
		return nil
	}
	if err := dx.Remove(h.Docker, proxyContainer); err != nil {
		return err
	}
	h.Log.OK("proxy stopped")
	return nil
}

// ProxyLog streams the proxy container's logs.
func (h Handler) ProxyLog() error {
	return h.Docker.Run("logs", "-f", proxyContainer)
}
