package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

// TestE2E_InternalNetBlocksDirectEgress confirms the security invariant: a
// container on a psb internal network cannot reach the internet directly. This
// is what makes the proxy the only egress path; if it ever regresses, the
// allowlist becomes advisory rather than enforced.
func TestE2E_InternalNetBlocksDirectEgress(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	net := "psb-net-e2e-test"
	exec.Command("docker", "network", "create", "--internal", net).Run()
	defer exec.Command("docker", "network", "rm", net).Run()

	out, _ := exec.Command("docker", "run", "--rm", "--network", net,
		"alpine", "sh", "-c",
		"wget -T3 -q -O- http://1.1.1.1 >/dev/null 2>&1 && echo REACHED || echo BLOCKED").CombinedOutput()
	// CombinedOutput may be prefixed with image-pull progress (CI has no cached
	// image), so match on content rather than an exact prefix.
	s := string(out)
	if !strings.Contains(s, "BLOCKED") || strings.Contains(s, "REACHED") {
		t.Fatalf("expected egress BLOCKED, got %q", s)
	}
}
