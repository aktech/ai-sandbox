package proxy

import (
	"fmt"
	"net"
)

// Subnet ties one sandbox network's CIDR to the extra hosts that project may
// reach. Allow may contain "*" to lift all limits for that project.
type Subnet struct {
	CIDR  string   `json:"cidr"`
	Allow []string `json:"allow"`
}

// RuleSet is the full routing table the proxy enforces. Universal applies to
// every project; Projects add per-subnet allowances; Inject is keyed by host
// and is global (a host is injected the same way regardless of caller). The
// rule set carries no secret values, only the secret names the injector looks
// up in the separately-delivered secret map, so it is safe to (re)deliver on
// every psb without unlocking the store.
type RuleSet struct {
	Universal []string              `json:"universal"`
	Projects  []Subnet              `json:"projects"`
	Inject    map[string]InjectRule `json:"inject"`
}

// CombineRuleSets unions several per-project rule sets (one per sandbox file)
// into the single rule set the proxy enforces. Project subnets accumulate
// (deduped by CIDR, last write wins); Universal and Inject are taken from any
// set that supplies them (they are identical across projects, derived from the
// shared default config).
func CombineRuleSets(sets []RuleSet) RuleSet {
	out := RuleSet{Inject: map[string]InjectRule{}}
	byCIDR := map[string]Subnet{}
	var order []string
	for _, rs := range sets {
		if len(rs.Universal) > 0 {
			out.Universal = rs.Universal
		}
		for h, r := range rs.Inject {
			out.Inject[h] = r
		}
		for _, s := range rs.Projects {
			if _, seen := byCIDR[s.CIDR]; !seen {
				order = append(order, s.CIDR)
			}
			byCIDR[s.CIDR] = s
		}
	}
	for _, cidr := range order {
		out.Projects = append(out.Projects, byCIDR[cidr])
	}
	return out
}

type compiledSubnet struct {
	net   *net.IPNet
	allow []string
}

type compiledRules struct {
	universal   []string
	injectHosts []string
	inject      map[string]InjectRule
	subnets     []compiledSubnet
}

func compile(rs RuleSet) (*compiledRules, error) {
	c := &compiledRules{
		universal: rs.Universal,
		inject:    rs.Inject,
	}
	for h := range rs.Inject {
		c.injectHosts = append(c.injectHosts, h)
	}
	for _, s := range rs.Projects {
		_, ipnet, err := net.ParseCIDR(s.CIDR)
		if err != nil {
			return nil, fmt.Errorf("bad subnet CIDR %q: %w", s.CIDR, err)
		}
		c.subnets = append(c.subnets, compiledSubnet{net: ipnet, allow: s.Allow})
	}
	return c, nil
}

// allowlistFor builds the effective allowlist for a request coming from srcAddr
// ("ip" or "ip:port"): universal hosts + every inject host (inject implies
// allow) + the allow list of whichever project subnet contains the source IP.
func (c *compiledRules) allowlistFor(srcAddr string) *Allowlist {
	entries := make([]string, 0, len(c.universal)+len(c.injectHosts)+4)
	entries = append(entries, c.universal...)
	entries = append(entries, c.injectHosts...)
	if ip := parseHostIP(srcAddr); ip != nil {
		for _, s := range c.subnets {
			if s.net.Contains(ip) {
				entries = append(entries, s.allow...)
			}
		}
	}
	return NewAllowlist(entries)
}

// injectRules returns the global host->rule map.
func (c *compiledRules) injectRules() map[string]InjectRule { return c.inject }

// parseHostIP extracts the IP from "ip" or "ip:port".
func parseHostIP(addr string) net.IP {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(addr)
}
