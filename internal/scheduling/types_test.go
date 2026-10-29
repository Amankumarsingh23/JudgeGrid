package scheduling

import "testing"

func TestPolicyReservesCapacityAndBoundsScope(t *testing.T) {
	p := Default()
	p.Enabled = true
	p.Principals = []string{"a", "b"}
	if e := p.Validate(); e != nil {
		t.Fatal(e)
	}
	p.PrincipalActive = p.GlobalActive
	if p.Validate() == nil {
		t.Fatal("one principal can occupy entire multi-slot pool")
	}
	p = Default()
	p.Enabled = true
	if p.Validate() == nil {
		t.Fatal("empty enabled scope")
	}
	p.Principals = []string{"a", "a"}
	if p.Validate() == nil {
		t.Fatal("duplicate scope")
	}
}
