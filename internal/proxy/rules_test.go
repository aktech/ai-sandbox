package proxy

import "testing"

func mkRules(t *testing.T) *compiledRules {
	t.Helper()
	rs := RuleSet{
		Universal: []string{"u.com"},
		Inject:    map[string]InjectRule{"inj.com": {Header: "x", Secret: "s"}},
		Projects: []Subnet{
			{CIDR: "10.0.0.0/8", Allow: []string{"a.com"}},
			{CIDR: "172.16.0.0/12", Allow: []string{"b.com"}},
			{CIDR: "192.168.5.0/24", Allow: []string{"*"}}, // unrestricted project
		},
	}
	c, err := compile(rs)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return c
}

func TestAllowlistFor_PerSubnet(t *testing.T) {
	c := mkRules(t)

	// project A (10/8): universal + a.com + inject host, NOT b.com
	a := c.allowlistFor("10.1.2.3:5000")
	for _, h := range []string{"u.com", "a.com", "inj.com"} {
		if !a.Allowed(h) {
			t.Errorf("10/8 should allow %q", h)
		}
	}
	if a.Allowed("b.com") {
		t.Error("10/8 must NOT allow b.com (belongs to another project)")
	}

	// project B (172.16/12): universal + b.com, NOT a.com
	b := c.allowlistFor("172.16.0.9:1")
	if !b.Allowed("b.com") || b.Allowed("a.com") {
		t.Errorf("172.16/12 allow wrong: b=%v a=%v", b.Allowed("b.com"), b.Allowed("a.com"))
	}

	// unrestricted project (192.168.5/24): everything
	u := c.allowlistFor("192.168.5.7:80")
	if !u.Allowed("literally-anything.example") {
		t.Error("192.168.5/24 has * and should allow anything")
	}

	// unknown source subnet: only universal + inject hosts
	o := c.allowlistFor("8.8.8.8:1")
	if !o.Allowed("u.com") || !o.Allowed("inj.com") {
		t.Error("unknown subnet should still get universal + inject hosts")
	}
	if o.Allowed("a.com") || o.Allowed("b.com") {
		t.Error("unknown subnet must not inherit any project's allow")
	}
}

func TestCombineRuleSets_UnionsProjectsKeepsGlobals(t *testing.T) {
	// One file per project (as the proxy reads them from rules.d).
	a := RuleSet{
		Universal: []string{"u.com"},
		Inject:    map[string]InjectRule{"inj.com": {Header: "x", Secret: "s"}},
		Projects:  []Subnet{{CIDR: "10.0.0.0/8", Allow: []string{"a.com"}}},
	}
	b := RuleSet{
		Universal: []string{"u.com"},
		Projects:  []Subnet{{CIDR: "172.16.0.0/12", Allow: []string{"b.com"}}},
	}
	combined := CombineRuleSets([]RuleSet{a, b})

	c, err := compile(combined)
	if err != nil {
		t.Fatal(err)
	}
	if !c.allowlistFor("10.1.1.1:1").Allowed("a.com") {
		t.Error("project A subnet missing from combined set")
	}
	if !c.allowlistFor("172.16.0.1:1").Allowed("b.com") {
		t.Error("project B subnet missing from combined set")
	}
	// Universal and inject survive even though only A supplied inject.
	if !c.allowlistFor("8.8.8.8:1").Allowed("u.com") || !c.allowlistFor("8.8.8.8:1").Allowed("inj.com") {
		t.Error("universal/inject lost in combine")
	}
	// A's subnet must NOT inherit B's allow and vice versa.
	if c.allowlistFor("10.1.1.1:1").Allowed("b.com") {
		t.Error("project A leaked project B's allow")
	}
}

func TestCombineRuleSets_LastCIDRWins(t *testing.T) {
	combined := CombineRuleSets([]RuleSet{
		{Projects: []Subnet{{CIDR: "10.0.0.0/8", Allow: []string{"old.com"}}}},
		{Projects: []Subnet{{CIDR: "10.0.0.0/8", Allow: []string{"new.com"}}}},
	})
	c, _ := compile(combined)
	if c.allowlistFor("10.0.0.1:1").Allowed("old.com") || !c.allowlistFor("10.0.0.1:1").Allowed("new.com") {
		t.Fatal("same CIDR should be replaced by the later file")
	}
}

func TestInjectFor_GlobalByHost(t *testing.T) {
	c := mkRules(t)
	if _, ok := c.injectRules()["inj.com"]; !ok {
		t.Fatal("inject rule for inj.com missing")
	}
}
