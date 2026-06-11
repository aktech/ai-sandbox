package ca

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestGenerateCA_ParsesAndIsCA(t *testing.T) {
	certPEM, keyPEM, err := Generate("psb proxy CA")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	block := mustDecode(t, certPEM)
	cert, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("generated cert is not a CA")
	}
	// The key pair must load as a usable TLS cert (used to sign leaves).
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
}

func mustDecode(t *testing.T, pemBytes []byte) []byte {
	t.Helper()
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		t.Fatal("no PEM block")
	}
	return b.Bytes
}
