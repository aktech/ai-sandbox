// Command aisb-proxy is the credential-injecting egress proxy that runs in its
// own container. Startup flow:
//
//  1. aisb starts the container detached.
//  2. aisb delivers the secrets payload (CA cert+key + secret values) by
//     running this binary with --write-payload via `docker exec -i`; it lands
//     on a tmpfs file. The serving process reads it, unlinks it and builds the
//     proxy. Later, `aisb secret set` / `rm` deliver just the secret values
//     with --write-secrets to a separate file; the serving process watches it
//     and swaps them in without a restart (the CA is fixed for its lifetime).
//     The two files never share a path, so a refresh cannot be mistaken for
//     the start-up payload.
//  3. On every `aisb`, aisb delivers the current rule set (per-project + universal
//     allowlists, inject rules) via --write-rules. The rule set carries no
//     secret values, so it needs no store unlock. The serving process watches
//     that file and hot-swaps the rules live.
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
	payloadPath = "/run/aisb/payload.json" // secrets + CA, delivered once
	secretsPath = "/run/aisb/secrets.json" // refreshed secrets, delivered on change
	rulesDir    = "/run/aisb/rules.d"      // one rule file per project, delivered per aisb
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--write-payload":
			writeFile(payloadPath, os.Stdin)
			return
		case "--write-secrets":
			writeFile(secretsPath, os.Stdin)
			return
		case "--write-rules":
			// `--write-rules <key>`: store this project's rule file as
			// rules.d/<key>.json. Each project owns its own file, so concurrent
			// aisb runs never clobber one another.
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

// writeFile copies r to path with 0600 perms. It writes a temporary file in
// the same directory and renames it into place, so a watcher polling path
// never reads a half-written file.
func writeFile(path string, r io.Reader) {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		die("write file", "path", path, "err", err)
	}
	tmp := f.Name()
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
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
	go watchSecrets(secretsPath, 500*time.Millisecond, nil, func(secrets map[string]string) {
		srv.UpdateSecrets(secrets)
		slog.Info("secrets updated", "count", len(secrets))
	})

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
		in, err := takePayload(payloadPath)
		if err == nil || !os.IsNotExist(err) {
			return in, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// takePayload reads and parses the payload at path, then unlinks it whether or
// not it parsed, so plaintext secrets never linger and a bad payload is not
// retried forever. A missing file returns an os.IsNotExist error.
func takePayload(path string) (*secretsInput, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	in, err := readSecrets(f)
	f.Close()
	_ = os.Remove(path)
	return in, err
}

// watchSecrets polls path every interval and hands the secrets of each new
// payload to apply, until stop is closed (nil: forever). A payload that does
// not parse is logged and dropped; the secrets in use stay as they were.
func watchSecrets(path string, every time.Duration, stop <-chan struct{}, apply func(map[string]string)) {
	for {
		in, err := takePayload(path)
		switch {
		case err == nil:
			apply(in.Secrets)
		case !os.IsNotExist(err):
			slog.Error("bad secrets payload, keeping current secrets", "err", err)
		}
		select {
		case <-stop:
			return
		case <-time.After(every):
		}
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
