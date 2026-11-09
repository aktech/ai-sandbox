package cmd

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

// credHome builds a fake home with credential stores, aisb's own config
// directory (CA key, secret store), a Unix socket standing in for a docker
// socket, and an ordinary project.
func credHome(t *testing.T) (home, sock, proj, cfgDir string) {
	home = t.TempDir()
	cfgDir = filepath.Join(home, ".config", "ai-sandbox")
	t.Setenv("AISB_CONFIG_FILE", filepath.Join(cfgDir, "config.json"))
	for _, d := range []string{".ssh", ".aws", ".codex", "proj", ".config/ai-sandbox/ssh-x"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{".codex/auth.json", ".npmrc", ".config/ai-sandbox/secrets.enc"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock = filepath.Join(os.TempDir(), "aisb-t.sock")
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close(); os.Remove(sock) })
	return home, sock, filepath.Join(home, "proj"), cfgDir
}

// In proxy mode the sandbox must hold no real secret, so mounts and copies
// that would hand it host credentials are refused: credential stores (SSH,
// cloud, registry and agent login files), aisb's own CA key and secret store,
// anything containing one (such as the whole home), and sockets (a docker
// socket is control of the host). Without proxy mode mounts are as configured.
func TestBuildSpec_ProxyRefusesCredentialMounts(t *testing.T) {
	home, sock, proj, cfgDir := credHome(t)
	risky := []string{"~/.ssh", "~/.aws", "~/.codex/auth.json", "~/.npmrc", home,
		cfgDir, cfgDir + "/ssh-x:" + home + "/.ssh", cfgDir + "/secrets.enc:/tmp/s", sock}
	c := cfg.Effective{Image: "img", Memory: "4g", CPUs: "2", SharedDir: filepath.Join(home, "sh"),
		Mounts: append([]string{proj}, risky...), Copies: append([]string{proj + ":/tmp/p"}, risky...),
		Proxy: &cfg.ProxyBlock{Allow: []string{"*"}}}

	spec := buildSpec("aisb-x", c, home, proj, nil, cfgDir+"/ca.crt", noopWarner{})
	want := []string{proj + ":" + proj, cfgDir + "/ca.crt:" + caInContainer + ":ro"}
	if strings.Join(spec.Mounts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("proxy mode mounts:\n%s\nwant only the project and the CA cert:\n%s",
			strings.Join(spec.Mounts, "\n"), strings.Join(want, "\n"))
	}
	copies := resolveCopies(c, home, proj, noopWarner{})
	if len(copies) != 1 || copies[0].Source != proj {
		t.Fatalf("proxy mode copies = %+v, want only the project", copies)
	}

	c.Proxy = nil
	if spec := buildSpec("aisb-x", c, home, proj, nil, "", noopWarner{}); len(spec.Mounts) < len(risky) {
		t.Fatalf("without proxy mode mounts must be as configured, got %v", spec.Mounts)
	}
}
