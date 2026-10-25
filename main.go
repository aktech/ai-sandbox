// aisb — launch a Docker (colima) sandbox per project. Image bundles `pi`
// and `claude`; pick whichever you need from inside the shell.
//
// Per-project overrides via ~/.config/ai-sandbox/config.json:
//
//	{
//	  "default": {
//	    "mounts": ["{{HOME}}/.pi/agent/skills", "{{HOME}}/dev/dotfiles", "{{CWD}}"],
//	    "extra_mounts": ["~/dev/shared"]
//	  },
//	  "projects": {
//	    "/Users/me/dev/some-project":     { "extra_mounts": ["~/dev/shared-lib"] },
//	    "/Users/me/dev/big-monorepo":     { "memory": "16g" }
//	  }
//	}
//
// "mounts" replaces the built-in default list.  Use {{HOME}}, {{SHARED_DIR}},
// {{CWD}} as placeholders, or ~/ and $VAR for shell-style expansion.  Each
// entry is "src" (same path in the container) or "src:dest" to remap it.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/cmd"
	"github.com/aktech/ai-sandbox/internal/dx"
)

// labelFlags collects repeated -label k=v values for `aisb create`.
type labelFlags []string

func (l *labelFlags) String() string     { return strings.Join(*l, ",") }
func (l *labelFlags) Set(v string) error { *l = append(*l, v); return nil }
func (l labelFlags) toMap() map[string]string {
	m := map[string]string{}
	for _, kv := range l {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

//go:embed Dockerfile
var embeddedDockerfile []byte

// ---------- env defaults ----------

func envDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func cfgPath() string {
	return envDefault("AISB_CONFIG_FILE", filepath.Join(os.Getenv("HOME"), ".config", "ai-sandbox", "config.json"))
}

// ---------- log helpers ----------

type logger struct {
	prefix string
	color  bool
}

func newLogger(prefix string) *logger {
	return &logger{prefix: prefix, color: isTerminal(1)}
}

func (l *logger) tag(c, msg string) string {
	if !l.color {
		return fmt.Sprintf("[%s] %s", l.prefix, msg)
	}
	return fmt.Sprintf("\033[%sm[%s]\033[0m %s", c, l.prefix, msg)
}

func (l *logger) Log(msg string)  { fmt.Println(l.tag("0;36", msg)) }
func (l *logger) Step(msg string) { fmt.Println(l.tag("0;36", "→ "+msg)) }
func (l *logger) OK(msg string)   { fmt.Println(l.tag("0;32", msg)) }
func (l *logger) Warn(msg string) { fmt.Fprintln(os.Stderr, l.tag("0;33", msg)) }
func (l *logger) Die(msg string, code int) {
	fmt.Fprintln(os.Stderr, l.tag("0;31", msg))
	os.Exit(code)
}

func isTerminal(fd uintptr) bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ---------- helpers ----------

func sanitizeName(s string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	cleaned := re.ReplaceAllString(s, "-")
	return strings.Trim(cleaned, "-")
}

func containerName(prefix string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s", prefix, sanitizeName(filepath.Base(cwd))), nil
}

// ---------- build (kept here because it owns the embedded Dockerfile) ----------

func cmdBuild(log *logger) error {
	image := envDefault("AISB_IMAGE_NAME", "ai-sandbox-pi:latest")
	piVersion := envDefault("PI_VERSION", "latest")
	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())
	home := os.Getenv("HOME")
	if home == "" {
		return fmt.Errorf("HOME not set")
	}

	tmp, err := os.MkdirTemp("", "aisb-build-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := os.WriteFile(filepath.Join(tmp, "Dockerfile"), embeddedDockerfile, 0o644); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}

	log.Step(fmt.Sprintf("building %s (pi=%s, home=%s, uid=%s, gid=%s)", image, piVersion, home, uid, gid))
	build := exec.Command("docker", "build",
		"--build-arg", "PI_VERSION="+piVersion,
		"--build-arg", "AGENT_UID="+uid,
		"--build-arg", "AGENT_GID="+gid,
		"--build-arg", "AGENT_HOME="+home,
		"-t", image,
		tmp,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return err
	}

	log.OK("built " + image)
	list := exec.Command("docker", "images",
		"--format", "table {{.Repository}}:{{.Tag}}\t{{.Size}}\t{{.CreatedSince}}",
		image,
	)
	list.Stdout = os.Stdout
	list.Stderr = os.Stderr
	return list.Run()
}

// ---------- main ----------

func usage() {
	fmt.Println(`aisb — launch a Docker sandbox per project. Image bundles pi + claude.

Usage:
  aisb             create or attach + shell into aisb-<project>
  aisb stop        stop the project's container
  aisb rm [n...]   destroy current container, or named ones
  aisb status      show container status
  aisb ls          list all aisb-* containers
  aisb build       (re)build the image

Config file (JSON):
  ` + filepath.Join(os.Getenv("HOME"), ".config/ai-sandbox/config.json") + `

  Keys:
    mounts         declarative mount list; each entry is "src" or "src:dest"
    extra_mounts   appended after mounts
    memory / cpus  resource limits
    gpus           docker --gpus value: "all", "device=0,1", "2" (needs NVIDIA Container Toolkit)
    image          custom image tag

  Template vars in mounts: {{HOME}}, {{SHARED_DIR}}, {{CWD}}
  Also supports ~/ and $ENV_VAR expansion.

Env vars (override config defaults):
  AISB_IMAGE_NAME   image tag (default: ai-sandbox-pi:latest)
  AISB_MEMORY       memory limit (default: 4g)
  AISB_CPUS         cpu limit (default: 2)
  AISB_GPUS         expose GPUs, e.g. all (default: none; needs NVIDIA Container Toolkit)
  AISB_SHARED_DIR   host↔container exchange dir (default: ~/sb-shared)
  AISB_CONFIG_FILE  config file path (default: ~/.config/ai-sandbox/config.json)
  HOMELAB_URL       passed through to container
  ANTHROPIC_API_KEY passed through to container (claude API auth)`)
}

func main() {
	log := newLogger("aisb")
	h := cmd.Handler{Log: log, Docker: dx.Cmd{}}

	if _, err := exec.LookPath("docker"); err != nil {
		log.Die("docker not found — install or `colima start`", 1)
	}

	home := os.Getenv("HOME")
	if home == "" {
		log.Die("HOME not set", 1)
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Die("cannot read cwd: "+err.Error(), 1)
	}

	base := cfg.Effective{
		Image:     envDefault("AISB_IMAGE_NAME", "ai-sandbox-pi:latest"),
		Memory:    envDefault("AISB_MEMORY", "4g"),
		CPUs:      envDefault("AISB_CPUS", "2"),
		GPUs:      envDefault("AISB_GPUS", ""), // empty = no GPU; "all" or "device=0,1" to expose
		SharedDir: envDefault("AISB_SHARED_DIR", filepath.Join(home, "sb-shared")),
	}
	c := cfg.Resolve(cfgPath(), cwd, base)

	name, err := containerName("aisb")
	if err != nil {
		log.Die("container name: "+err.Error(), 1)
	}

	sub := "up"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	dieOn := func(err error) {
		if err != nil {
			log.Die(err.Error(), 1)
		}
	}

	switch sub {
	case "", "up":
		dieOn(h.Up(name, c, home, cwd))
	case "create":
		// Non-interactive: prepare a sandbox (all configured mounts) and print
		// its name. Lets other tools (e.g. darb) reuse aisb's sandbox.
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		workdir := fs.String("workdir", cwd, "project directory to sandbox")
		nameOverride := fs.String("name", "", "container name (default: aisb-<dir>)")
		origin := fs.String("origin", "", "path matched against the projects config "+
			"(default: workdir). Lets a caller running in a throwaway clone route by the real source folder.")
		var labels labelFlags
		fs.Var(&labels, "label", "extra container label k=v (repeatable)")
		_ = fs.Parse(os.Args[2:])
		wd := *workdir
		// Absolutise so a relative workdir (used for remote runs, relative to
		// the SSH home) yields absolute docker bind-mount paths.
		if abs, err := filepath.Abs(wd); err == nil {
			wd = abs
		}
		// Match the projects config against the origin (the real source folder)
		// when provided, else the workdir. Mounts and the container workdir
		// still use wd — only the config-matching key changes.
		matchPath := wd
		if *origin != "" {
			matchPath = *origin
			if abs, err := filepath.Abs(*origin); err == nil {
				matchPath = abs
			}
		}
		cc := cfg.Resolve(cfgPath(), matchPath, base)
		nm := *nameOverride
		if nm == "" {
			nm = "aisb-" + sanitizeName(filepath.Base(wd))
		}
		dieOn(h.Create(nm, cc, home, wd, labels.toMap()))
	case "stop":
		dieOn(h.Stop(name))
	case "rm", "remove":
		// `aisb rm`            → current project's container
		// `aisb rm name [...]` → explicit list of aisb-* containers
		targets := []string{name}
		if len(os.Args) > 2 {
			targets = os.Args[2:]
		}
		for _, t := range targets {
			dieOn(h.RM(t))
		}
	case "status":
		dieOn(h.Status(name))
	case "ls", "list":
		dieOn(h.LS())
	case "build":
		dieOn(cmdBuild(log))
	case "-h", "--help", "help":
		usage()
	default:
		log.Die("unknown command: "+sub+" (use: up | stop | rm | status | ls | build)", 2)
	}
}
