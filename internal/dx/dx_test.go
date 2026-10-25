package dx

import (
	"strings"
	"testing"
)

// recorder is a fake Executor that captures the argv of the last Run call.
type recorder struct{ args []string }

func (r *recorder) Output(args ...string) (string, error) { return "", nil }
func (r *recorder) Run(args ...string) error              { r.args = args; return nil }
func (r *recorder) RunSilent(args ...string) error        { r.args = args; return nil }
func (r *recorder) Replace(args ...string) error          { r.args = args; return nil }

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

// outputStub is an Executor whose Output returns a fixed string, for exercising
// the parsing in Inspect.
type outputStub struct{ out string }

func (o outputStub) Output(...string) (string, error) { return o.out, nil }
func (o outputStub) Run(...string) error              { return nil }
func (o outputStub) RunSilent(...string) error        { return nil }
func (o outputStub) Replace(...string) error          { return nil }

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
