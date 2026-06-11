// Command psb-proxy is the credential-injecting egress proxy that runs in
// its own container. The container is started detached; psb then delivers the
// payload (CA cert+key, secrets, allow/inject config) by running this same
// binary with --write-payload via `docker exec -i`, which writes the JSON to
// a tmpfs (RAM-backed) file. The serving process polls for that file, reads
// it, unlinks it, and serves HTTP CONNECT on :8080. Nothing sensitive is ever
// taken from argv or the environment, nor written to any host disk.
package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// payloadPath is on a tmpfs mount (see proxyRunArgs --tmpfs), so the decrypted
// secrets live only in the container's RAM and never touch disk.
const payloadPath = "/run/psb/payload.json"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--write-payload" {
		writePayload()
		return
	}
	serve()
}

// writePayload copies stdin to the tmpfs payload file with 0600 perms. Invoked
// via `docker exec -i psb-proxy /psb-proxy --write-payload`.
func writePayload() {
	f, err := os.OpenFile(payloadPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatalf("psb-proxy --write-payload: open: %v", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, os.Stdin); err != nil {
		log.Fatalf("psb-proxy --write-payload: write: %v", err)
	}
}

// serve waits for the payload file to appear (psb writes it just after the
// container starts), reads and removes it, then runs the proxy.
func serve() {
	in, err := waitForPayload(15 * time.Second)
	if err != nil {
		log.Fatalf("psb-proxy: %v", err)
	}
	srv, err := proxy.New(&in.Config, in.Secrets, []byte(in.CACert), []byte(in.CAKey))
	if err != nil {
		log.Fatalf("psb-proxy: %v", err)
	}
	addr := ":8080"
	log.Printf("psb-proxy listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Handler()); err != nil {
		log.Fatalf("psb-proxy: serve: %v", err)
	}
}

// waitForPayload polls payloadPath until it exists and parses, or timeout.
// On success it unlinks the file so the plaintext does not linger even in RAM.
func waitForPayload(timeout time.Duration) (*input, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.Open(payloadPath)
		if err == nil {
			in, perr := readInput(f)
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
