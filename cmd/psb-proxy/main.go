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
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

const (
	payloadPath = "/run/psb/payload.json" // secrets + CA, delivered once
	rulesPath   = "/run/psb/rules.json"   // rule set, delivered on every psb
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--write-payload":
			writeFile(payloadPath)
			return
		case "--write-rules":
			writeFile(rulesPath)
			return
		}
	}
	serve()
}

// writeFile copies stdin to path with 0600 perms. Used by both delivery modes
// via `docker exec -i psb-proxy /psb-proxy --write-payload|--write-rules`.
func writeFile(path string) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatalf("psb-proxy write %s: %v", path, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, os.Stdin); err != nil {
		log.Fatalf("psb-proxy write %s: %v", path, err)
	}
}

func serve() {
	in, err := waitForSecrets(15 * time.Second)
	if err != nil {
		log.Fatalf("psb-proxy: %v", err)
	}
	srv, err := proxy.New(in.Secrets, []byte(in.CACert), []byte(in.CAKey))
	if err != nil {
		log.Fatalf("psb-proxy: %v", err)
	}
	go watchRules(srv)

	addr := ":8080"
	log.Printf("psb-proxy listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("psb-proxy: serve: %v", err)
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

// watchRules loads rulesPath whenever its modification time changes and applies
// the new rule set. This lets psb push updated per-project allowlists to a
// running proxy without a restart or any secret re-entry.
func watchRules(srv *proxy.Server) {
	var lastMod time.Time
	for {
		if fi, err := os.Stat(rulesPath); err == nil && fi.ModTime() != lastMod {
			if f, oerr := os.Open(rulesPath); oerr == nil {
				rs, rerr := readRules(f)
				f.Close()
				if rerr == nil {
					if err := srv.UpdateRules(rs); err == nil {
						lastMod = fi.ModTime()
						log.Printf("psb-proxy: rules updated (%d project(s))", len(rs.Projects))
					} else {
						log.Printf("psb-proxy: bad rules: %v", err)
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}
