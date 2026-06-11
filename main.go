// psb — launch a Docker (colima) sandbox per project. Image bundles `pi`
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

// labelFlags collects repeated -label k=v values for `psb create`.
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
	return envDefault("PSB_CONFIG_FILE", filepath.Join(os.Getenv("HOME"), ".config", "ai-sandbox", "config.json"))
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
	image := envDefault("PSB_IMAGE_NAME", "ai-sandbox-pi:latest")
	piVersion := envDefault("PI_VERSION", "v0.79.1")
	uid := fmt.Sprintf("%d", os.Getuid())
	gid := fmt.Sprintf("%d", os.Getgid())
	home := os.Getenv("HOME")
	if home == "" {
		return fmt.Errorf("HOME not set")
	}

	tmp, err := os.MkdirTemp("", "psb-build-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := os.WriteFile(filepath.Join(tmp, "Dockerfile"), embeddedDockerfile, 0o644); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}

	log.Step(fmt.Sprintf("building %s (pi=%s, home=%s, uid=%s, gid=%s)", image, piVersion, home, uid, gid))
	args := []string{"build",
		"--build-arg", "PI_VERSION=" + piVersion,
		"--build-arg", "AGENT_UID=" + uid,
		"--build-arg", "AGENT_GID=" + gid,
		"--build-arg", "AGENT_HOME=" + home,
		"-t", image,
	}
	// GITHUB_TOKEN raises the API rate limit for mise's attestation checks
	// during the build. Passed as a BuildKit secret so it never lands in a
	// layer; the value itself is read by docker from the environment.
	if os.Getenv("GITHUB_TOKEN") != "" {
		args = append(args, "--secret", "id=github_token,env=GITHUB_TOKEN")
	}
	args = append(args, tmp)
	build := exec.Command("docker", args...)
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
	fmt.Println(`psb — launch a Docker sandbox per project. Image bundles pi + claude.

Usage:
  psb              create or attach + shell into psb-<project>
  psb stop         stop the project's container
  psb rm [n...]    destroy current container, or named ones
  psb status       show container status
  psb ls           list all psb-* containers
  psb build        (re)build the image
  psb proxy init   generate the egress-proxy CA + secret store
  psb proxy stop   stop the shared credential proxy
  psb proxy log    stream the proxy's allow/deny log
  psb secret set <name>   store a secret (no echo)
  psb secret rm  <name>   remove a secret
  psb secret ls           list stored secret names

Config file (JSON):
  ` + filepath.Join(os.Getenv("HOME"), ".config/ai-sandbox/config.json") + `

  Keys:
    mounts         declarative mount list; each entry is "src" or "src:dest"
    extra_mounts   appended after mounts
    memory / cpus  resource limits
    image          custom image tag

  Template vars in mounts: {{HOME}}, {{SHARED_DIR}}, {{CWD}}
  Also supports ~/ and $ENV_VAR expansion.

Env vars (override config defaults):
  PSB_IMAGE_NAME   image tag (default: ai-sandbox-pi:latest)
  PSB_MEMORY       memory limit (default: 4g)
  PSB_CPUS         cpu limit (default: 2)
  PSB_SHARED_DIR   host↔container exchange dir (default: ~/sb-shared)
  PSB_CONFIG_FILE  config file path (default: ~/.config/ai-sandbox/config.json)
  HOMELAB_URL      passed through to container
  ANTHROPIC_API_KEY passed through to container (claude API auth)`)
}

