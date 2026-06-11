package cmd

import (
	"encoding/json"
	"testing"
)

func TestSecretsPayload(t *testing.T) {
	payload, err := secretsPayload(map[string]string{"anthropic": "real"}, "CERTPEM", "KEYPEM")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CACert  string            `json:"ca_cert"`
		CAKey   string            `json:"ca_key"`
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.CACert != "CERTPEM" || got.CAKey != "KEYPEM" || got.Secrets["anthropic"] != "real" {
		t.Fatalf("payload wrong: %s", payload)
	}
	// The secrets payload must NOT carry allow/inject config (that goes in the
	// separate, secret-free rules payload).
	if json.Valid(payload) && string(payload) != "" {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(payload, &raw)
		if _, ok := raw["config"]; ok {
			t.Fatalf("secrets payload must not contain config: %s", payload)
		}
	}
}
