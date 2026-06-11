// Command psb-proxy is the credential-injecting egress proxy that runs in
// its own container. It reads a single JSON object on stdin (CA cert+key,
// secrets, allow/inject config), then serves HTTP CONNECT on :8080. Nothing
// sensitive is taken from argv or the environment.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

func main() {
	in, err := readInput(os.Stdin)
	if err != nil {
		log.Fatalf("psb-proxy: read stdin: %v", err)
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
