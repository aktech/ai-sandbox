package dx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a fake Executor that captures the argv of the last Run call.
type recorder struct{ args []string }

func (r *recorder) Output(args ...string) (string, error) { return "", nil }
func (r *recorder) Run(args ...string) error              { r.args = args; return nil }
func (r *recorder) RunSilent(args ...string) error        { r.args = args; return nil }
func (r *recorder) Replace(args ...string) error          { r.args = args; return nil }
func (r *recorder) RunWithStdin(stdin []byte, args ...string) error {
	r.args = args
	return nil
}

// argPairs returns the values that follow each occurrence of flag in argv.
func argPairs(argv []string, flag string) []string {
	var vals []string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			vals = append(vals, argv[i+1])
		}
	}
	return vals
}

// Create must attach the container to a named network via `--network` when
// ContainerSpec.Network is set (used by proxy mode's internal networks), and
// must omit the flag entirely when empty (default bridge, today's behavior).
func TestCreate_NetworkFlag(t *testing.T) {
	r := &recorder{}
	if err := Create(r, ContainerSpec{Name: "aisb-x", Image: "img", Memory: "4g", CPUs: "2", Network: "aisb-net-x"}); err != nil {
		t.Fatal(err)
	}
	if got := argPairs(r.args, "--network"); len(got) != 1 || got[0] != "aisb-net-x" {
		t.Fatalf("--network = %v, want [aisb-net-x]\nargv: %v", got, r.args)
	}

	r2 := &recorder{}
	if err := Create(r2, ContainerSpec{Name: "aisb-y", Image: "img", Memory: "4g", CPUs: "2"}); err != nil {
		t.Fatal(err)
	}
	if got := argPairs(r2.args, "--network"); len(got) != 0 {
		t.Fatalf("--network should be absent without Network set, got %v", got)
	}
}

