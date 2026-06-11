// Package cmd implements the per-subcommand business logic for psb.
//
// Each method on Handler corresponds to one CLI verb (`psb up`, `psb stop`,
// etc.) and returns an error rather than killing the process — main.go
// decides how to surface failures. The image-build verb stays in main.go
// because it is bound to an embedded asset that only the binary's package
// can reach.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/dx"
	"github.com/aktech/ai-sandbox/internal/mountresolver"
	"github.com/aktech/ai-sandbox/internal/proxy"
)

// Logger is the subset of psb's logger that command handlers need. Defining
// it here (rather than importing main's logger type) keeps cmd self-
// contained and lets tests stub it. Die is intentionally absent — handlers
// return errors instead of calling os.Exit.
type Logger interface {
	Log(string)
	Step(string)
	OK(string)
	Warn(string)
}

// Handler bundles the dependencies every command needs.
type Handler struct {
	Log    Logger
	Docker dx.Executor

	// caHostPath is the host path of the proxy CA cert, stashed by ensure when
	// proxy mode is active so create/buildSpec can mount it into the sandbox.
	caHostPath string
}

// ensure creates the container (or starts it if it already exists) without
// attaching a shell. extraLabels are added alongside the default psb.cwd label.
func (h Handler) ensure(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) error {
	h.Log.Log(fmt.Sprintf("project: %s  container: %s", filepath.Base(cwd), name))

	if !dx.ImageExists(h.Docker, c.Image) {
		return fmt.Errorf("image %s not found — run `psb build`", c.Image)
	}

	// Proxy mode: bring up the shared credential proxy and the sandbox's own
	// internal network before creating/starting the container. caHostPath is
	// just a path (no unlock needed) so create -> buildSpec can mount the CA.
	// The master password is only required to (re)start the proxy, so when the
	// proxy is already running we skip the unlock entirely and re-entering a
	// sandbox is prompt-free.
	if c.Proxy != nil {
		h.caHostPath = caCertPath()
		if !dx.ContainerRunning(h.Docker, proxyContainer) {
			payload, _, err := h.proxyPayload(c)
			if err != nil {
				return err
			}
			if err := ensureProxyRunning(h.Docker, proxyImage(), payload); err != nil {
				return fmt.Errorf("start proxy: %w", err)
			}
		}
		if err := setupSandboxNet(h.Docker, name); err != nil {
			return fmt.Errorf("setup sandbox network: %w", err)
		}
		// Push this project's allowlist to the proxy (no password needed). The
		// proxy merges it by subnet, so other sandboxes keep their own rules.
		if err := deliverRules(h.Docker, c, name); err != nil {
			return fmt.Errorf("deliver proxy rules: %w", err)
		}
	}

	if dx.ContainerExists(h.Docker, name) {
		if !dx.ContainerRunning(h.Docker, name) {
			h.Log.Step("starting existing container")
			if err := dx.Start(h.Docker, name); err != nil {
				return err
			}
		} else {
			h.Log.Log("container already running")
		}
	} else {
		if err := h.create(name, c, home, cwd, extraLabels); err != nil {
			return err
		}
		h.Log.OK("container created")
	}
	return nil
}

// Up creates (or starts) the container for the project rooted at cwd and
// replaces the current process with an interactive shell inside it.
func (h Handler) Up(name string, c cfg.Effective, home, cwd string) error {
	if err := h.ensure(name, c, home, cwd, nil); err != nil {
		return err
	}
	h.Log.OK("entering shell — run `pi` (or `claude`) inside")
	return dx.Shell(h.Docker, name)
}

