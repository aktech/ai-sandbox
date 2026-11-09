package proxy

import (
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aktech/ai-sandbox/internal/ca"
)

// fakeJWT builds an unsigned token shaped like a JWT, as the Codex CLI
// expects (it reads the claims, e.g. exp, from the middle segment).
func fakeJWT(claims, sig string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(claims)) + "." + sig
}

// oauthEnv is a fake OAuth provider behind the proxy. Its token endpoint
// records the refresh token it was sent and answers with reply; /api echoes
// the Authorization header it was sent.
type oauthEnv struct {
	client   *http.Client
	upHost   string
	srv      *Server
	mu       sync.Mutex
	gotRT    []string
	captured []map[string]string
}

func newOAuthEnv(t *testing.T, secrets map[string]string, allow []string, reply func(w http.ResponseWriter, r *http.Request)) *oauthEnv {
	t.Helper()
	env := &oauthEnv{}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			body, _ := io.ReadAll(r.Body)
			rt := ""
			if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				var m map[string]any
				_ = json.Unmarshal(body, &m)
				rt, _ = m["refresh_token"].(string)
			} else {
				r.Body = io.NopCloser(bytes.NewReader(body))
				_ = r.ParseForm()
				rt = r.PostForm.Get("refresh_token")
			}
			env.mu.Lock()
			env.gotRT = append(env.gotRT, rt)
			env.mu.Unlock()
			reply(w, r)
		case "/api":
			io.WriteString(w, r.Header.Get("Authorization"))
		}
	}))
	t.Cleanup(up.Close)
	env.upHost = up.Listener.Addr().String()
	host := hostOnly(env.upHost)

	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(secrets, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	srv.OnRefresh(func(tokens map[string]string) {
		env.mu.Lock()
		env.captured = append(env.captured, tokens)
		env.mu.Unlock()
	})
	if err := srv.UpdateRules(RuleSet{
		Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: append(allow, env.upHost)}},
		Inject:   map[string]InjectRule{host: {Header: "Authorization", Secret: "codex", Format: "Bearer %s"}},
		OAuth:    map[string]OAuthRule{host: {Path: "/oauth/token", Access: "codex", Refresh: "codex_refresh"}},
	}); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	srv.TrustUpstreamRoots(pool)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	env.srv = srv
	env.client = newClient(t, ts.URL, caCert)
	return env
}

func (e *oauthEnv) post(t *testing.T, contentType, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", "https://"+e.upHost+"/oauth/token", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *oauthEnv) apiAuth(t *testing.T, bearer string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+e.upHost+"/api", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("api request: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// Codex refreshes with a JSON body. The proxy must send the real refresh
// token upstream, keep the new real tokens for itself, and hand the agent
// placeholders: JWT-shaped where the original was a JWT (same claims, the
// signature replaced by the placeholder), so the agent can still read exp.
// The next API call with the placeholder then carries the new real token.
// This holds for a project whose allowlist is "*".
func TestOAuth_JSONRefreshKeepsRealTokensInProxy(t *testing.T) {
	newAccess := fakeJWT(`{"exp":4102444800}`, "REALSIG-ACCESS-2")
	newID := fakeJWT(`{"email":"alice@example.com"}`, "REALSIG-ID-2")
	env := newOAuthEnv(t, map[string]string{"codex": "OLD-ACCESS", "codex_refresh": "RT-OLD-REAL"}, []string{"*"},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"id_token": newID, "access_token": newAccess, "refresh_token": "RT-NEW-REAL",
			})
		})

	status, body := env.post(t, "application/json",
		`{"client_id":"app_x","grant_type":"refresh_token","refresh_token":"`+Sentinel("codex_refresh")+`"}`)
	if status != 200 {
		t.Fatalf("status %d body %q", status, body)
	}
	if len(env.gotRT) != 1 || env.gotRT[0] != "RT-OLD-REAL" {
		t.Fatalf("upstream got refresh tokens %q, want the real one", env.gotRT)
	}
	for _, real := range []string{"REALSIG", "RT-NEW-REAL", "RT-OLD-REAL"} {
		if strings.Contains(body, real) {
			t.Fatalf("agent's response carries a real token (%s): %s", real, body)
		}
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response is not JSON: %v: %s", err, body)
	}
	wantAccess := strings.TrimSuffix(newAccess, "REALSIG-ACCESS-2") + Sentinel("codex")
	if got["access_token"] != wantAccess {
		t.Fatalf("access_token = %q, want JWT-shaped placeholder %q", got["access_token"], wantAccess)
	}
	if !strings.Contains(got["refresh_token"], Sentinel("codex_refresh")) {
		t.Fatalf("refresh_token = %q, want the placeholder", got["refresh_token"])
	}
	if p := strings.Split(got["id_token"], "."); len(p) != 3 || p[1] != strings.Split(newID, ".")[1] {
		t.Fatalf("id_token = %q, want the claims kept and the signature replaced", got["id_token"])
	}

	if saw := env.apiAuth(t, got["access_token"]); saw != "Bearer "+newAccess {
		t.Fatalf("API call after refresh carried %q, want the new real token", saw)
	}
	if len(env.captured) != 1 || env.captured[0]["codex"] != newAccess || env.captured[0]["codex_refresh"] != "RT-NEW-REAL" {
		t.Fatalf("OnRefresh got %d captures, want one with the new access and refresh tokens", len(env.captured))
	}
	// The rotated refresh token is the one sent on the next refresh.
	env.post(t, "application/json", `{"grant_type":"refresh_token","refresh_token":"`+got["refresh_token"]+`"}`)
	if env.gotRT[1] != "RT-NEW-REAL" {
		t.Fatalf("second refresh sent %q upstream, want the rotated real token", env.gotRT[1])
	}
}

