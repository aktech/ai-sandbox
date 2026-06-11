package main

import (
	"strings"
	"testing"
)

func TestReadStdin(t *testing.T) {
	// Format piped by psb: a single JSON object.
	in := `{"ca_cert":"CERT","ca_key":"KEY","secrets":{"anthropic":"k"},"config":{"allow":["github.com"],"inject":{}}}`
	got, err := readInput(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readInput: %v", err)
	}
	if got.CACert != "CERT" || got.CAKey != "KEY" || got.Secrets["anthropic"] != "k" {
		t.Fatalf("parsed wrong: %#v", got)
	}
	if len(got.Config.Allow) != 1 || got.Config.Allow[0] != "github.com" {
		t.Fatalf("config wrong: %#v", got.Config)
	}
}
