package proxy

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func TestInject_PlainHeader(t *testing.T) {
	rules := map[string]InjectRule{
		"api.anthropic.com": {Header: "x-api-key", Secret: "anthropic"},
	}
	inj := NewInjector(rules, map[string]string{"anthropic": "real-key"})

	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
	req.Header.Set("x-api-key", "__aisb_anthropic__")
	inj.Apply(req)

	if got := req.Header.Get("x-api-key"); got != "real-key" {
		t.Fatalf("x-api-key = %q, want real-key", got)
	}
}

func TestInject_BearerFormat(t *testing.T) {
	rules := map[string]InjectRule{
		"api.github.com": {Header: "Authorization", Secret: "github", Format: "Bearer %s"},
	}
	inj := NewInjector(rules, map[string]string{"github": "ghp_xxx"})

	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer __aisb_github__")
	inj.Apply(req)

	if got := req.Header.Get("Authorization"); got != "Bearer ghp_xxx" {
		t.Fatalf("Authorization = %q, want Bearer ghp_xxx", got)
	}
}

func TestInject_BasicAuthGitPush(t *testing.T) {
	// git over HTTPS sends Authorization: Basic base64(user:sentinel).
	// The injector must decode, replace the password sentinel, re-encode.
	rules := map[string]InjectRule{
		"github.com": {Header: "Authorization", Secret: "github", Basic: true},
	}
	inj := NewInjector(rules, map[string]string{"github": "ghp_real"})

	req, _ := http.NewRequest("POST", "https://github.com/u/r.git/git-receive-pack", nil)
	sentinelCreds := base64.StdEncoding.EncodeToString([]byte("x-access-token:__aisb_github__"))
	req.Header.Set("Authorization", "Basic "+sentinelCreds)
	inj.Apply(req)

	wantCreds := base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_real"))
	if got := req.Header.Get("Authorization"); got != "Basic "+wantCreds {
		t.Fatalf("Authorization = %q, want Basic %s", got, wantCreds)
	}
}

func TestInject_NoRuleForHost_Untouched(t *testing.T) {
	inj := NewInjector(map[string]InjectRule{}, map[string]string{})
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	req.Header.Set("x-api-key", "__aisb_anthropic__")
	inj.Apply(req)
	if got := req.Header.Get("x-api-key"); got != "__aisb_anthropic__" {
		t.Fatalf("header changed for unconfigured host: %q", got)
	}
}

// A request that carries no placeholder must leave the proxy exactly as it
// arrived: the proxy only swaps a placeholder, it never adds a credential.
func TestInject_NoPlaceholder_NoCredentialAdded(t *testing.T) {
	rules := map[string]InjectRule{
		"api.anthropic.com": {Header: "x-api-key", Secret: "anthropic"},
		"api.github.com":    {Header: "Authorization", Secret: "github", Format: "Bearer %s"},
	}
	inj := NewInjector(rules, map[string]string{"anthropic": "real-key", "github": "ghp_real"})

	cases := []struct {
		url, header, value string
	}{
		{"https://api.anthropic.com/v1/messages", "x-api-key", ""},           // header absent
		{"https://api.anthropic.com/v1/messages", "x-api-key", "users-own"},  // agent's own value
		{"https://api.github.com/user", "Authorization", ""},                 // header absent
		{"https://api.github.com/user", "Authorization", "Bearer users-own"}, // agent's own token
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", c.url, nil)
		if c.value != "" {
			req.Header.Set(c.header, c.value)
		}
		inj.Apply(req)
		if got := req.Header.Get(c.header); got != c.value {
			t.Errorf("%s with %s=%q: proxy rewrote it to %q, want it untouched",
				c.url, c.header, c.value, got)
		}
	}
}

// A placeholder for one secret must not unlock a different secret: the rule
// for the host names which placeholder it swaps.
func TestInject_OtherSecretsPlaceholder_Untouched(t *testing.T) {
	rules := map[string]InjectRule{
		"api.anthropic.com": {Header: "x-api-key", Secret: "anthropic"},
	}
	inj := NewInjector(rules, map[string]string{"anthropic": "real-key", "github": "ghp_real"})
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/messages", nil)
	req.Header.Set("x-api-key", Sentinel("github"))
	inj.Apply(req)
	if got := req.Header.Get("x-api-key"); got != Sentinel("github") {
		t.Fatalf("x-api-key = %q, want the github placeholder left as is", got)
	}
}

// Some clients refuse a token that is not shaped like the real thing (a JWT,
// say), so the placeholder may sit inside a larger fake value. Any header that
// carries the placeholder is replaced whole by the formatted real secret.
func TestInject_PlaceholderInsideFakeToken_ReplacedWhole(t *testing.T) {
	rules := map[string]InjectRule{
		"chatgpt.com": {Header: "Authorization", Secret: "codex", Format: "Bearer %s"},
	}
	inj := NewInjector(rules, map[string]string{"codex": "real.jwt.value"})
	req, _ := http.NewRequest("GET", "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOi.eyJleHAiOjF9."+Sentinel("codex"))
	inj.Apply(req)
	if got := req.Header.Get("Authorization"); got != "Bearer real.jwt.value" {
		t.Fatalf("Authorization = %q, want Bearer real.jwt.value", got)
	}
}
