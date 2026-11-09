package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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

// setDial swaps the resolver, dialer and timings for one test.
func setDial(t *testing.T, ips []netip.Addr, attempt, delay, budget time.Duration, dial func(ctx context.Context, network, addr string) (net.Conn, error)) {
	t.Helper()
	oldLookup, oldDial, oldAttempt, oldDelay, oldBudget := lookupIP, dialIP, attemptTimeout, raceDelay, dialBudget
	t.Cleanup(func() {
		lookupIP, dialIP, attemptTimeout, raceDelay, dialBudget = oldLookup, oldDial, oldAttempt, oldDelay, oldBudget
	})
	attemptTimeout, raceDelay, dialBudget = attempt, delay, budget
	lookupIP = func(context.Context, string, string) ([]netip.Addr, error) { return ips, nil }
	dialIP = dial
}

// pipe returns one end of an in-memory connection, closed when the test ends.
func pipe(t *testing.T) net.Conn {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a
}

// An address that never answers (a black-holed IPv6 route) must not stall the
// request: a reachable address is raced in shortly after and wins.
func TestGuardedDial_SilentAddressTimesOutAndNextIsTried(t *testing.T) {
	silent, good := netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("93.184.215.14")
	var mu sync.Mutex
	var tried []string
	setDial(t, []netip.Addr{silent, good}, time.Second, 50*time.Millisecond, 5*time.Second,
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			tried = append(tried, addr)
			mu.Unlock()
			if strings.HasPrefix(addr, "[2001:db8::1]") {
				<-ctx.Done() // never answers
				return nil, ctx.Err()
			}
			return pipe(t), nil
		})

	start := time.Now()
	c, err := guardedDial(context.Background(), "tcp", "example.net:443", true)
	if err != nil {
		t.Fatalf("dial failed: %v (tried %v)", err, tried)
	}
	c.Close()
	if took := time.Since(start); took > 300*time.Millisecond {
		t.Fatalf("took %v: a silent address stalled the dial", took)
	}
}

// A dropped first SYN to the only address (a NAT that loses the first packet
// of a new flow) is retried on a fresh connection well before the OS's own
// retransmit, for an explicitly allowed host as well as through "*".
func TestGuardedDial_DroppedFirstAttemptIsRetriedQuickly(t *testing.T) {
	for _, wildcard := range []bool{true, false} {
		var n atomic.Int32
		setDial(t, []netip.Addr{netip.MustParseAddr("160.79.104.10")}, time.Second, 100*time.Millisecond, 5*time.Second,
			func(ctx context.Context, network, addr string) (net.Conn, error) {
				if n.Add(1) == 1 {
					<-ctx.Done() // the first SYN is lost
					return nil, ctx.Err()
				}
				return pipe(t), nil
			})
		start := time.Now()
		c, err := guardedDial(context.Background(), "tcp", "api.example.com:443", wildcard)
		if err != nil {
			t.Fatalf("wildcard=%v: dial failed: %v", wildcard, err)
		}
		c.Close()
		if took := time.Since(start); took > 400*time.Millisecond {
			t.Fatalf("wildcard=%v: took %v, want the racing attempt to win in about 100ms", wildcard, took)
		}
		if got := n.Load(); got < 2 {
			t.Fatalf("wildcard=%v: %d attempts, want a second one", wildcard, got)
		}
	}
}

// An attempt that hangs is abandoned after attemptTimeout and the address is
// tried again, so a lost SYN costs at most one short attempt.
func TestGuardedDial_EachAttemptIsBounded(t *testing.T) {
	var mu sync.Mutex
	var spans []time.Duration
	var n atomic.Int32
	setDial(t, []netip.Addr{netip.MustParseAddr("160.79.104.10")}, 150*time.Millisecond, time.Hour, 5*time.Second,
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			if n.Add(1) <= 2 {
				s := time.Now()
				<-ctx.Done()
				mu.Lock()
				spans = append(spans, time.Since(s))
				mu.Unlock()
				return nil, ctx.Err()
			}
			return pipe(t), nil
		})
	c, err := guardedDial(context.Background(), "tcp", "api.example.com:443", false)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	for _, d := range spans {
		if d > 400*time.Millisecond {
			t.Fatalf("an attempt ran %v, want it cut at attemptTimeout", d)
		}
	}
}

// IPv4 is tried first: a sandbox VM often has no IPv6 route.
func TestGuardedDial_PrefersIPv4(t *testing.T) {
	var first atomic.Value
	setDial(t, []netip.Addr{netip.MustParseAddr("2606:4700::1"), netip.MustParseAddr("93.184.215.14")}, time.Second, time.Second, 5*time.Second,
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			first.CompareAndSwap(nil, addr)
			return pipe(t), nil
		})
	c, err := guardedDial(context.Background(), "tcp", "example.net:443", true)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if got := first.Load(); got != "93.184.215.14:443" {
		t.Fatalf("first attempt went to %v, want the IPv4 address", got)
	}
}

// When two racing attempts both connect, the loser is closed, not leaked.
func TestGuardedDial_ClosesTheLosingConnection(t *testing.T) {
	var closed atomic.Int32
	release := make(chan struct{})
	var n atomic.Int32
	setDial(t, []netip.Addr{netip.MustParseAddr("93.184.215.14")}, time.Second, 20*time.Millisecond, 5*time.Second,
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			if n.Add(1) == 1 {
				<-release // connects, but only after the second attempt won
			}
			return &closeCounter{Conn: pipe(t), n: &closed}, nil
		})
	c, err := guardedDial(context.Background(), "tcp", "example.net:443", true)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if closed.Load() != 1 {
		t.Fatalf("losing connection closed %d times, want 1", closed.Load())
	}
	c.Close()
}

type closeCounter struct {
	net.Conn
	n *atomic.Int32
}

func (c *closeCounter) Close() error { c.n.Add(1); return c.Conn.Close() }

// With every address silent the dial fails within the overall budget instead
// of hanging the agent's request.
func TestGuardedDial_AllSilentFailsWithinBudget(t *testing.T) {
	setDial(t, []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::2")}, 100*time.Millisecond, 30*time.Millisecond, 400*time.Millisecond,
		func(ctx context.Context, network, addr string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() })

	start := time.Now()
	if _, err := guardedDial(context.Background(), "tcp", "example.net:443", true); err == nil {
		t.Fatal("dial to silent addresses succeeded")
	}
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Fatalf("took %v: silent addresses must fail within the dial budget", took)
	}
}

// A host that refuses fails fast with the refusal, not after the budget.
func TestGuardedDial_RefusedFailsFast(t *testing.T) {
	setDial(t, []netip.Addr{netip.MustParseAddr("93.184.215.14")}, time.Second, 50*time.Millisecond, 5*time.Second,
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		})
	start := time.Now()
	_, err := guardedDial(context.Background(), "tcp", "example.net:443", false)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("err = %v, want connection refused", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("took %v: a refusal must not be retried until the budget runs out", took)
	}
}