// Create prepares the container non-interactively (no shell) and prints its
// name to stdout, so other tools can layer on top of a psb sandbox while
// reusing psb's mount/image configuration.
func (h Handler) Create(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) error {
	if err := h.ensure(name, c, home, cwd, extraLabels); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// Stop halts a running container without removing it.
func (h Handler) Stop(name string) error {
	if !dx.ContainerExists(h.Docker, name) {
		return fmt.Errorf("container %s does not exist", name)
	}
	h.Log.Step("stopping " + name)
	if err := dx.Stop(h.Docker, name); err != nil {
		return err
	}
	h.Log.OK("stopped")
	return nil
}

// RM force-removes a container.
func (h Handler) RM(name string) error {
	if !dx.ContainerExists(h.Docker, name) {
		return fmt.Errorf("container %s does not exist", name)
	}
	h.Log.Step("removing " + name + " (force)")
	if err := dx.Remove(h.Docker, name); err != nil {
		return err
	}
	// Tear down the sandbox's private network if proxy mode created one. The
	// shared proxy container is left running for other sandboxes; stop it
	// explicitly with `psb proxy stop`.
	teardownSandboxNet(h.Docker, name)
	h.Log.OK("removed")
	return nil
}

// Status prints a one-row docker ps table for `name`. A missing container
// is logged as a warning rather than treated as an error.
func (h Handler) Status(name string) error {
	if !dx.ContainerExists(h.Docker, name) {
		h.Log.Warn("container " + name + " does not exist")
		return nil
	}
	return dx.PrintStatusTable(h.Docker, name)
}

// LS prints a colorized table of all psb-* containers.
func (h Handler) LS() error {
	names, err := dx.ListNames(h.Docker, "psb-")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("no psb-* containers")
		return nil
	}
	infos, err := dx.Inspect(h.Docker, names...)
	if err != nil {
		return err
	}
	rows := [][5]string{{"name", "uptime", "cpus", "mem", "cwd"}}
	for _, i := range infos {
		rows = append(rows, [5]string{i.Name, dx.CompactUptime(i.Status, i.StartedAt),
			dx.CompactCPUs(i.NanoCpus), dx.CompactMem(i.Memory), i.CWDLabel})
	}
	w := [5]int{}
	for _, r := range rows {
		for i, c := range r {
			if n := len(c); n > w[i] {
				w[i] = n
			}
		}
	}
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	for i, r := range rows {
		c0 := pad(r[0], w[0])
		c1 := pad(r[1], w[1])
		c2 := pad(r[2], w[2])
		c3 := pad(r[3], w[3])
		c4 := r[4]
		if i == 0 {
			fmt.Printf("\033[1;36m%s\033[0m  \033[1;36m%s\033[0m  \033[1;36m%s\033[0m  \033[1;36m%s\033[0m  \033[1;36m%s\033[0m\n",
				c0, c1, c2, c3, c4)
		} else {
			st := fmt.Sprintf("\033[2m%s\033[0m", c1)
			if last := r[1][len(r[1])-1]; last == 's' || last == 'm' || last == 'h' || last == 'd' {
				st = fmt.Sprintf("\033[32m%s\033[0m", c1)
			}
			fmt.Printf("\033[35m%s\033[0m  %s  \033[33m%s\033[0m  \033[33m%s\033[0m  \033[34m%s\033[0m\n",
				c0, st, c2, c3, c4)
		}
	}
	return nil
}

// create issues `docker run -d` for a fresh container. Internal helper
// shared by Up and Create. extraLabels are merged on top of psb.cwd.
func (h Handler) create(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) error {
	h.Log.Step(fmt.Sprintf("creating container %s (image=%s, mem=%s, cpus=%s)", name, c.Image, c.Memory, c.CPUs))
	if err := os.MkdirAll(c.SharedDir, 0o755); err != nil {
		return err
	}
	spec := buildSpec(name, c, home, cwd, extraLabels, h.caHostPath, h.Log)
	return dx.Create(h.Docker, spec)
}

// caInContainer is where the proxy's CA cert is mounted inside the sandbox.
const caInContainer = "/etc/psb/ca.crt"

// buildSpec assembles the docker run spec. When c.Proxy is nil it reproduces
// the original behavior exactly (real ANTHROPIC_API_KEY, default network). When
// c.Proxy is set it swaps secret env vars for sentinels, attaches the sandbox
// to its private internal network, mounts the CA cert read-only, and points the
// standard CA env vars at it so claude/gh/git/curl trust the MITM proxy.
func buildSpec(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string, caHostPath string, warn mountresolver.Warner) dx.ContainerSpec {
	mounts := mountresolver.Resolve(c.Mounts, c.ExtraMounts,
		mountresolver.Env{Home: home, CWD: cwd, SharedDir: c.SharedDir}, warn)
	labels := map[string]string{"psb.cwd": cwd}
	for k, v := range extraLabels {
		labels[k] = v
	}
	env := map[string]string{
		"HOME":        home,
		"SB_SHARED":   c.SharedDir,
		"HOMELAB_URL": os.Getenv("HOMELAB_URL"),
	}
	spec := dx.ContainerSpec{
		Name: name, Image: c.Image, Memory: c.Memory, CPUs: c.CPUs,
		Workdir: cwd, Labels: labels, Env: env, Mounts: mounts, Ports: c.Ports,
	}
	if c.Proxy == nil {
		env["ANTHROPIC_API_KEY"] = os.Getenv("ANTHROPIC_API_KEY")
		return spec
	}
	// Proxy mode: no real secrets enter the container.
	spec.Network = networkName(name)
	proxyURL := "http://" + proxyContainer + ":8080"
	env["HTTPS_PROXY"] = proxyURL
	env["HTTP_PROXY"] = proxyURL
	env["NO_PROXY"] = "localhost,127.0.0.1"
	spec.Mounts = append(spec.Mounts, caHostPath+":"+caInContainer+":ro")
	for _, v := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS",
		"REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		env[v] = caInContainer
	}
	// Sentinels: for each secret the config maps to an env var, set that env
	// var to the secret's sentinel. Entirely config-driven (proxy.env); no
	// secret name or env var is hardcoded.
	for sec, ev := range c.Proxy.Env {
		env[ev] = proxy.Sentinel(sec)
	}
	return spec
}
