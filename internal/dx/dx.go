// Package dx is the docker-shell-out seam for aisb.
//
// Layer 1 (Executor) is the only seam tests fake. Layer 2 is a set of
// pure helper functions composed on top — argv construction, output
// parsing, and the small set of operations aisb needs.
package dx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
	// RunWithStdin runs argv feeding stdin to the process, discarding output.
	// Used to pipe the proxy's CA + secrets payload to `docker run -i`.
	RunWithStdin(stdin []byte, args ...string) error
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

func (c Cmd) RunWithStdin(stdin []byte, args ...string) error {
	cmd := exec.Command(c.bin(), args...)
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
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
	GPUs     string            // --gpus value ("all", "device=0,1"); empty omits the flag
	Workdir  string            // -w
	Hostname string            // defaults to Name when empty
	Labels   map[string]string // --label k=v
	Env      map[string]string // -e K=V (empty values skipped)
	Mounts   []string          // -v <entry> per item; each is a "src:dest" spec
	Ports    []string          // -p <entry> per item; each is a "host:container" spec
	Network  string            // --network (empty = docker default bridge)
}

// Info is the parsed output of Inspect for one container.
type Info struct {
	Name      string
	Status    string // docker State.Status (running, exited, ...)
	StartedAt time.Time
	NanoCpus  string // raw int64 string from HostConfig.NanoCpus
	Memory    string // raw int64 string from HostConfig.Memory (bytes)
	CWDLabel  string // aisb.cwd label, "" if absent
	GPUs      string // compacted --gpus request: "all", "2", "0,1", or "-" when none
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

// Inspect fetches state, started-at, cpu/mem limits, the aisb.cwd label, and
// the GPU request for each named container.
func Inspect(e Executor, names ...string) ([]Info, error) {
	if len(names) == 0 {
		return nil, nil
	}
	args := append([]string{"inspect", "--format",
		`{{.Name}}` + "\t" + `{{.State.Status}}` + "\t" + `{{.State.StartedAt}}` + "\t" +
			`{{.HostConfig.NanoCpus}}` + "\t" + `{{.HostConfig.Memory}}` + "\t" +
			`{{index .Config.Labels "aisb.cwd"}}` + "\t" +
			`{{range .HostConfig.DeviceRequests}}{{.Count}}:{{range .DeviceIDs}}{{.}},{{end}} {{end}}`}, names...)
	out, err := e.Output(args...)
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 7)
		if len(f) < 7 {
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
			GPUs:      CompactGPUs(f[6]),
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
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	if s.GPUs != "" {
		args = append(args, "--gpus", gpusFlag(s.GPUs))
		// Pass the requested GPUs' device nodes explicitly so Docker records them
		// in the container's systemd scope (DeviceAllow=). Without this, the nvidia
		// runtime hook whitelists them out-of-band and any `systemctl
		// daemon-reload` silently revokes GPU access — NVML "Unknown Error"
		// though the device nodes remain visible. See nvidia-container-toolkit#48.
		// Only the requested GPUs' nodes are passed, so pinning stays pinned.
		for _, dev := range gpuDevices(s.GPUs, hostGPUs) {
			args = append(args, "--device", dev)
		}
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

// gpusFlag quotes a "device=" list so docker's CSV parser keeps it as one
// field: unquoted, "device=0,1" means device 0 plus a count of 1. Values that
// set other keys too (a second "=") are passed through for the user to quote.
func gpusFlag(v string) string {
	if strings.HasPrefix(v, "device=") && strings.Contains(v, ",") && strings.Count(v, "=") == 1 {
		return `"` + v + `"`
	}
	return v
}

// gpuHost is where gpuDevices looks up the host's GPUs. Fields are swappable
// so tests can fake a GPU host.
type gpuHost struct {
	dev           string                 // device node dir, "/dev"
	procGPUs      string                 // per-GPU driver info, "/proc/driver/nvidia/gpus"
	smiIndexUUIDs func() (string, error) // "index, uuid" lines from nvidia-smi
}

// hostGPUs is the real host. It is read on the machine aisb runs on, which
// must be the Docker daemon's host for the device paths to mean anything.
var hostGPUs = gpuHost{
	dev:      "/dev",
	procGPUs: "/proc/driver/nvidia/gpus",
	smiIndexUUIDs: func() (string, error) {
		out, err := exec.Command("nvidia-smi", "--query-gpu=index,uuid", "--format=csv,noheader").Output()
		return string(out), err
	},
}

// nvidiaControlNodes are shared by every GPU and needed whichever GPU is used.
var nvidiaControlNodes = []string{"nvidiactl", "nvidia-uvm", "nvidia-uvm-tools", "nvidia-modeset"}

// gpuDevices returns the host device nodes backing a --gpus request: the
// requested GPUs' own nodes plus the shared control nodes. GPU indexes are
// NVML's numbering, which need not match /dev/nvidiaN, so each index is
// resolved to its UUID (nvidia-smi) and the UUID to its device minor (/proc).
func gpuDevices(gpus string, h gpuHost) []string {
	if gpus == "all" {
		return allNvidiaNodes(h.dev)
	}
	ids, ok := strings.CutPrefix(strings.Trim(gpus, `"`), "device=")
	if !ok {
		return nil
	}
	smi, err := h.smiIndexUUIDs()
	if err != nil {
		return nil
	}
	uuidByIndex := parseSMIIndexUUIDs(smi)
	minorByUUID := procMinorsByUUID(h.procGPUs)
	var devs []string
	for _, id := range strings.Split(ids, ",") {
		uuid := id
		if u, isIndex := uuidByIndex[id]; isIndex {
			uuid = u
		}
		minor, ok := minorByUUID[uuid]
		if !ok {
			return nil
		}
		devs = append(devs, filepath.Join(h.dev, "nvidia"+minor))
	}
	for _, n := range nvidiaControlNodes {
		p := filepath.Join(h.dev, n)
		if _, err := os.Stat(p); err == nil {
			devs = append(devs, p)
		}
	}
	return devs
}

// parseSMIIndexUUIDs reads `nvidia-smi --query-gpu=index,uuid
// --format=csv,noheader` output into index → UUID.
func parseSMIIndexUUIDs(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if idx, uuid, ok := strings.Cut(line, ","); ok {
			m[strings.TrimSpace(idx)] = strings.TrimSpace(uuid)
		}
	}
	return m
}

// procMinorsByUUID maps each GPU's UUID to its /dev/nvidiaN minor number from
// the driver's per-GPU <procGPUs>/<pci-bus-id>/information files.
func procMinorsByUUID(procGPUs string) map[string]string {
	m := map[string]string{}
	infos, _ := filepath.Glob(filepath.Join(procGPUs, "*", "information"))
	for _, p := range infos {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var uuid, minor string
		for _, line := range strings.Split(string(b), "\n") {
			k, v, _ := strings.Cut(line, ":")
			switch strings.TrimSpace(k) {
			case "GPU UUID":
				uuid = strings.TrimSpace(v)
			case "Device Minor":
				minor = strings.TrimSpace(v)
			}
		}
		if uuid != "" && minor != "" {
			m[uuid] = minor
		}
	}
	return m
}

// allNvidiaNodes returns every NVIDIA device node in dev (nvidia0..N plus the
// control nodes), skipping the nvidia-caps directory, which is not a node.
func allNvidiaNodes(dev string) []string {
	matches, _ := filepath.Glob(filepath.Join(dev, "nvidia*"))
	var devs []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			devs = append(devs, m)
		}
	}
	return devs
}

