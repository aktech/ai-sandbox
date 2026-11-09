package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// An address that never answers (a black-holed IPv6 route, a dropped SYN) must
// not stall the request: each attempt gives up after dialTimeout and the next
// address is tried, so a reachable one still connects quickly.
func TestGuardedDial_SilentAddressTimesOutAndNextIsTried(t *testing.T) {
	silent, good := netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("93.184.215.14")
	oldLookup, oldDial, oldTimeout := lookupIP, dialIP, dialTimeout
	t.Cleanup(func() { lookupIP, dialIP, dialTimeout = oldLookup, oldDial, oldTimeout })
	dialTimeout = 200 * time.Millisecond
	lookupIP = func(context.Context, string, string) ([]netip.Addr, error) { return []netip.Addr{silent, good}, nil }
	var tried []string
	dialIP = func(ctx context.Context, network, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if strings.HasPrefix(addr, "[2001:db8::1]") {
			<-ctx.Done() // never answers
			return nil, ctx.Err()
		}
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		return a, nil
	}

	start := time.Now()
	c, err := guardedDial(context.Background(), "tcp", "example.net:443", true)
	if err != nil {
		t.Fatalf("dial failed: %v (tried %v)", err, tried)
	}
	c.Close()
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v: a silent address stalled the dial", took)
	}
}

// With every address silent the dial fails within a bounded time instead of
// hanging the agent's request.
func TestGuardedDial_AllSilentFailsWithinTimeout(t *testing.T) {
	oldLookup, oldDial, oldTimeout := lookupIP, dialIP, dialTimeout
	t.Cleanup(func() { lookupIP, dialIP, dialTimeout = oldLookup, oldDial, oldTimeout })
	dialTimeout = 150 * time.Millisecond
	lookupIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::2")}, nil
	}
	dialIP = func(ctx context.Context, network, addr string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }

	start := time.Now()
	if _, err := guardedDial(context.Background(), "tcp", "example.net:443", true); err == nil {
		t.Fatal("dial to silent addresses succeeded")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("took %v: silent addresses must fail within the dial timeout", took)
	}
}
