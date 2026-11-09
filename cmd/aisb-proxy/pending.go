package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/aktech/ai-sandbox/internal/proxy"
)

// The pending file holds real tokens the proxy obtained by completing OAuth
// refreshes, until a host-side aisb command that unlocks the secret store
// pulls them in (`--read-pending`) and acknowledges what it saved
// (`--ack-pending <sum>`). It lives on the proxy's tmpfs, owner-only.

var pendingMu sync.Mutex

// addPending merges tokens into the pending file (latest value per name).
func addPending(path string, tokens map[string]string) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	cur := map[string]string{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &cur); err != nil {
			cur = map[string]string{}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	for k, v := range tokens {
		cur[k] = v
	}
	out, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, bytes.NewReader(out))
}

// readPending copies the pending file to w; nothing when there is none.
func readPending(path string, w io.Writer) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ackPending removes the pending file if it still holds the content whose
// sum the host read; newer content is kept for the next pull.
func ackPending(path, sum string) error {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if proxy.PendingSum(b) != sum {
		return nil
	}
	return os.Remove(path)
}
