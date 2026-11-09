package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
)

// PendingSum identifies the content of the proxy's pending-token file. The
// host sends back the sum of what it read and saved; the proxy deletes the
// file only if it still holds that content.
func PendingSum(b []byte) string {
	sum := sha256.Sum256(bytes.TrimSpace(b))
	return hex.EncodeToString(sum[:])
}
