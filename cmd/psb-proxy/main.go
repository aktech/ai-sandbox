// Command psb-proxy is the credential-injecting egress proxy that runs in its
// own container. Startup flow:
//
//   1. psb starts the container detached.
//   2. psb delivers the secrets payload (CA cert+key + secret values) by
//      running this binary with --write-payload via `docker exec -i`; it lands
//      on a tmpfs file. The serving process reads it once and builds the proxy.
//   3. On every `psb`, psb delivers the current rule set (per-project + universal
//      allowlists, inject rules) via --write-rules. The rule set carries no
//      secret values, so it needs no store unlock. The serving process watches
//      that file and hot-swaps the rules live.
//
// Nothing sensitive is taken from argv or the environment, nor written to any
// host disk: the tmpfs is RAM-backed inside the container.
package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// die logs a final error and exits non-zero (slog has no Fatal).
func die(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

const (
	payloadPath = "/run/psb/payload.json" // secrets + CA, delivered once
	rulesDir    = "/run/psb/rules.d"      // one rule file per project, delivered per psb
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--write-payload":
			writeFile(payloadPath, os.Stdin)
			return
		case "--write-rules":
			// `--write-rules <key>`: store this project's rule file as
			// rules.d/<key>.json. Each project owns its own file, so concurrent
			// psb runs never clobber one another.
			if len(os.Args) < 3 {
				die("missing key", "arg", "--write-rules")
			}
			_ = os.MkdirAll(rulesDir, 0o700)
			writeFile(filepath.Join(rulesDir, sanitizeKey(os.Args[2])+".json"), os.Stdin)
			return
		case "--rm-rules":
			if len(os.Args) < 3 {
				die("missing key", "arg", "--rm-rules")
			}
			_ = os.Remove(filepath.Join(rulesDir, sanitizeKey(os.Args[2])+".json"))
			return
		}
	}
	serve()
}

// sanitizeKey keeps a filename safe (alnum, dash, underscore).
func sanitizeKey(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// writeFile copies r to path with 0600 perms.
func writeFile(path string, r io.Reader) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		die("write file", "path", path, "err", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		die("write file", "path", path, "err", err)
	}
}

func serve() {
	in, err := waitForSecrets(15 * time.Second)
	if err != nil {
		die("waiting for secrets payload", "err", err)
	}
	srv, err := proxy.New(in.Secrets, []byte(in.CACert), []byte(in.CAKey))
	if err != nil {
		die("building proxy", "err", err)
	}
	go watchRules(srv)

	addr := ":8080"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		die("serve", "err", err)
	}
}

// waitForSecrets polls payloadPath until it exists and parses, then unlinks it
// so the plaintext secrets do not linger even in RAM.
func waitForSecrets(timeout time.Duration) (*secretsInput, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.Open(payloadPath)
		if err == nil {
			in, perr := readSecrets(f)
			f.Close()
			if perr != nil {
				return nil, perr
			}
			_ = os.Remove(payloadPath)
			return in, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// watchRules rebuilds the full rule set from every file in rules.d whenever the
// directory's contents change, and applies it. Because each project owns its
// own file and the proxy unions them all, no update is ever lost to a race, and
// removing a project's file drops its rules on the next poll.
func watchRules(srv *proxy.Server) {
	var lastSig string
	for {
		sig, sets := readAllRules()
		if sig != lastSig {
			combined := proxy.CombineRuleSets(sets)
			if err := srv.UpdateRules(combined); err == nil {
				lastSig = sig
				slog.Info("rules updated", "projects", len(combined.Projects))
			} else {
				slog.Error("bad rules", "err", err)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// readAllRules reads every *.json in rules.d into a slice of RuleSets and
// returns a signature (names + mtimes) used to detect changes cheaply.
func readAllRules() (sig string, sets []proxy.RuleSet) {
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		return "", nil
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		sig += e.Name() + info.ModTime().String() + ";"
		f, oerr := os.Open(filepath.Join(rulesDir, e.Name()))
		if oerr != nil {
			continue
		}
		rs, rerr := readRules(f)
		f.Close()
		if rerr == nil {
			sets = append(sets, rs)
		}
	}
	return sig, sets
}
