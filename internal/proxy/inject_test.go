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
	req.Header.Set("x-api-key", "__psb_anthropic__")
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
	req.Header.Set("Authorization", "Bearer __psb_github__")
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
	sentinelCreds := base64.StdEncoding.EncodeToString([]byte("x-access-token:__psb_github__"))
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
	req.Header.Set("x-api-key", "__psb_anthropic__")
	inj.Apply(req)
	if got := req.Header.Get("x-api-key"); got != "__psb_anthropic__" {
		t.Fatalf("header changed for unconfigured host: %q", got)
	}
}
