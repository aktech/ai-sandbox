package proxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/ca"
)

// newClient builds an HTTP client that trusts caCertPEM and routes through
// the proxy at proxyURL.
func newClient(t *testing.T, proxyURL string, caCertPEM []byte) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCertPEM)
	pu, _ := url.Parse(proxyURL)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

func TestServer_AllowedHostInjectsHeader(t *testing.T) {
	// upstream echoes the x-api-key header it received.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("x-api-key"))
	}))
	defer upstream.Close()
	upHost := upstream.Listener.Addr().String() // host:port

	caCert, caKey, err := ca.Generate("test")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(map[string]string{"anthropic": "REALKEY"}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	// The httptest client connects from 127.0.0.1, so a 127/8 project rule
	// applies. The inject rule makes the proxy MITM + swap the header.
	if err := srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{hostOnly(upHost)}}},
		Inject:   map[string]InjectRule{hostOnly(upHost): {Header: "x-api-key", Secret: "anthropic"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Trust the self-signed httptest upstream (production uses system roots).
	upPool := x509.NewCertPool()
	upPool.AddCert(upstream.Certificate())
	srv.TrustUpstreamRoots(upPool)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newClient(t, ts.URL, caCert)
	req, _ := http.NewRequest("GET", "https://"+upHost+"/", nil)
	req.Header.Set("x-api-key", Sentinel("anthropic"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "REALKEY" {
		t.Fatalf("upstream saw x-api-key=%q, want REALKEY", string(body))
	}
}

// An allowlisted host with NO inject rule must be tunneled end-to-end (plain
// CONNECT, no MITM), so the proxy never sees the bytes. We prove this by
// trusting ONLY the upstream's own cert (not the proxy CA): if the proxy tried
// to MITM, the client would reject the proxy-minted cert and the request fails.
func TestServer_AllowOnlyHostIsTunneledNotMitmed(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("x-test"))
	}))
	defer upstream.Close()
	upHost := upstream.Listener.Addr().String()

	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(map[string]string{}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	// Allowed (bare host) via a 127/8 project rule, but no inject rule.
	if err := srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{hostOnly(upHost)}}},
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Trust ONLY the upstream's real cert, NOT the proxy CA.
	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	pu, _ := url.Parse(ts.URL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}

	req, _ := http.NewRequest("GET", "https://"+upHost+"/", nil)
	req.Header.Set("x-test", "passthrough")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("plain tunnel should succeed trusting only upstream cert: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "passthrough" {
		t.Fatalf("upstream saw x-test=%q, want passthrough", string(body))
	}
}

func TestServer_DeniedHostRefused(t *testing.T) {
	caCert, caKey, _ := ca.Generate("test")
	srv, _ := New(map[string]string{}, caCert, caKey)
	_ = srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{"allowed.example"}}},
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newClient(t, ts.URL, caCert)
	// CONNECT to a non-allowlisted host must fail.
	_, err := client.Get("https://denied.example/")
	if err == nil {
		t.Fatal("expected error reaching denied host, got nil")
	}
}

// End to end: a request to an inject host that carries no placeholder must
// reach the upstream without any credential added by the proxy.
func TestServer_NoPlaceholderGetsNoCredential(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("x-api-key"))
	}))
	defer upstream.Close()
	upHost := upstream.Listener.Addr().String()

	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(map[string]string{"anthropic": "REALKEY"}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{hostOnly(upHost)}}},
		Inject:   map[string]InjectRule{hostOnly(upHost): {Header: "x-api-key", Secret: "anthropic"}},
	}); err != nil {
		t.Fatal(err)
	}
	upPool := x509.NewCertPool()
	upPool.AddCert(upstream.Certificate())
	srv.TrustUpstreamRoots(upPool)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := newClient(t, ts.URL, caCert).Get("https://" + upHost + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "" {
		t.Fatalf("upstream saw x-api-key=%q, want none (no placeholder was sent)", string(body))
	}
}