func main() {
	log := newLogger("psb")
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
		Image:     envDefault("PSB_IMAGE_NAME", "ai-sandbox-pi:latest"),
		Memory:    envDefault("PSB_MEMORY", "4g"),
		CPUs:      envDefault("PSB_CPUS", "2"),
		SharedDir: envDefault("PSB_SHARED_DIR", filepath.Join(home, "sb-shared")),
	}
	c := cfg.Resolve(cfgPath(), cwd, base)

	name, err := containerName("psb")
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
		// its name. Lets other tools (e.g. darb) reuse psb's sandbox.
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		workdir := fs.String("workdir", cwd, "project directory to sandbox")
		nameOverride := fs.String("name", "", "container name (default: psb-<dir>)")
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
			nm = "psb-" + sanitizeName(filepath.Base(wd))
		}
		dieOn(h.Create(nm, cc, home, wd, labels.toMap()))
	case "stop":
		dieOn(h.Stop(name))
	case "rm", "remove":
		// `psb rm`            → current project's container
		// `psb rm name [...]` → explicit list of psb-* containers
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
	case "secret":
		// psb secret set|rm|ls <name>
		dieOn(dispatchSecret(h, os.Args[2:]))
	case "proxy":
		// psb proxy init|stop|log
		dieOn(dispatchProxy(h, os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		log.Die("unknown command: "+sub+" (use: up | stop | rm | status | ls | build | secret | proxy)", 2)
	}
}

const secretHelp = `psb secret - manage the encrypted secret store

Secrets are kept in an encrypted file on the host (~/.config/ai-sandbox/secrets.enc),
unlocked with your master password. Sandboxes never see the real values, only
sentinels that the proxy swaps for the real secret on the way out.

Usage:
  psb secret set <name>    store/replace a secret named <name>
  psb secret rm  <name>    remove a secret
  psb secret ls            list stored secret names (never values)

The value for 'set' is read from stdin (pipe it, or paste at the masked prompt),
never from an argument, so it can't leak via shell history or the process list.

Examples:
  printf '%s' "$GITHUB_TOKEN" | psb secret set github
  psb secret set openai < token.txt
  psb secret set github            # prompts: master password, then the value
  psb secret ls
  psb secret rm openai

Notes:
  - <name> must match the "secret" field in your proxy.inject rules and the
    proxy.env map in config.json.
  - Set PSB_MASTER_PASSWORD to avoid the master-password prompt.
  - The proxy loads secrets once at startup; run 'psb proxy stop' then re-enter
    a sandbox so a new/changed secret takes effect.`

const proxyHelp = `psb proxy - manage the credential-injecting egress proxy

The proxy is one container that every sandbox routes through. It holds the real
secrets in memory, injects them into allowed requests, and enforces a per-project
domain allowlist. Sandboxes sit on a private network whose only exit is the proxy.

Usage:
  psb proxy init    generate the CA + an empty secret store (run once)
  psb proxy stop    stop the shared proxy container
  psb proxy log     stream the proxy's live ALLOW / DENY decisions

Examples:
  psb proxy init                   # first-time setup; sets the master password
  psb proxy log                    # watch traffic decisions
  psb proxy stop                   # then re-enter a sandbox to reload secrets/rules

Notes:
  - 'init' will not overwrite an existing CA (so already-shared certs stay valid).
  - Egress rules live under the "proxy" block in config.json (allow / extra_allow,
    "*" for unrestricted, [] for a full airgap; inject + env declare secret use).`

// dispatchSecret routes `psb secret <action> [name]`.
func dispatchSecret(h cmd.Handler, args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		os.Stdout.WriteString(secretHelp + "\n")
		return nil
	}
	switch args[0] {
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: psb secret set <name>\n(run `psb secret` for help)")
		}
		return h.SecretSet(args[1])
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: psb secret rm <name>\n(run `psb secret` for help)")
		}
		return h.SecretRM(args[1])
	case "ls":
		return h.SecretLS()
	default:
		return fmt.Errorf("unknown secret action %q (use: set | rm | ls; `psb secret` for help)", args[0])
	}
}

// dispatchProxy routes `psb proxy <action>`.
func dispatchProxy(h cmd.Handler, args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		os.Stdout.WriteString(proxyHelp + "\n")
		return nil
	}
	switch args[0] {
	case "init":
		return h.ProxyInit()
	case "stop":
		return h.ProxyStop()
	case "log", "logs":
		return h.ProxyLog()
	default:
		return fmt.Errorf("unknown proxy action %q (use: init | stop | log; `psb proxy` for help)", args[0])
	}
}

// isHelp reports whether an arg is a help flag.
func isHelp(s string) bool {
	return s == "-h" || s == "--help" || s == "help"
}
