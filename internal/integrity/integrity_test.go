package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fakeSandbox is a running sandbox whose files and PATH lookups are given
// directly, so Verify can be exercised without Docker.
type fakeSandbox struct {
	arch     string
	files    map[string]string // path -> contents
	resolves map[string]string // command name -> resolved path
}

func (f fakeSandbox) Arch() (string, error) { return f.arch, nil }
func (f fakeSandbox) Open(path string) (io.ReadCloser, error) {
	c, ok := f.files[path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return io.NopCloser(strings.NewReader(c)), nil
}
func (f fakeSandbox) Lookup(names []string) (map[string]string, error) { return f.resolves, nil }

// dockerfileWith renders the ARG lines Verify reads its pins from.
func dockerfileWith(lines ...string) []byte {
	return []byte("FROM scratch\n" + strings.Join(lines, "\n") + "\n")
}

// pinned returns a Dockerfile pinning one build of each agent for arm64, and
// a sandbox holding exactly those builds at their pinned paths.
func pinned() ([]byte, fakeSandbox) {
	df := dockerfileWith(
		"ARG CLAUDE_BIN_SHA256_ARM64="+sum("claude-arm64"),
		"ARG CLAUDE_BIN_SHA256_AMD64="+sum("claude-amd64"),
		"ARG CODEX_BIN_SHA256_ARM64="+sum("codex-arm64"),
		"ARG PI_BIN_SHA256_ARM64="+sum("pi-arm64"),
	)
	sb := fakeSandbox{
		arch: "arm64",
		files: map[string]string{
			"/opt/aisb/claude/claude":   "claude-arm64",
			"/opt/aisb/codex/bin/codex": "codex-arm64",
			"/opt/aisb/pi/pi":           "pi-arm64",
		},
		resolves: map[string]string{
			"claude": "/opt/aisb/claude/claude",
			"codex":  "/opt/aisb/codex/bin/codex",
			"pi":     "/opt/aisb/pi/pi",
		},
	}
	return df, sb
}

func result(t *testing.T, rep Report, agent string) Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.Agent == agent {
			return r
		}
	}
	t.Fatalf("no result for %s in %+v", agent, rep)
	return Result{}
}

// A sandbox whose agents are byte-for-byte the pinned builds, and which run
// from their pinned paths, passes.
func TestVerify_PinnedBinariesPass(t *testing.T) {
	df, sb := pinned()
	rep, err := Verify(df, sb)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Fatalf("pinned sandbox reported a problem: %+v", rep)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("got %d results, want one per agent", len(rep.Results))
	}
}

// A binary overwritten after the build (here: claude replaced in place) is
// reported as changed, with both hashes, while the untouched agents pass.
func TestVerify_ReplacedBinaryIsChanged(t *testing.T) {
	df, sb := pinned()
	sb.files["/opt/aisb/claude/claude"] = "claude-arm64 with a payload"

	rep, err := Verify(df, sb)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("a replaced binary passed")
	}
	c := result(t, rep, "claude")
	if c.Status != Changed || c.Want != sum("claude-arm64") || c.Got != sum("claude-arm64 with a payload") {
		t.Fatalf("claude = %+v, want changed with both hashes", c)
	}
	if s := result(t, rep, "codex").Status; s != OK {
		t.Fatalf("untouched codex = %s, want ok", s)
	}
}

// A binary that is gone (deleted, or the sandbox runs an image without
// /opt/aisb) is reported as missing rather than aborting the other checks.
func TestVerify_DeletedBinaryIsMissing(t *testing.T) {
	df, sb := pinned()
	delete(sb.files, "/opt/aisb/pi/pi")

	rep, err := Verify(df, sb)
	if err != nil {
		t.Fatal(err)
	}
	if s := result(t, rep, "pi").Status; s != Missing {
		t.Fatalf("deleted pi = %s, want missing", s)
	}
	if s := result(t, rep, "claude").Status; s != OK {
		t.Fatalf("claude = %s, want ok", s)
	}
}

// The pinned file can be intact while the shell runs something else: a
// `claude` dropped earlier on PATH (e.g. ~/.local/bin via .zshrc) shadows
// it. That is reported, naming what actually runs.
func TestVerify_ShadowedCommandIsReported(t *testing.T) {
	df, sb := pinned()
	sb.resolves["claude"] = "/home/alice/.local/share/claude/versions/9.9.9"
	delete(sb.resolves, "codex") // not on PATH at all

	rep, err := Verify(df, sb)
	if err != nil {
		t.Fatal(err)
	}
	c := result(t, rep, "claude")
	if c.Status != Shadowed || !strings.Contains(c.Detail, "/home/alice/.local/share/claude/versions/9.9.9") {
		t.Fatalf("claude = %+v, want shadowed naming the binary that runs", c)
	}
	if s := result(t, rep, "codex").Status; s != Shadowed {
		t.Fatalf("codex off PATH = %s, want shadowed", s)
	}
	if s := result(t, rep, "pi").Status; s != OK {
		t.Fatalf("pi = %s, want ok", s)
	}
}

// With no pin for the sandbox's architecture there is nothing to compare
// against; that is reported as unpinned, not passed and not "changed".
func TestVerify_NoPinForArchIsUnpinned(t *testing.T) {
	df, sb := pinned()
	sb.arch = "amd64" // pinned() pins only claude for amd64
	sb.files["/opt/aisb/claude/claude"] = "claude-amd64"

	rep, err := Verify(df, sb)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() {
		t.Fatal("unpinned agents passed")
	}
	if s := result(t, rep, "codex").Status; s != Unpinned {
		t.Fatalf("codex on amd64 = %s, want unpinned", s)
	}
	if s := result(t, rep, "claude").Status; s != OK {
		t.Fatalf("claude on amd64 = %s, want ok", s)
	}
}

// failingClose is a file whose read ends early: its Close reports the error
// (as docker cp's exit status does when the copy breaks off).
type failingClose struct{ io.Reader }

func (failingClose) Close() error { return errors.New("docker cp: exit status 1") }

type truncatingSandbox struct{ fakeSandbox }

func (t truncatingSandbox) Open(path string) (io.ReadCloser, error) {
	return failingClose{strings.NewReader("clau")}, nil
}

// A copy that breaks off is reported as missing, not hashed as if it were
// the whole file.
func TestVerify_BrokenReadIsMissing(t *testing.T) {
	df, sb := pinned()
	rep, err := Verify(df, truncatingSandbox{sb})
	if err != nil {
		t.Fatal(err)
	}
	if c := result(t, rep, "claude"); c.Status != Missing || c.Got != "" {
		t.Fatalf("claude = %+v, want missing with no hash", c)
	}
}