// mitmConn opens a CONNECT tunnel to target through the proxy at proxyAddr and
// completes TLS against the proxy's minted cert, returning a conn the caller
// can write raw HTTP/1.1 requests to.
func mitmConn(t *testing.T, proxyAddr, target string, caCertPEM []byte) *tls.Conn {
	t.Helper()
	raw, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(raw)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT %s: resp=%v err=%v", target, resp, err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCertPEM)
	c := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: hostOnly(target)})
	if err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// Inside a MITM'd tunnel to an inject host, a request naming a different host
// (absolute-form URL, or a Host header that disagrees with the tunnel) must be
// refused: it must neither reach that host nor carry the injected secret there.
func TestServer_MitmRequestForOtherHostRefused(t *testing.T) {
	hits := make(chan string, 4)
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.Header.Get("x-api-key")
	}))
	defer other.Close()
	inj := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.Host + " " + r.Header.Get("x-api-key")
	}))
	defer inj.Close()
	// Two names for loopback so the inject host and the other host differ:
	// only "localhost" is allowed (and injected); 127.0.0.1 is not.
	injHost := "localhost:" + strings.Split(inj.Listener.Addr().String(), ":")[1]

	caCert, caKey, _ := ca.Generate("test")
	srv, _ := New(map[string]string{"anthropic": "REALKEY"}, caCert, caKey)
	_ = srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{"localhost"}}},
		Inject:   map[string]InjectRule{"localhost": {Header: "x-api-key", Secret: "anthropic"}},
	})
	pool := x509.NewCertPool()
	pool.AddCert(inj.Certificate())
	pool.AddCert(other.Certificate())
	srv.TrustUpstreamRoots(pool)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	otherAddr := other.Listener.Addr().String()

	for _, raw := range []string{
		"GET https://" + otherAddr + "/ HTTP/1.1\r\nHost: " + otherAddr + "\r\nx-api-key: " + Sentinel("anthropic") + "\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: evil.example\r\nx-api-key: " + Sentinel("anthropic") + "\r\n\r\n",
	} {
		c := mitmConn(t, ts.Listener.Addr().String(), injHost, caCert)
		io.WriteString(c, raw)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("request %q: status %d, want 403", strings.SplitN(raw, "\r\n", 3)[:2], resp.StatusCode)
		}
		select {
		case h := <-hits:
			t.Errorf("request %q reached an upstream (saw %q)", strings.SplitN(raw, "\r\n", 3)[:2], h)
		default:
		}
	}
}

// A secret replaced while the proxy runs (e.g. a refreshed token) must be used
// for the very next request, with no restart. Run with -race: requests in
// flight while the secret is swapped must not race.
func TestServer_UpdateSecretsTakesEffectLive(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.Header.Get("x-api-key"))
	}))
	defer upstream.Close()
	upHost := upstream.Listener.Addr().String()

	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(map[string]string{"anthropic": "OLDKEY"}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{hostOnly(upHost)}}},
		Inject:   map[string]InjectRule{hostOnly(upHost): {Header: "x-api-key", Secret: "anthropic"}},
	})
	upPool := x509.NewCertPool()
	upPool.AddCert(upstream.Certificate())
	srv.TrustUpstreamRoots(upPool)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := newClient(t, ts.URL, caCert)

	get := func() string {
		req, _ := http.NewRequest("GET", "https://"+upHost+"/", nil)
		req.Header.Set("x-api-key", Sentinel("anthropic"))
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("request: %v", err)
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(); got != "OLDKEY" {
		t.Fatalf("before update: upstream saw %q, want OLDKEY", got)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			get()
		}
	}()
	srv.UpdateSecrets(map[string]string{"anthropic": "NEWKEY"})
	<-done

	if got := get(); got != "NEWKEY" {
		t.Fatalf("after update: upstream saw %q, want NEWKEY", got)
	}
}
