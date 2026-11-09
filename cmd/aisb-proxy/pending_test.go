package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// Tokens the proxy refreshed accumulate in the pending file (latest value per
// name, owner-only), until the host acknowledges exactly what it read. A
// newer capture arriving in between keeps the file, so it is not lost.
func TestPending_AccumulatesUntilAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	if err := addPending(path, map[string]string{"codex": "a1", "codex_refresh": "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := addPending(path, map[string]string{"codex": "a2"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := readPending(path, &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["codex"] != "a2" || got["codex_refresh"] != "r1" {
		t.Fatalf("pending = %v (%v), want latest values of both", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending file mode %v, want 0600", fi.Mode().Perm())
	}
	read := proxy.PendingSum(out.Bytes())

	_ = addPending(path, map[string]string{"codex": "a3"})
	if err := ackPending(path, read); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("an ack for older content removed newer tokens")
	}

	out.Reset()
	_ = readPending(path, &out)
	if err := ackPending(path, proxy.PendingSum(out.Bytes())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("pending file still there after acknowledging its content")
	}
	out.Reset()
	if err := readPending(path, &out); err != nil || out.Len() != 0 {
		t.Fatalf("reading with nothing pending: %q, %v; want empty, no error", out.String(), err)
	}
}
