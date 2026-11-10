// Package integrity checks that the agent binaries inside a running sandbox
// are the builds the image pinned.
//
// The pins are the <AGENT>_BIN_SHA256_<ARCH> build args in aisb's embedded
// Dockerfile, the same values the image build checked the binaries against.
// A binary that no longer matches was replaced after the build: by an
// update, a package script, or something with root in the sandbox.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Agent is one binary aisb checks: the command name and where the image
// installs it.
type Agent struct {
	Name string
	Path string
}

// Agents lists the binaries the image installs under /opt/aisb.
var Agents = []Agent{
	{Name: "claude", Path: "/opt/aisb/claude/claude"},
	{Name: "codex", Path: "/opt/aisb/codex/bin/codex"},
	{Name: "pi", Path: "/opt/aisb/pi/pi"},
}

// Sandbox is a running sandbox as Verify sees it.
type Sandbox interface {
	// Arch is the image architecture, as Docker names it (amd64, arm64).
	Arch() (string, error)
	// Open returns the contents of a file in the sandbox.
	Open(path string) (io.ReadCloser, error)
	// Lookup returns, for each command name, the real path the sandbox
	// shell runs for it ("" when it is not on PATH).
	Lookup(names []string) (map[string]string, error)
}

// Status is the outcome for one agent.
type Status string

const (
	// OK: the binary matches its pin and is what the shell runs.
	OK Status = "ok"
	// Changed: the binary differs from the pinned build.
	Changed Status = "changed"
	// Missing: the binary could not be read from the sandbox.
	Missing Status = "missing"
	// Shadowed: the pinned binary is intact, but the sandbox shell runs a
	// different file for the command (or none).
	Shadowed Status = "shadowed"
	// Unpinned: aisb has no pin for this agent on the image's architecture.
	Unpinned Status = "unpinned"
)

// Result is the check for one agent.
type Result struct {
	Agent  string `json:"agent"`
	Path   string `json:"path"`
	Status Status `json:"status"`
	Want   string `json:"want_sha256"`
	Got    string `json:"got_sha256,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Report is the check for every agent in one sandbox.
type Report struct {
	Arch    string   `json:"arch"`
	Results []Result `json:"results"`
}

// OK reports whether every agent passed.
func (r Report) OK() bool {
	for _, res := range r.Results {
		if res.Status != OK {
			return false
		}
	}
	return true
}

var pinLine = regexp.MustCompile(`(?m)^ARG ([A-Z]+)_BIN_SHA256_([A-Z0-9]+)=([0-9a-f]{64})\s*$`)

// pins returns agent name -> docker arch -> sha256 from the Dockerfile.
func pins(dockerfile []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, m := range pinLine.FindAllSubmatch(dockerfile, -1) {
		name, arch := strings.ToLower(string(m[1])), strings.ToLower(string(m[2]))
		if out[name] == nil {
			out[name] = map[string]string{}
		}
		out[name][arch] = string(m[3])
	}
	return out
}

// Verify checks every agent in sb against the pins in dockerfile.
func Verify(dockerfile []byte, sb Sandbox) (Report, error) {
	arch, err := sb.Arch()
	if err != nil {
		return Report{}, fmt.Errorf("image architecture: %w", err)
	}
	names := make([]string, len(Agents))
	for i, a := range Agents {
		names[i] = a.Name
	}
	resolved, err := sb.Lookup(names)
	if err != nil {
		return Report{}, fmt.Errorf("resolve agent commands: %w", err)
	}
	p := pins(dockerfile)
	rep := Report{Arch: arch}
	for _, a := range Agents {
		res := Result{Agent: a.Name, Path: a.Path, Want: p[a.Name][arch]}
		got, err := hashFile(sb, a.Path)
		switch {
		case res.Want == "":
			res.Status, res.Got = Unpinned, got
			res.Detail = "no " + strings.ToUpper(a.Name) + "_BIN_SHA256_" + strings.ToUpper(arch) + " pin in this aisb's Dockerfile"
		case err != nil:
			res.Status, res.Detail = Missing, err.Error()
		case got != res.Want:
			res.Status, res.Got = Changed, got
		case resolved[a.Name] == "":
			res.Status, res.Got = Shadowed, got
			res.Detail = a.Name + " is not on the sandbox PATH"
		case resolved[a.Name] != a.Path:
			res.Status, res.Got = Shadowed, got
			res.Detail = a.Name + " runs " + resolved[a.Name] + ", not the pinned " + a.Path
		default:
			res.Status, res.Got = OK, got
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

func hashFile(sb Sandbox, path string) (string, error) {
	f, err := sb.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	if cerr := f.Close(); err == nil {
		err = cerr // a read cut short must not be hashed as the file
	}
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
