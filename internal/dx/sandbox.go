package dx

import (
	"archive/tar"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Streamer is an Executor that can also hand back a command's stdout as a
// stream, for output too large to buffer (a 300 MB binary from docker cp).
type Streamer interface {
	Executor
	Stream(args ...string) (io.ReadCloser, error)
}

// Stream starts docker with args and returns its stdout. Close waits for the
// process and reports its exit error.
func (c Cmd) Stream(args ...string) (io.ReadCloser, error) {
	cmd := exec.Command(c.bin(), args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &waitCloser{ReadCloser: out, cmd: cmd}, nil
}

type waitCloser struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (w *waitCloser) Close() error {
	_, _ = io.Copy(io.Discard, w.ReadCloser)
	w.ReadCloser.Close()
	return w.cmd.Wait()
}

// Sandbox is a running container seen from the host, for integrity checks.
type Sandbox struct {
	Docker Streamer
	Name   string
}

// Open returns the contents of the regular file at path inside the
// container. It goes through `docker cp`, which the daemon serves from the
// container's filesystem, so nothing inside the sandbox can alter what is
// read.
func (s Sandbox) Open(path string) (io.ReadCloser, error) {
	stream, err := s.Docker.Stream("cp", s.Name+":"+path, "-")
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(stream)
	h, err := tr.Next()
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("%s: not found in %s", path, s.Name)
	}
	if h.Typeflag != tar.TypeReg {
		stream.Close()
		return nil, fmt.Errorf("%s in %s is not a regular file", path, s.Name)
	}
	return struct {
		io.Reader
		io.Closer
	}{tr, stream}, nil
}

// Arch is the architecture of the container's image (amd64, arm64), read
// from the daemon's image metadata.
func (s Sandbox) Arch() (string, error) {
	img, err := s.Docker.Output("inspect", "--format", "{{.Image}}", s.Name)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %s", s.Name, img)
	}
	arch, err := s.Docker.Output("image", "inspect", "--format", "{{.Architecture}}", img)
	if err != nil {
		return "", fmt.Errorf("inspect image of %s: %s", s.Name, arch)
	}
	return arch, nil
}

// Lookup returns, for each command name, the real path (symlinks resolved)
// that the sandbox's interactive zsh runs for it, or "" when it finds none.
// This runs inside the sandbox, so it shows what the agent user's own shell
// setup does (for example a PATH change in ~/.zshrc); unlike Open, it cannot
// stand up to someone with root in the sandbox.
func (s Sandbox) Lookup(names []string) (map[string]string, error) {
	script := `for t in "$@"; do printf '%s=%s\n' "$t" "$(readlink -f "$(command -v "$t")" 2>/dev/null)"; done`
	args := append([]string{"exec", s.Name, "/bin/zsh", "-i", "-c", script, "zsh"}, names...)
	out, err := s.Docker.Output(args...)
	if err != nil {
		return nil, fmt.Errorf("resolve commands in %s: %v: %s", s.Name, err, out)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	got := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && want[k] {
			got[k] = v
		}
	}
	return got, nil
}
