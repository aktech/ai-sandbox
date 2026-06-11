package main

import (
	"encoding/json"
	"io"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// secretsInput is delivered once at proxy start (it carries the CA private key
// and the real secret values, so producing it requires unlocking the store).
type secretsInput struct {
	CACert  string            `json:"ca_cert"`
	CAKey   string            `json:"ca_key"`
	Secrets map[string]string `json:"secrets"`
}

func readSecrets(r io.Reader) (*secretsInput, error) {
	var in secretsInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return nil, err
	}
	return &in, nil
}

// readRules decodes a RuleSet (delivered on every psb; carries no secret
// values, only host/secret-name rules, so it needs no store unlock).
func readRules(r io.Reader) (proxy.RuleSet, error) {
	var rs proxy.RuleSet
	if err := json.NewDecoder(r).Decode(&rs); err != nil {
		return rs, err
	}
	return rs, nil
}
