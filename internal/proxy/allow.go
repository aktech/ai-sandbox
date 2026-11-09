// Package proxy implements the credential-injecting egress proxy that aisb
// sandboxes route through. The allowlist is default-deny: only hosts that
// match an entry may be reached. A "*." prefix matches any subdomain depth;
// a plain entry matches that exact host only.
//
// Entries are port-aware. "host" reaches the web ports 80 and 443 only,
// "host:port" reaches that one port, and "host:*" reaches any port. IPv6
// literals are written bare ("2001:db8::1") or bracketed with a port
// ("[2001:db8::1]:8443"). A lone "*" allows any host on any port.
package proxy

import (
	"net"
	"strings"
)

type Allowlist struct {
	all     bool // a "*" entry: allow any host
	entries []allowEntry
}

type allowEntry struct {
	host   string // exact host, or ".suffix" for a "*." entry
	suffix bool
	port   string // "" = web ports (80, 443), "*" = any, else one port
}

func NewAllowlist(entries []string) *Allowlist {
	a := &Allowlist{}
	for _, e := range entries {
		if e == "*" {
			a.all = true
			continue
		}
		host, port := splitHostPort(e)
		ent := allowEntry{host: strings.ToLower(host), port: port}
		if rest, ok := strings.CutPrefix(ent.host, "*."); ok {
			ent.host, ent.suffix = "."+rest, true
		}
		a.entries = append(a.entries, ent)
	}
	return a
}

// Allowed reports whether host (optionally "host:port") may be reached. A
// host without a port is taken to be on a web port.
func (a *Allowlist) Allowed(hostport string) bool {
	if a.all {
		return true
	}
	return a.Explicit(hostport)
}

// Explicit reports whether hostport matches a named entry, ignoring "*".
func (a *Allowlist) Explicit(hostport string) bool {
	host, port := splitHostPort(hostport)
	host = strings.ToLower(host)
	for _, e := range a.entries {
		if !e.portOK(port) {
			continue
		}
		if e.suffix && strings.HasSuffix(host, e.host) || !e.suffix && host == e.host {
			return true
		}
	}
	return false
}

func (e allowEntry) portOK(port string) bool {
	switch e.port {
	case "*":
		return true
	case "":
		return port == "" || port == "80" || port == "443"
	default:
		return port == e.port
	}
}

// splitHostPort splits "host:port", "[v6]:port", "host" or a bare IPv6
// literal into host and port ("" when absent).
func splitHostPort(s string) (host, port string) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), ""
}

// hostOnly strips an optional port, IPv6 brackets included.
func hostOnly(s string) string {
	h, _ := splitHostPort(s)
	return h
}
