// Package dx is the docker-shell-out seam for aisb.
//
// Layer 1 (Executor) is the only seam tests fake. Layer 2 is a set of
// pure helper functions composed on top — argv construction, output
// parsing, and the small set of operations aisb needs.
package dx

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Executor runs the docker (or compatible) binary. Production uses Cmd.
// Tests substitute their own implementation.
type Executor interface {
	// Output runs argv and returns trimmed combined stdout+stderr.
	Output(args ...string) (string, error)
	// Run streams stdout+stderr to the host terminal.
	Run(args ...string) error
	// RunSilent discards stdout+stderr and returns the exit error.
	RunSilent(args ...string) error
	// Replace replaces the current process via syscall.Exec.
	// Returns only on failure to exec.
	Replace(args ...string) error
}

// Cmd is the production Executor. Zero-value invokes the "docker" binary
// found on $PATH; set Bin to swap to "podman", "finch", etc.
type Cmd struct{ Bin string }

func (c Cmd) bin() string {
	if c.Bin == "" {
		return "docker"
	}
	return c.Bin
}

func (c Cmd) Output(args ...string) (string, error) {
	out, err := exec.Command(c.bin(), args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (c Cmd) Run(args ...string) error {
	cmd := exec.Command(c.bin(), args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c Cmd) RunSilent(args ...string) error {
	return exec.Command(c.bin(), args...).Run()
}

func (c Cmd) Replace(args ...string) error {
	path, err := exec.LookPath(c.bin())
	if err != nil {
		return err
	}
	return syscall.Exec(path, append([]string{c.bin()}, args...), os.Environ())
}

// ---------- helpers ----------

// ContainerSpec is the structured input to Create. Fields map directly to
// `docker run -d` flags.
type ContainerSpec struct {
	Name     string
	Image    string
	Memory   string            // "4g"
	CPUs     string            // "2"
	Workdir  string            // -w
	Hostname string            // defaults to Name when empty
	Labels   map[string]string // --label k=v
	Env      map[string]string // -e K=V (empty values skipped)
	Mounts   []string          // -v <entry> per item; each is a "src:dest" spec
	Ports    []string          // -p <entry> per item; each is a "host:container" spec
}

// Info is the parsed output of Inspect for one container.
type Info struct {
	Name      string
	Status    string // docker State.Status (running, exited, ...)
	StartedAt time.Time
	NanoCpus  string // raw int64 string from HostConfig.NanoCpus
	Memory    string // raw int64 string from HostConfig.Memory (bytes)
	CWDLabel  string // aisb.cwd label, "" if absent
}

func ContainerExists(e Executor, name string) bool {
	out, err := e.Output("ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if l == name {
			return true
		}
	}
	return false
}

func ContainerRunning(e Executor, name string) bool {
	out, err := e.Output("ps", "--format", "{{.Names}}")
	if err != nil {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if l == name {
			return true
		}
	}
	return false
}

func ImageExists(e Executor, image string) bool {
	return e.RunSilent("image", "inspect", image) == nil
}

// ListNames returns container names matching the given prefix.
func ListNames(e Executor, namePrefix string) ([]string, error) {
	out, err := e.Output("ps", "-a",
		"--filter", "name=^"+namePrefix,
		"--format", "{{.Names}}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// Inspect fetches state, started-at, cpu/mem limits, and the aisb.cwd
// label for each named container.
func Inspect(e Executor, names ...string) ([]Info, error) {
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"inspect", "--format",
		`{{.Name}}` + "\t" + `{{.State.Status}}` + "\t" + `{{.State.StartedAt}}` + "\t" +
			`{{.HostConfig.NanoCpus}}` + "\t" + `{{.HostConfig.Memory}}` + "\t" +
			`{{index .Config.Labels "aisb.cwd"}}`}, names...)
	out, err := e.Output(args...)
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 6)
		if len(f) < 6 {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, f[2])
		infos = append(infos, Info{
			Name:      strings.TrimPrefix(f[0], "/"),
			Status:    f[1],
			StartedAt: t,
			NanoCpus:  f[3],
			Memory:    f[4],
			CWDLabel:  f[5],
		})
	}
	return infos, nil
}

// Create issues `docker run -d` from a structured spec.
func Create(e Executor, s ContainerSpec) error {
	host := s.Hostname
	if host == "" {
		host = s.Name
	}
	args := []string{"run", "-d",
		"--name", s.Name,
		"--memory", s.Memory,
		"--cpus", s.CPUs,
		"--hostname", host,
	}
	for k, v := range s.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if s.Workdir != "" {
		args = append(args, "-w", s.Workdir)
	}
	for k, v := range s.Env {
		if v == "" {
			continue
		}
		args = append(args, "-e", k+"="+v)
	}
	for _, m := range s.Mounts {
		args = append(args, "-v", m)
	}
	for _, p := range s.Ports {
		args = append(args, "-p", p)
	}
	args = append(args, s.Image)
	return e.Run(args...)
}

func Start(e Executor, name string) error  { return e.RunSilent("start", name) }
func Stop(e Executor, name string) error   { return e.RunSilent("stop", name) }
func Remove(e Executor, name string) error { return e.RunSilent("rm", "-f", name) }

// Copy runs `docker cp -a` to copy a file or directory from the host into
// the container. dest must be of the form "<container-name>:<path>".
func Copy(e Executor, src, dest string) error {
	return e.RunSilent("cp", "-a", src, dest)
}

// PrintStatusTable streams a one-row docker ps table for `name`.
func PrintStatusTable(e Executor, name string) error {
	return e.Run("ps", "-a",
		"--filter", "name=^"+name+"$",
		"--format", "table {{.Names}}\t{{.Status}}\t{{.Image}}")
}

// Shell replaces the current process with `docker exec -it name /bin/zsh`.
// Returns only on exec failure.
func Shell(e Executor, name string) error {
	return e.Replace("exec", "-it", name, "/bin/zsh")
}

// ---------- format helpers (parsing of Info fields) ----------

// CompactUptime renders status + StartedAt as "5s", "12m", "3h", "2d", or
// the raw status string when not running.
func CompactUptime(status string, started time.Time) string {
	if status != "running" {
		return status
	}
	if started.IsZero() {
		return "?"
	}
	d := time.Since(started)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// CompactCPUs turns NanoCpus (1e9 = 1 core) into "2" / "1.5" / "-".
func CompactCPUs(s string) string {
	var n int64
	fmt.Sscanf(s, "%d", &n)
	if n == 0 {
		return "-"
	}
	cores := float64(n) / 1e9
	if cores == float64(int64(cores)) {
		return fmt.Sprintf("%d", int64(cores))
	}
	return fmt.Sprintf("%.1f", cores)
}

// CompactMem turns memory bytes into "4G" / "512M" / "1024K" / "-".
func CompactMem(s string) string {
	var b int64
	fmt.Sscanf(s, "%d", &b)
	if b == 0 {
		return "-"
	}
	const k = 1024
	switch {
	case b >= k*k*k:
		return fmt.Sprintf("%gG", float64(b)/float64(k*k*k))
	case b >= k*k:
		return fmt.Sprintf("%gM", float64(b)/float64(k*k))
	default:
		return fmt.Sprintf("%dK", b/k)
	}
}