func Start(e Executor, name string) error  { return e.RunSilent("start", name) }
func Stop(e Executor, name string) error   { return e.RunSilent("stop", name) }
func Remove(e Executor, name string) error { return e.RunSilent("rm", "-f", name) }

// Copy copies a host file or directory to dest inside container name with
// `docker cp -a`. docker cp nests a directory inside dest when dest exists and
// fails when dest's parent is missing, so a directory's contents are copied
// into a freshly created dest, and a file goes to dest after creating its
// parent.
func Copy(e Executor, name, src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	dir, from := path.Dir(dest), src
	if fi.IsDir() {
		dir, from = dest, src+"/."
	}
	if err := e.RunSilent("exec", name, "mkdir", "-p", dir); err != nil {
		return err
	}
	return e.RunSilent("cp", "-a", from, name+":"+dest)
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

// CompactGPUs renders a container's GPU request from the Inspect template's
// per-request "count:id,id," tokens (docker's HostConfig.DeviceRequests): "all"
// for `--gpus all` (count -1), the device IDs for `--gpus device=0,1`, a plain
// count for `--gpus 2`, or "-" when the container requested no GPU.
func CompactGPUs(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	count, ids, _ := strings.Cut(strings.Fields(s)[0], ":")
	ids = strings.Trim(ids, ",")
	switch {
	case ids != "":
		return ids
	case count == "-1":
		return "all"
	case count != "" && count != "0":
		return count
	default:
		return "-"
	}
}
