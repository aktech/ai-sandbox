package proxy

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// InjectRule describes how to put a real secret into one host's requests.
// Header is the header to write. Secret is the key into the secret map.
// Format wraps the value (e.g. "Bearer %s"); empty means the raw value.
// Basic means the header carries `Basic base64(user:sentinel)` (git over
// HTTPS) and only the password half is swapped.
type InjectRule struct {
	Header string
	Secret string
	Format string
	Basic  bool
}

// Sentinel returns the placeholder value the sandbox uses for a secret name.
func Sentinel(name string) string { return "__aisb_" + name + "__" }

type Injector struct {
	rules   map[string]InjectRule // keyed by host
	secrets map[string]string
}

func NewInjector(rules map[string]InjectRule, secrets map[string]string) *Injector {
	return &Injector{rules: rules, secrets: secrets}
}

// Apply swaps the placeholder in req's auth header for the real secret, in
// place, when a rule matches its host and the header carries the placeholder.
// Only HTTPS requests are touched: a real secret never goes out unencrypted.
func (i *Injector) Apply(req *http.Request) {
	if req.URL.Scheme != "https" {
		return
	}
	host := req.URL.Hostname()
	rule, ok := i.rules[host]
	if !ok {
		return
	}
	real, ok := i.secrets[rule.Secret]
	if !ok {
		return
	}
	sentinel := Sentinel(rule.Secret)

	if rule.Basic {
		val := req.Header.Get(rule.Header)
		raw, ok := strings.CutPrefix(val, "Basic ")
		if !ok {
			return
		}
		dec, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return
		}
		swapped := strings.Replace(string(dec), sentinel, real, 1)
		req.Header.Set(rule.Header,
			"Basic "+base64.StdEncoding.EncodeToString([]byte(swapped)))
		return
	}

	// Only a header that carries this secret's placeholder is rewritten, and
	// then replaced whole (the placeholder may sit inside a fake token shaped
	// like the real one). A request without it (no header, or the agent's own
	// value) passes through untouched: the proxy never adds a credential
	// nobody asked for.
	if !strings.Contains(req.Header.Get(rule.Header), sentinel) {
		return
	}
	value := real
	if rule.Format != "" {
		value = fmt.Sprintf(rule.Format, real)
	}
	req.Header.Set(rule.Header, value)
}
