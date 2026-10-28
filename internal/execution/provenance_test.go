package execution

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testProvenance() Provenance {
	return Provenance{"judgegrid-sandbox-" + strings.Repeat("a", 26), strings.Repeat("b", 26), "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440001", strings.Repeat("c", 64), time.Now().UTC().Format(time.RFC3339Nano), 1}
}
func TestStructuredProvenance(t *testing.T) {
	p := testProvenance()
	for _, role := range []string{"artifact", "compile", "prepare", "run", "run-16"} {
		actual, r, e := ParseLabels(p.Labels(role))
		if e != nil || actual != p || r != role {
			t.Fatal(actual, r, e)
		}
	}
	for _, key := range []string{"judgegrid.execution", "judgegrid.controller", "judgegrid.run", "judgegrid.submission", "judgegrid.owner", "judgegrid.created", "judgegrid.generation", "judgegrid.role", "judgegrid.namespace"} {
		m := p.Labels("run")
		m[key] = "../invalid"
		if _, _, e := ParseLabels(m); e == nil {
			t.Fatal("invalid accepted", key)
		}
	}
}
func TestExactResourceRelationship(t *testing.T) {
	p := testProvenance()
	r := Resource{Kind: "container", ID: strings.Repeat("a", 64), Name: ResourceName(p, "run"), Labels: p.Labels("run")}
	if !OwnedResource(r, p, "run") {
		t.Fatal("valid rejected")
	}
	r.Name += "-other"
	if OwnedResource(r, p, "run") {
		t.Fatal("name prefix accepted")
	}
	r.Name = ResourceName(p, "run")
	r.Labels["judgegrid.extra"] = "untrusted"
	if OwnedResource(r, p, "run") {
		t.Fatal("extra provenance accepted")
	}
}
func TestTypedPolicyConstructionFailure(t *testing.T) {
	_, err := NewDocker(Images{CPP: "latest", Python: "latest"}, "unused", 1)
	var infra *InfrastructureError
	if !errors.As(err, &infra) {
		t.Fatal("policy failed without typed infrastructure error", err)
	}
}
