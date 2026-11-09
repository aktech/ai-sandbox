package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// After start-up the proxy keeps watching for a new secrets payload, so
// `aisb secret set` can hand it a refreshed value without a restart. A payload
// that does not parse is dropped (and logged) without stopping the watcher or
// replacing the secrets already in use.
func TestWatchSecrets_AppliesEachNewPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.json")
	got := make(chan map[string]string, 4)
	stop := make(chan struct{})
	defer close(stop)
	go watchSecrets(path, 5*time.Millisecond, stop, func(s map[string]string) { got <- s })

	next := func() map[string]string {
		t.Helper()
		select {
		case s := <-got:
			return s
		case <-time.After(2 * time.Second):
			t.Fatal("watcher did not pick up the payload")
			return nil
		}
	}
	waitGone := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("payload file was not removed after it was read")
	}

	if err := os.WriteFile(path, []byte(`{"secrets":{"openai":"v1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := next(); s["openai"] != "v1" {
		t.Fatalf("first payload: got %v", s)
	}
	waitGone()

	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	waitGone()

	if err := os.WriteFile(path, []byte(`{"secrets":{"openai":"v2"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := next(); s["openai"] != "v2" {
		t.Fatalf("second payload: got %v (a bad payload must not be applied)", s)
	}
}
