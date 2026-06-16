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

// Copy must invoke `docker cp -a` with the correct src and dest arguments.
func TestCopy_UsesArchiveFlag(t *testing.T) {
	r := &recorder{}
	_ = Copy(r, "/host/path", "aisb-x:/container/path")

	if len(r.args) < 3 {
		t.Fatalf("Copy sent %d args, want at least 3\nfull argv: %v", len(r.args), r.args)
	}
	if r.args[0] != "cp" {
		t.Errorf("arg[0] = %q, want %q", r.args[0], "cp")
	}
	if r.args[1] != "-a" {
		t.Errorf("arg[1] = %q, want %q", r.args[1], "-a")
	}
	if r.args[2] != "/host/path" {
		t.Errorf("arg[2] = %q, want %q", r.args[2], "/host/path")
	}
	if r.args[3] != "aisb-x:/container/path" {
		t.Errorf("arg[3] = %q, want %q", r.args[3], "aisb-x:/container/path")
	}
}
