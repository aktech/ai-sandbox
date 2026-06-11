// Package proxy implements the credential-injecting egress proxy that psb
// sandboxes route through. The allowlist is default-deny: only hosts that
// match an entry may be reached. A "*." prefix matches any subdomain depth;
// a plain entry matches that exact host only.
package proxy

import "strings"

type Allowlist struct {
	exact    map[string]bool
	suffixes []string // for "*.example.com" -> ".example.com"
}

func NewAllowlist(entries []string) *Allowlist {
	a := &Allowlist{exact: map[string]bool{}}
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e, "*."); ok {
			a.suffixes = append(a.suffixes, "."+rest)
		} else {
			a.exact[e] = true
		}
	}
	return a
}

// Allowed reports whether host (optionally "host:port") may be reached.
func (a *Allowlist) Allowed(host string) bool {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if a.exact[host] {
		return true
	}
	for _, suf := range a.suffixes {
		if strings.HasSuffix(host, suf) {
			return true
		}
	}
	return false
}
