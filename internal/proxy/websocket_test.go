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
	"strings"
	"testing"

	"github.com/aktech/ai-sandbox/internal/ca"
)

// wsEcho is an upstream that accepts a WebSocket upgrade, reports the
// Authorization header it received in an X-Seen-Auth response header, then
// echoes raw bytes back. Frames are not parsed: the proxy must pass the
// upgraded stream through byte for byte, which is all this checks.
func wsEcho(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "not a websocket upgrade", http.StatusBadRequest)
		return
	}
	conn, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\nX-Seen-Auth: %s\r\n\r\n", r.Header.Get("Authorization"))
	brw.Flush()
	io.Copy(conn, brw)
}

// upgrade writes a WebSocket upgrade request (target, Host: host) to conn, checks for a
// 101, then round-trips a message over the upgraded stream. It returns the
// Authorization value the upstream saw.
func upgrade(t *testing.T, conn net.Conn, target, host, auth string) string {
	t.Helper()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nAuthorization: %s\r\n\r\n",
		target, host, auth)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upgrade status %d (%s), want 101", resp.StatusCode, b)
	}
	io.WriteString(conn, "hello-ws")
	buf := make([]byte, len("hello-ws"))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read echo over upgraded stream: %v", err)
	}
	if string(buf) != "hello-ws" {
		t.Fatalf("echo = %q, want hello-ws", buf)
	}
	return resp.Header.Get("X-Seen-Auth")
}

func wsProxy(t *testing.T, rs RuleSet, upstreams ...*httptest.Server) (*httptest.Server, []byte) {
	t.Helper()
	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(map[string]string{"openai": "REALTOKEN"}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.UpdateRules(rs); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	for _, u := range upstreams {
		if u.Certificate() != nil {
			pool.AddCert(u.Certificate())
		}
	}
	srv.TrustUpstreamRoots(pool)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, caCert
}

// wss:// to an inject host: the proxy MITMs the tunnel, swaps the placeholder
// on the upgrade request, and then relays the upgraded stream both ways.
func TestServer_WebSocketThroughMitmInjectsOnUpgrade(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(wsEcho))
	defer up.Close()
	upHost := up.Listener.Addr().String()
	ts, caCert := wsProxy(t, RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{upHost}}},
		Inject:   map[string]InjectRule{hostOnly(upHost): {Header: "Authorization", Secret: "openai", Format: "Bearer %s"}},
	}, up)

	c := mitmConn(t, ts.Listener.Addr().String(), upHost, caCert)
	if got := upgrade(t, c, "/backend-api/ws", upHost, "Bearer "+Sentinel("openai")); got != "Bearer REALTOKEN" {
		t.Fatalf("upstream saw Authorization %q, want Bearer REALTOKEN", got)
	}
}

// wss:// to an allowed host with no inject rule: plain tunnel, untouched.
func TestServer_WebSocketThroughTunnel(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(wsEcho))
	defer up.Close()
	upHost := up.Listener.Addr().String()
	ts, _ := wsProxy(t, RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{upHost}}},
	}, up)

	raw, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	fmt.Fprintf(raw, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upHost, upHost)
	br := bufio.NewReader(raw)
	if resp, err := http.ReadResponse(br, nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	c := tlsClient(raw, pool, hostOnly(upHost))
	if got := upgrade(t, c, "/ws", upHost, "Bearer users-own"); got != "Bearer users-own" {
		t.Fatalf("upstream saw Authorization %q, want it untouched", got)
	}
}

// ws:// (plain HTTP, no CONNECT) to an inject host: same swap, same relay.
func TestServer_PlainWebSocketInjectsOnUpgrade(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(wsEcho))
	defer up.Close()
	upHost := up.Listener.Addr().String()
	ts, _ := wsProxy(t, RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{upHost}}},
		Inject:   map[string]InjectRule{hostOnly(upHost): {Header: "Authorization", Secret: "openai", Format: "Bearer %s"}},
	})

	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := upgrade(t, c, "http://"+upHost+"/ws", upHost, "Bearer "+Sentinel("openai")); got != "Bearer REALTOKEN" {
		t.Fatalf("upstream saw Authorization %q, want Bearer REALTOKEN", got)
	}
}

func tlsClient(raw net.Conn, roots *x509.CertPool, name string) net.Conn {
	return tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: name})
}
