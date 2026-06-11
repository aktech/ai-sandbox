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
	if err := Create(r, ContainerSpec{Name: "psb-x", Image: "img", Memory: "4g", CPUs: "2", Network: "psb-net-x"}); err != nil {
		t.Fatal(err)
	}
	if got := argPairs(r.args, "--network"); len(got) != 1 || got[0] != "psb-net-x" {
		t.Fatalf("--network = %v, want [psb-net-x]\nargv: %v", got, r.args)
	}

	r2 := &recorder{}
	if err := Create(r2, ContainerSpec{Name: "psb-y", Image: "img", Memory: "4g", CPUs: "2"}); err != nil {
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
		Name:   "psb-x",
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
