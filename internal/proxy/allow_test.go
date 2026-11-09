package proxy

import "testing"

func TestAllowlist(t *testing.T) {
	a := NewAllowlist([]string{"api.anthropic.com", "*.githubusercontent.com", "github.com"})
	cases := []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true},
		{"api.anthropic.com:443", true}, // port stripped
		{"evil.com", false},
		{"raw.githubusercontent.com", true}, // wildcard one level
		{"a.b.githubusercontent.com", true}, // wildcard multi level
		{"githubusercontent.com", false},    // bare apex not matched by *. prefix
		{"github.com", true},
		{"notgithub.com", false},
		{"sub.github.com", false}, // exact entry, no implicit subdomain
	}
	for _, c := range cases {
		if got := a.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// A "*" entry means allow any host (a project that opts out of all limits).
func TestAllowlist_StarMatchesEverything(t *testing.T) {
	a := NewAllowlist([]string{"*"})
	for _, h := range []string{"example.com", "api.github.com:443", "anything.at.all", "1.2.3.4:9000"} {
		if !a.Allowed(h) {
			t.Errorf("with [*], Allowed(%q) = false, want true", h)
		}
	}
}

// "*" mixed with specific entries still allows everything.
func TestAllowlist_StarAmongOthers(t *testing.T) {
	a := NewAllowlist([]string{"github.com", "*"})
	if !a.Allowed("totally-random.example") {
		t.Fatal("star anywhere in the list should allow everything")
	}
}

// An entry without a port reaches only the web ports (80, 443); "host:port"
// reaches that port only; "host:*" reaches any port. A bare host (no port, as
// in a plain-HTTP request's URL) counts as a web port.
func TestAllowlist_PortAware(t *testing.T) {
	a := NewAllowlist([]string{"api.example.com", "db.example.com:5432", "*.cdn.example:8443", "any.example:*"})
	cases := []struct {
		host string
		want bool
	}{
		{"api.example.com:443", true},
		{"api.example.com:80", true},
		{"api.example.com", true},
		{"api.example.com:22", false},
		{"db.example.com:5432", true},
		{"db.example.com:443", false},
		{"x.cdn.example:8443", true},
		{"x.cdn.example:443", false},
		{"any.example:2222", true},
	}
	for _, c := range cases {
		if got := a.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// IPv6 literals carry colons of their own: "[2001:db8::1]:443" is host
// 2001:db8::1, port 443, and a bare "2001:db8::1" is a host with no port.
func TestAllowlist_IPv6(t *testing.T) {
	a := NewAllowlist([]string{"2001:db8::1", "[2001:db8::2]:8080"})
	cases := []struct {
		host string
		want bool
	}{
		{"[2001:db8::1]:443", true},
		{"2001:db8::1", true},
		{"[2001:db8::1]:22", false},
		{"[2001:db8::2]:8080", true},
		{"[2001:db8::3]:443", false},
	}
	for _, c := range cases {
		if got := a.Allowed(c.host); got != c.want {
			t.Errorf("Allowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}
