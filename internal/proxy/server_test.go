package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	cfg := &Config{
		Allow:  []string{upHost},
		Inject: map[string]InjectRule{hostOnly(upHost): {Header: "x-api-key", Secret: "anthropic"}},
	}
	srv, err := New(cfg, map[string]string{"anthropic": "REALKEY"}, caCert, caKey)
	if err != nil {
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
	cfg := &Config{Allow: []string{hostOnly(upHost)}} // allowed (bare host), no inject rule
	srv, err := New(cfg, map[string]string{}, caCert, caKey)
	if err != nil {
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
	cfg := &Config{Allow: []string{"allowed.example"}}
	srv, _ := New(cfg, map[string]string{}, caCert, caKey)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := newClient(t, ts.URL, caCert)
	// CONNECT to a non-allowlisted host must fail.
	_, err := client.Get("https://denied.example/")
	if err == nil {
		t.Fatal("expected error reaching denied host, got nil")
	}
}
