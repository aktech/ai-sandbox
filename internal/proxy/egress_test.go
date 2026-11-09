package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aktech/ai-sandbox/internal/ca"
)

// "*" opens the internet, not the machine the proxy runs on: loopback,
// private, link-local (cloud metadata) and other sandboxes' addresses stay
// unreachable through a wildcard, by IP literal or by a name resolving there.
// An entry naming the host explicitly still reaches it.
func TestServer_StarDoesNotReachLocalAddresses(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "local service")
	}))
	defer up.Close()
	upHost := up.Listener.Addr().String()
	port := upHost[strings.LastIndexByte(upHost, ':')+1:]
	// A raw listener counts tunnel connections (CONNECT is never MITM'd here).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()

	caCert, caKey, _ := ca.Generate("test")
	srv, _ := New(map[string]string{}, caCert, caKey)
	_ = srv.UpdateRules(RuleSet{Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{"*"}}}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := newClient(t, ts.URL, caCert)

	for _, u := range []string{"http://" + upHost + "/", "http://localhost:" + port + "/", "https://" + ln.Addr().String() + "/"} {
		resp, err := client.Get(u)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("GET %s through \"*\": status 200, want refused", u)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a local service was reached %d times through \"*\"", n)
	}

	// Naming it explicitly opens it.
	_ = srv.UpdateRules(RuleSet{Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{"*", upHost}}}})
	resp, err := client.Get("http://" + upHost + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("explicit entry: status %d, want 200", resp.StatusCode)
	}
}
