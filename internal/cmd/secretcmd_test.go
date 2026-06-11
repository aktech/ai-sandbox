package cmd

import (
	"encoding/json"
	"testing"

	"github.com/aktech/ai-sandbox/internal/cfg"
)

func TestAssemblePayload(t *testing.T) {
	c := cfg.Effective{Proxy: &cfg.ProxyBlock{
		Allow:  []string{"api.anthropic.com"},
		Inject: rawInject(`{"api.anthropic.com":{"header":"x-api-key","secret":"anthropic"}}`),
	}}
	secrets := map[string]string{"anthropic": "real"}
	payload, err := assemblePayload(c, secrets, "CERTPEM", "KEYPEM")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CACert  string            `json:"ca_cert"`
		CAKey   string            `json:"ca_key"`
		Secrets map[string]string `json:"secrets"`
		Config  struct {
			Allow  []string                   `json:"allow"`
			Inject map[string]json.RawMessage `json:"inject"`
		} `json:"config"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.CACert != "CERTPEM" || got.CAKey != "KEYPEM" || got.Secrets["anthropic"] != "real" {
		t.Fatalf("payload wrong: %s", payload)
	}
	if len(got.Config.Allow) == 0 {
		t.Fatalf("config.allow missing: %s", payload)
	}
	if _, ok := got.Config.Inject["api.anthropic.com"]; !ok {
		t.Fatalf("config.inject missing the rule: %s", payload)
	}
}

// A secret referenced only via inject (not in Allow) must still be reachable:
// assemblePayload must emit it in config.allow (inject implies allow).
func TestAssemblePayload_InjectImpliesAllow(t *testing.T) {
	c := cfg.Effective{Proxy: &cfg.ProxyBlock{
		Inject: rawInject(`{"api.github.com":{"header":"Authorization","secret":"github","format":"Bearer %s"}}`),
	}}
	payload, err := assemblePayload(c, map[string]string{"github": "ghp"}, "C", "K")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Config struct {
			Allow []string `json:"allow"`
		} `json:"config"`
	}
	_ = json.Unmarshal(payload, &got)
	found := false
	for _, h := range got.Config.Allow {
		if h == "api.github.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("api.github.com must be in allow (inject implies allow): %s", payload)
	}
}