// Create must publish each requested port via a `-p host:container` flag so a
// server inside the sandbox is reachable from the host browser.
func TestCreate_PublishesPorts(t *testing.T) {
	r := &recorder{}
	err := Create(r, ContainerSpec{
		Name:   "aisb-x",
		Image:  "img",
		Memory: "4g",
		CPUs:   "2",
		Ports:  []string{"3000:3000", "8000:8000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := argPairs(r.args, "-p")
	want := []string{"3000:3000", "8000:8000"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("-p values = %v, want %v\nfull argv: %v", got, want, r.args)
	}
}

// docker parses --gpus as CSV, so an unquoted "device=0,1" reads as device 0
// plus a count of 1. Create must quote a device list so docker sees one field.
func TestCreate_QuotesGPUDeviceList(t *testing.T) {
	cases := []struct{ in, want string }{
		{"device=0,1", `"device=0,1"`},
		{`"device=0,1"`, `"device=0,1"`}, // already quoted: leave alone
		{"device=0", "device=0"},
		{"device=0,capabilities=compute", "device=0,capabilities=compute"}, // separate keys: not a list
		{"all", "all"},
		{"2", "2"},
	}
	for _, c := range cases {
		r := &recorder{}
		if err := Create(r, ContainerSpec{Name: "aisb-x", Image: "img", Memory: "4g", CPUs: "2", GPUs: c.in}); err != nil {
			t.Fatal(err)
		}
		if got := argPairs(r.args, "--gpus"); len(got) != 1 || got[0] != c.want {
			t.Errorf("GPUs %q: --gpus = %v, want [%s]", c.in, got, c.want)
		}
	}
}

// callLog is a fake Executor that records every command it is asked to run.
type callLog struct{ calls [][]string }

func (c *callLog) Output(args ...string) (string, error) { return "", nil }
func (c *callLog) Run(args ...string) error              { c.calls = append(c.calls, args); return nil }
func (c *callLog) RunSilent(args ...string) error        { c.calls = append(c.calls, args); return nil }
func (c *callLog) Replace(args ...string) error          { c.calls = append(c.calls, args); return nil }
func (c *callLog) RunWithStdin(_ []byte, args ...string) error {
	c.calls = append(c.calls, args)
	return nil
}

func joinCalls(calls [][]string) string {
	var lines []string
	for _, c := range calls {
		lines = append(lines, strings.Join(c, " "))
	}
	return strings.Join(lines, "\n")
}

// docker cp nests a directory inside dest when dest already exists, and fails
// when dest's parent is missing. Copying a directory must therefore create
// dest first and copy the directory's contents, so they land at dest whether
// or not it existed.
func TestCopy_DirectoryLandsAtDest(t *testing.T) {
	src := t.TempDir()
	c := &callLog{}
	if err := Copy(c, "aisb-x", src, "/home/u/.cache/pip"); err != nil {
		t.Fatal(err)
	}
	want := "exec aisb-x mkdir -p /home/u/.cache/pip\n" +
		"cp -a " + src + "/. aisb-x:/home/u/.cache/pip"
	if got := joinCalls(c.calls); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
}

// A single file is copied to dest itself, after creating dest's parent.
func TestCopy_FileLandsAtDest(t *testing.T) {
	src := filepath.Join(t.TempDir(), "netrc")
	if err := os.WriteFile(src, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &callLog{}
	if err := Copy(c, "aisb-x", src, "/home/u/.config/tool/netrc"); err != nil {
		t.Fatal(err)
	}
	want := "exec aisb-x mkdir -p /home/u/.config/tool\n" +
		"cp -a " + src + " aisb-x:/home/u/.config/tool/netrc"
	if got := joinCalls(c.calls); got != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", got, want)
	}
}

// outputStub is an Executor whose Output returns a fixed string, for exercising
// the parsing in Inspect.
type outputStub struct{ out string }

func (o outputStub) Output(...string) (string, error)     { return o.out, nil }
func (o outputStub) Run(...string) error                  { return nil }
func (o outputStub) RunSilent(...string) error            { return nil }
func (o outputStub) Replace(...string) error              { return nil }
func (o outputStub) RunWithStdin([]byte, ...string) error { return nil }

// CompactGPUs maps docker's DeviceRequests encoding to a compact column value:
// count -1 → "all", explicit device IDs verbatim, a positive count as-is, and
// no request → "-".
func TestCompactGPUs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "-"},          // no GPU request
		{"-1: ", "all"},    // --gpus all (count -1, no device IDs)
		{"2: ", "2"},       // --gpus 2
		{"0:0, ", "0"},     // --gpus device=0 (IDs arrive comma-terminated)
		{"0:0,1, ", "0,1"}, // --gpus device=0,1
		{"0: ", "-"},       // count 0, no IDs → nothing requested
	}
	for _, c := range cases {
		if got := CompactGPUs(c.in); got != c.want {
			t.Errorf("CompactGPUs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Inspect must parse the 7th (GPU) field into Info.GPUs without disturbing the
// other columns.
func TestInspect_ParsesGPUs(t *testing.T) {
	line := strings.Join([]string{
		"/aisb-x", "running", "2026-06-23T08:00:00Z",
		"24000000000", "34359738368", "/home/me/proj", "-1:",
	}, "\t")
	infos, err := Inspect(outputStub{out: line}, "aisb-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d infos, want 1", len(infos))
	}
	if infos[0].GPUs != "all" {
		t.Errorf("GPUs = %q, want %q", infos[0].GPUs, "all")
	}
	if infos[0].CWDLabel != "/home/me/proj" {
		t.Errorf("CWDLabel = %q, want %q", infos[0].CWDLabel, "/home/me/proj")
	}
}

// fakeGPUHost builds a host with two GPUs whose /dev minor numbers are the
// reverse of their NVML index, so a test only passes if it maps index → UUID →
// minor instead of assuming index == minor.
func fakeGPUHost(t *testing.T) gpuHost {
	t.Helper()
	dev, proc := t.TempDir(), t.TempDir()
	for _, n := range []string{"nvidia0", "nvidia1", "nvidiactl", "nvidia-uvm", "nvidia-uvm-tools", "nvidia-modeset"} {
		if err := os.WriteFile(filepath.Join(dev, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dev, "nvidia-caps"), 0o755); err != nil {
		t.Fatal(err)
	}
	for bus, info := range map[string]string{
		"0000:01:00.0": "Model:\t\t NVIDIA X\nGPU UUID:\t GPU-aaaa\nDevice Minor:\t 1\n",
		"0000:02:00.0": "Model:\t\t NVIDIA X\nGPU UUID:\t GPU-bbbb\nDevice Minor:\t 0\n",
	} {
		if err := os.Mkdir(filepath.Join(proc, bus), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proc, bus, "information"), []byte(info), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return gpuHost{
		dev:      dev,
		procGPUs: proc,
		smiIndexUUIDs: func() (string, error) {
			return "0, GPU-aaaa\n1, GPU-bbbb\n", nil
		},
	}
}

// Pinning one GPU by index must pass that GPU's device node (looked up by its
// UUID, not assumed from the index) plus the shared control nodes, and never
// the other GPU's node.
func TestGPUDevices_PinnedIndex(t *testing.T) {
	h := fakeGPUHost(t)
	got := gpuDevices("device=0", h)
	want := []string{
		filepath.Join(h.dev, "nvidia1"),
		filepath.Join(h.dev, "nvidiactl"),
		filepath.Join(h.dev, "nvidia-uvm"),
		filepath.Join(h.dev, "nvidia-uvm-tools"),
		filepath.Join(h.dev, "nvidia-modeset"),
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("gpuDevices(device=0) = %v\nwant %v", got, want)
	}
}

// A GPU pinned by UUID resolves straight to its device node.
func TestGPUDevices_PinnedUUID(t *testing.T) {
	h := fakeGPUHost(t)
	got := gpuDevices("device=GPU-bbbb", h)
	if len(got) == 0 || got[0] != filepath.Join(h.dev, "nvidia0") {
		t.Fatalf("gpuDevices(device=GPU-bbbb) = %v, want nvidia0 first", got)
	}
}

// "all" exposes every GPU, so every NVIDIA device node is passed (but not the
// nvidia-caps directory).
func TestGPUDevices_All(t *testing.T) {
	h := fakeGPUHost(t)
	got := gpuDevices("all", h)
	if len(got) != 6 {
		t.Fatalf("gpuDevices(all) = %v, want the 6 device nodes", got)
	}
	for _, p := range got {
		if filepath.Base(p) == "nvidia-caps" {
			t.Fatalf("gpuDevices(all) included the nvidia-caps directory: %v", got)
		}
	}
}

// When the request can't be mapped to specific GPUs, no extra nodes are passed:
// handing over every GPU's node would widen access past what was requested.
func TestGPUDevices_UnmappableAddsNothing(t *testing.T) {
	h := fakeGPUHost(t)
	for _, gpus := range []string{"2", "device=7", "device=0,7"} {
		if got := gpuDevices(gpus, h); got != nil {
			t.Errorf("gpuDevices(%q) = %v, want nil", gpus, got)
		}
	}
	h.smiIndexUUIDs = func() (string, error) { return "", os.ErrNotExist }
	if got := gpuDevices("device=0", h); got != nil {
		t.Errorf("nvidia-smi failing: gpuDevices(device=0) = %v, want nil", got)
	}
}

// A device list the user already quoted for docker maps the same as unquoted.
func TestGPUDevices_UserQuotedList(t *testing.T) {
	h := fakeGPUHost(t)
	got := gpuDevices(`"device=0,1"`, h)
	if len(got) < 2 || got[0] != filepath.Join(h.dev, "nvidia1") || got[1] != filepath.Join(h.dev, "nvidia0") {
		t.Fatalf(`gpuDevices("device=0,1" quoted) = %v, want nvidia1, nvidia0 first`, got)
	}
}