// A form-encoded refresh with opaque tokens (Claude Code's token shape) gets
// placeholders that keep the token's type prefix, so a client checking for
// "sk-ant-oat" still accepts it. A reply without a new refresh token keeps
// the old one in use.
func TestOAuth_FormRefreshOpaqueTokens(t *testing.T) {
	env := newOAuthEnv(t, map[string]string{"codex": "OLD", "codex_refresh": "sk-ant-ort01-OLDREAL"}, nil,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"sk-ant-oat01-NEWREAL","expires_in":28800,"token_type":"Bearer"}`)
		})
	status, body := env.post(t, "application/x-www-form-urlencoded",
		"grant_type=refresh_token&refresh_token=sk-ant-ort01-"+Sentinel("codex_refresh"))
	if status != 200 || env.gotRT[0] != "sk-ant-ort01-OLDREAL" {
		t.Fatalf("status %d, upstream got %q", status, env.gotRT)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if got["access_token"] != "sk-ant-oat01-"+Sentinel("codex") || got["expires_in"] != float64(28800) {
		t.Fatalf("reply = %s, want the access token as a prefixed placeholder and other fields kept", body)
	}
	if saw := env.apiAuth(t, "sk-ant-oat01-"+Sentinel("codex")); saw != "Bearer sk-ant-oat01-NEWREAL" {
		t.Fatalf("API call carried %q, want the new real token", saw)
	}
	env.post(t, "application/x-www-form-urlencoded", "grant_type=refresh_token&refresh_token="+Sentinel("codex_refresh"))
	if env.gotRT[1] != "sk-ant-ort01-OLDREAL" {
		t.Fatalf("second refresh sent %q, want the unchanged refresh token", env.gotRT[1])
	}
}

// Replies the proxy cannot rewrite safely never reach the agent with a real
// token: an error reply echoing the refresh token is masked, a compressed
// reply is refused, and a refresh over plain HTTP gets no real token at all.
func TestOAuth_RepliesNeverLeakRealTokens(t *testing.T) {
	t.Run("error echo", func(t *testing.T) {
		env := newOAuthEnv(t, map[string]string{"codex_refresh": "RT-REAL"}, nil, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"token RT-REAL was revoked"}`)
		})
		status, body := env.post(t, "application/json", `{"refresh_token":"`+Sentinel("codex_refresh")+`"}`)
		if status != 400 || strings.Contains(body, "RT-REAL") || !strings.Contains(body, "invalid_grant") {
			t.Fatalf("status %d body %q: want the 400 kept with the token masked", status, body)
		}
	})
	t.Run("compressed reply", func(t *testing.T) {
		// An error reply compressed regardless of Accept-Encoding, echoing the
		// refresh token: the proxy must still mask it (decompressing it, or
		// refusing it when it cannot).
		env := newOAuthEnv(t, map[string]string{"codex_refresh": "RT-REAL"}, nil, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusBadRequest)
			gz := gzip.NewWriter(w)
			io.WriteString(gz, `{"error":"invalid_grant","error_description":"RT-REAL"}`)
			gz.Close()
		})
		status, body := env.post(t, "application/json", `{"refresh_token":"`+Sentinel("codex_refresh")+`"}`)
		if strings.Contains(body, "RT-REAL") {
			t.Fatalf("status %d body %q carries the real token", status, body)
		}
	})
	t.Run("plain http", func(t *testing.T) {
		env := newOAuthEnv(t, map[string]string{"codex_refresh": "RT-REAL"}, nil, func(w http.ResponseWriter, r *http.Request) {})
		got := ""
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
		}))
		defer plain.Close()
		ph := plain.Listener.Addr().String()
		_ = env.srv.UpdateRules(RuleSet{
			Projects: []Subnet{{CIDR: "127.0.0.0/8", Allow: []string{ph}}},
			OAuth:    map[string]OAuthRule{hostOnly(ph): {Path: "/oauth/token", Access: "codex", Refresh: "codex_refresh"}},
		})
		resp, err := env.client.Post("http://"+ph+"/oauth/token", "application/json",
			strings.NewReader(`{"refresh_token":"`+Sentinel("codex_refresh")+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if strings.Contains(got, "RT-REAL") {
			t.Fatalf("plain-HTTP refresh carried the real token upstream: %s", got)
		}
	})
}
