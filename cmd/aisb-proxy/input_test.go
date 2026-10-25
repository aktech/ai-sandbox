package main

import (
	"strings"
	"testing"
)

func TestReadSecrets(t *testing.T) {
	in := `{"ca_cert":"CERT","ca_key":"KEY","secrets":{"anthropic":"k"}}`
	got, err := readSecrets(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readSecrets: %v", err)
	}
	if got.CACert != "CERT" || got.CAKey != "KEY" || got.Secrets["anthropic"] != "k" {
		t.Fatalf("parsed wrong: %#v", got)
	}
}

func TestReadRules(t *testing.T) {
	in := `{"universal":["u.com"],"projects":[{"cidr":"10.0.0.0/8","allow":["a.com"]}],"inject":{}}`
	rs, err := readRules(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readRules: %v", err)
	}
	if len(rs.Universal) != 1 || rs.Universal[0] != "u.com" {
		t.Fatalf("universal wrong: %#v", rs.Universal)
	}
	if len(rs.Projects) != 1 || rs.Projects[0].CIDR != "10.0.0.0/8" || rs.Projects[0].Allow[0] != "a.com" {
		t.Fatalf("projects wrong: %#v", rs.Projects)
	}
}
