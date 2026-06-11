package main

import (
	"encoding/json"
	"io"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// input is the single JSON object psb pipes to the proxy over stdin. Keeping
// secrets + CA key off env/argv/disk means a sandbox compromise cannot reach
// them even by reading the proxy container's /proc.
type input struct {
	CACert  string
	CAKey   string
	Secrets map[string]string
	Config  proxy.Config
}

// wire mirrors the JSON shape. Config is held raw so proxy.ParseConfig can map
// the lowercase inject-rule fields (header/secret/format/basic) correctly.
type wire struct {
	CACert  string            `json:"ca_cert"`
	CAKey   string            `json:"ca_key"`
	Secrets map[string]string `json:"secrets"`
	Config  json.RawMessage   `json:"config"`
}

func readInput(r io.Reader) (*input, error) {
	var w wire
	if err := json.NewDecoder(r).Decode(&w); err != nil {
		return nil, err
	}
	cfg, err := proxy.ParseConfig(w.Config)
	if err != nil {
		return nil, err
	}
	return &input{CACert: w.CACert, CAKey: w.CAKey, Secrets: w.Secrets, Config: *cfg}, nil
}
