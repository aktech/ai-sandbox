package proxy

import (
	"reflect"
	"testing"
)

func TestParseConfig(t *testing.T) {
	raw := []byte(`{
	  "allow": ["api.anthropic.com", "github.com"],
	  "inject": {
	    "api.anthropic.com": {"header": "x-api-key", "secret": "anthropic"},
	    "api.github.com":    {"header": "Authorization", "secret": "github", "format": "Bearer %s"},
	    "github.com":        {"header": "Authorization", "secret": "github", "basic": true}
	  }
	}`)

	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if !reflect.DeepEqual(c.Allow, []string{"api.anthropic.com", "github.com"}) {
		t.Fatalf("Allow = %#v", c.Allow)
	}
	if c.Inject["api.github.com"].Format != "Bearer %s" {
		t.Fatalf("github inject format wrong: %#v", c.Inject["api.github.com"])
	}
	if !c.Inject["github.com"].Basic {
		t.Fatalf("github.com should be basic")
	}
}

// Hosts that appear in inject but not allow must be auto-allowed: you can't
// inject into a host you can't reach.
func TestConfig_InjectImpliesAllow(t *testing.T) {
	c, err := ParseConfig([]byte(`{
	  "allow": ["github.com"],
	  "inject": {"api.anthropic.com": {"header": "x-api-key", "secret": "anthropic"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !NewAllowlist(c.EffectiveAllow()).Allowed("api.anthropic.com") {
		t.Fatal("inject host api.anthropic.com must be implicitly allowed")
	}
}
