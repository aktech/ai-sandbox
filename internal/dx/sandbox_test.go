package dx

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

// cpStub answers `docker cp <c>:<path> -` with a tar stream built from
// entries, and Output from a map keyed by the joined argv.
type cpStub struct {
	outputStub
	entries map[string]*tar.Header // path -> header (Size set for files)
	bodies  map[string]string
	outputs map[string]string
	execOut string // answer to any `docker exec`
	execs   [][]string
	streams [][]string
}

func (s *cpStub) Output(args ...string) (string, error) {
	if args[0] == "exec" {
		s.execs = append(s.execs, args)
		return s.execOut, nil
	}
	return s.outputs[strings.Join(args, " ")], nil
}
func (s *cpStub) Stream(args ...string) (io.ReadCloser, error) {
	s.streams = append(s.streams, args)
	src := strings.SplitN(args[1], ":", 2)[1]
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if h, ok := s.entries[src]; ok {
		_ = tw.WriteHeader(h)
		_, _ = tw.Write([]byte(s.bodies[src]))
	}
	_ = tw.Close()
	return io.NopCloser(&buf), nil
}

// Open reads a file out of the container through `docker cp`, which the
// Docker daemon serves from the container filesystem without running
// anything inside the sandbox.
func TestSandboxOpen_ReadsFileThroughDockerCp(t *testing.T) {
	s := &cpStub{
		entries: map[string]*tar.Header{"/opt/aisb/pi/pi": {Name: "pi", Typeflag: tar.TypeReg, Mode: 0o755, Size: 7}},
		bodies:  map[string]string{"/opt/aisb/pi/pi": "pi-body"},
	}
	r, err := Sandbox{Docker: s, Name: "aisb-x"}.Open("/opt/aisb/pi/pi")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "pi-body" {
		t.Fatalf("read %q, want the file's contents", got)
	}
	if want := "cp aisb-x:/opt/aisb/pi/pi -"; strings.Join(s.streams[0], " ") != want {
		t.Fatalf("ran docker %v, want %q", s.streams[0], want)
	}
}

// A symlink at the pinned path is refused rather than reported as the file:
// docker cp copies the link itself, and its target is not what was pinned.
func TestSandboxOpen_RefusesSymlink(t *testing.T) {
	s := &cpStub{entries: map[string]*tar.Header{
		"/opt/aisb/pi/pi": {Name: "pi", Typeflag: tar.TypeSymlink, Linkname: "/tmp/evil"},
	}}
	if _, err := (Sandbox{Docker: s, Name: "aisb-x"}).Open("/opt/aisb/pi/pi"); err == nil {
		t.Fatal("opened a symlink as if it were the pinned binary")
	}
	if _, err := (Sandbox{Docker: s, Name: "aisb-x"}).Open("/opt/aisb/claude/claude"); err == nil {
		t.Fatal("opened a path docker cp did not return")
	}
}

// Arch is the architecture of the image the container runs, from the daemon.
func TestSandboxArch_ComesFromTheContainersImage(t *testing.T) {
	s := &cpStub{outputs: map[string]string{
		"inspect --format {{.Image}} aisb-x":                  "sha256:abc",
		"image inspect --format {{.Architecture}} sha256:abc": "arm64",
	}}
	got, err := Sandbox{Docker: s, Name: "aisb-x"}.Arch()
	if err != nil || got != "arm64" {
		t.Fatalf("Arch = %q, %v; want arm64", got, err)
	}
}

// Lookup reports what the sandbox's interactive shell (the one `aisb` opens)
// runs for each command, ignoring anything else the shell prints.
func TestSandboxLookup_ParsesShellOutput(t *testing.T) {
	out := "some banner from .zshrc\n" +
		"claude=/opt/aisb/claude/claude\n" +
		"codex=\n" +
		"pi=/home/alice/.local/bin/pi\n" +
		"evil=/x\n"
	s := &cpStub{execOut: out}
	got, err := Sandbox{Docker: s, Name: "aisb-x"}.Lookup([]string{"claude", "codex", "pi"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"claude": "/opt/aisb/claude/claude", "codex": "", "pi": "/home/alice/.local/bin/pi"}
	if len(got) != len(want) {
		t.Fatalf("Lookup = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Lookup[%s] = %q, want %q", k, got[k], v)
		}
	}
	if ex := strings.Join(s.execs[0], " "); !strings.Contains(ex, "aisb-x /bin/zsh -i -c") {
		t.Fatalf("ran %q, want the interactive zsh aisb opens", ex)
	}
}
