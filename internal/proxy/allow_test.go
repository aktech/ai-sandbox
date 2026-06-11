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
