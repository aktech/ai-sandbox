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
