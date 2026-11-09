package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

// noImage is a dx.Executor where no image exists. version answers with
// versionOut and versionErr, standing in for a running or stopped daemon.
type noImage struct {
	versionOut string
	versionErr error
}

func (n noImage) Output(args ...string) (string, error) {
	if len(args) > 0 && args[0] == "version" {
		return n.versionOut, n.versionErr
	}
	return "", nil
}
func (noImage) Run(...string) error                  { return nil }
func (noImage) RunSilent(...string) error            { return errors.New("exit status 1") }
func (noImage) Replace(...string) error              { return nil }
func (noImage) RunWithStdin([]byte, ...string) error { return nil }

// With the daemon stopped every image looks missing. The error must say the
// daemon is unreachable, not send the user off to rebuild an image that exists.
func TestCreate_DaemonDownIsNotReportedAsMissingImage(t *testing.T) {
	home := t.TempDir()
	c := cfg.Effective{Image: "img", Mounts: []string{"{{CWD}}"}}
	down := noImage{versionOut: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock", versionErr: errors.New("exit status 1")}

	err := Handler{Log: &captureLog{}, Docker: down}.Create("aisb-x", c, home, home, nil)
	if err == nil || !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") || strings.Contains(err.Error(), "aisb build") {
		t.Errorf("daemon down: error = %v, want the daemon's message and no `aisb build` hint", err)
	}

	up := noImage{versionOut: "29.2.1"}
	err = Handler{Log: &captureLog{}, Docker: up}.Create("aisb-x", c, home, home, nil)
	if err == nil || !strings.Contains(err.Error(), "aisb build") {
		t.Errorf("daemon up, image missing: error = %v, want the `aisb build` hint", err)
	}
}
