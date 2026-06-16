// Package cmd implements the per-subcommand business logic for aisb.
//
// Each method on Handler corresponds to one CLI verb (`aisb up`, `aisb stop`,
// etc.) and returns an error rather than killing the process — main.go
// decides how to surface failures. The image-build verb stays in main.go
// because it is bound to an embedded asset that only the binary's package
// can reach.
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aktech/ai-sandbox/internal/cfg"
	"github.com/aktech/ai-sandbox/internal/dx"
	"github.com/aktech/ai-sandbox/internal/mountresolver"
)

// Logger is the subset of aisb's logger that command handlers need. Defining
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
	Log        Logger
	Docker     dx.Executor
	WaitCopies bool // wait for copies to complete before entering the shell
}

// ensure creates the container (or starts it if it already exists) without
// attaching a shell. extraLabels are added alongside the default aisb.cwd label.
// It returns whether a new container was created.
func (h Handler) ensure(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) (bool, error) {
	h.Log.Log(fmt.Sprintf("project: %s  container: %s", filepath.Base(cwd), name))

	if !dx.ImageExists(h.Docker, c.Image) {
		return false, fmt.Errorf("image %s not found — run `aisb build`", c.Image)
	}

	if dx.ContainerExists(h.Docker, name) {
		if !dx.ContainerRunning(h.Docker, name) {
			h.Log.Step("starting existing container")
			if err := dx.Start(h.Docker, name); err != nil {
				return false, err
			}
		} else {
			h.Log.Log("container already running")
		}
		return false, nil
	} else {
		if err := h.create(name, c, home, cwd, extraLabels); err != nil {
			return false, err
		}
		h.Log.OK("container created")
		return true, nil
	}
}

// Up creates (or starts) the container for the project rooted at cwd and
// replaces the current process with an interactive shell inside it.
func (h Handler) Up(name string, c cfg.Effective, home, cwd string) error {
	created, err := h.ensure(name, c, home, cwd, nil)
	if err != nil {
		return err
	}
	if created && len(c.Copies) > 0 {
		copies := mountresolver.ResolveCopies(c.Copies,
			mountresolver.Env{Home: home, CWD: cwd, SharedDir: c.SharedDir}, h.Log)
		h.doCopies(name, copies)
	}
	h.Log.OK("entering shell — run `pi` (or `claude`) inside")
	return dx.Shell(h.Docker, name)
}

// Create prepares the container non-interactively (no shell) and prints its
// name to stdout, so other tools can layer on top of an aisb sandbox while
// reusing aisb's mount/image configuration.
//
// Copies are intentionally skipped for create (non-interactive).
// External tools using create may have their own caching strategy,
// and copies would also force additional startup time, changing the
// non-interactive contract.
func (h Handler) Create(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) error {
	_, err := h.ensure(name, c, home, cwd, extraLabels)
	if err != nil {
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

// LS prints a colorized table of all aisb-* containers.
func (h Handler) LS() error {
	names, err := dx.ListNames(h.Docker, "aisb-")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println("no aisb-* containers")
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

// doCopies runs the resolved copy specs. In sync mode (WaitCopies=true) each
// copy runs sequentially with per-entry logging so the shell only opens after
// all copies finish. In async mode (default) copies are launched as detached
// subprocesses and the method returns immediately.
func (h Handler) doCopies(name string, copies []mountresolver.CopySpec) {
	if len(copies) == 0 {
		return
	}
	if h.WaitCopies {
		for _, cp := range copies {
			dest := name + ":" + cp.Dest
			h.Log.Step(fmt.Sprintf("copying %s -> %s", cp.Source, dest))
			if err := dx.Copy(h.Docker, cp.Source, dest); err != nil {
				h.Log.Warn(fmt.Sprintf("copy failed: %v", err))
			}
		}
	} else {
		h.Log.Log("warming caches in background...")
		for _, cp := range copies {
			dest := name + ":" + cp.Dest
			cmd := exec.Command("docker", "cp", "-a", cp.Source, dest)
			_ = cmd.Start() // detached; reparented to init when aisb exec's the shell
		}
	}
}

// create issues `docker run -d` for a fresh container. Internal helper
// shared by Up and Create. extraLabels are merged on top of aisb.cwd.
func (h Handler) create(name string, c cfg.Effective, home, cwd string, extraLabels map[string]string) error {
	h.Log.Step(fmt.Sprintf("creating container %s (image=%s, mem=%s, cpus=%s)", name, c.Image, c.Memory, c.CPUs))
	if err := os.MkdirAll(c.SharedDir, 0o755); err != nil {
		return err
	}
	mounts := mountresolver.Resolve(c.Mounts, c.ExtraMounts,
		mountresolver.Env{Home: home, CWD: cwd, SharedDir: c.SharedDir}, h.Log)
	labels := map[string]string{"aisb.cwd": cwd}
	for k, v := range extraLabels {
		labels[k] = v
	}
	return dx.Create(h.Docker, dx.ContainerSpec{
		Name:    name,
		Image:   c.Image,
		Memory:  c.Memory,
		CPUs:    c.CPUs,
		Workdir: cwd,
		Labels:  labels,
		Env: map[string]string{
			"HOME":              home,
			"SB_SHARED":         c.SharedDir,
			"HOMELAB_URL":       os.Getenv("HOMELAB_URL"),
			"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"),
		},
		Mounts: mounts,
		Ports:  c.Ports,
	})
}
