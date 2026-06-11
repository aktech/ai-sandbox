package proxy

import (
	"testing"

	"github.com/aktech/ai-sandbox/internal/ca"
)

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

func TestUpdateRules_MergesSubnetsByCIDR(t *testing.T) {
	caCert, caKey, _ := ca.Generate("test")
	srv, err := New(map[string]string{}, caCert, caKey)
	if err != nil {
		t.Fatal(err)
	}
	// Project A delivers its own subnet.
	if err := srv.UpdateRules(RuleSet{
		Universal: []string{"u.com"},
		Projects:  []Subnet{{CIDR: "10.0.0.0/8", Allow: []string{"a.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	// Project B delivers only its subnet; A's must survive.
	if err := srv.UpdateRules(RuleSet{
		Universal: []string{"u.com"},
		Projects:  []Subnet{{CIDR: "172.16.0.0/12", Allow: []string{"b.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	c := srv.rules.Load()
	if !c.allowlistFor("10.1.1.1:1").Allowed("a.com") {
		t.Error("project A subnet must survive project B's update")
	}
	if !c.allowlistFor("172.16.0.1:1").Allowed("b.com") {
		t.Error("project B subnet must be present")
	}
	// Re-delivering A's CIDR with a new allow replaces it.
	if err := srv.UpdateRules(RuleSet{
		Universal: []string{"u.com"},
		Projects:  []Subnet{{CIDR: "10.0.0.0/8", Allow: []string{"a2.com"}}},
	}); err != nil {
		t.Fatal(err)
	}
	c = srv.rules.Load()
	if c.allowlistFor("10.1.1.1:1").Allowed("a.com") {
		t.Error("old a.com should be gone after re-delivering the CIDR")
	}
	if !c.allowlistFor("10.1.1.1:1").Allowed("a2.com") {
		t.Error("new a2.com should be active")
	}
}

func TestInjectFor_GlobalByHost(t *testing.T) {
	c := mkRules(t)
	if _, ok := c.injectRules()["inj.com"]; !ok {
		t.Fatal("inject rule for inj.com missing")
	}
}
