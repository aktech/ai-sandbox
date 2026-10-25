package secret

import (
	"path/filepath"
	"testing"
)

func TestStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc")
	pw := []byte("correct horse battery staple")

	if err := Save(path, pw, map[string]string{"anthropic": "sk-ant-xyz"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path, pw)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["anthropic"] != "sk-ant-xyz" {
		t.Fatalf("got %q, want sk-ant-xyz", got["anthropic"])
	}
}

func TestStore_WrongPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.enc")
	if err := Save(path, []byte("right"), map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, []byte("wrong")); err == nil {
		t.Fatal("expected error with wrong password, got nil")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.enc"), []byte("x"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
