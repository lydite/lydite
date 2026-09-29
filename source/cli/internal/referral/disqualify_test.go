package referral

import (
	"testing"

	"lydite/lydite/internal/annotation"
)

// The mutation-equivalence marker is a suppression token whatever language it
// sits in — internal/referral reads it by the prefix every gate's
// declaration shares, not by which grammar's comment syntax carries it.
// TestTheEquivalentMutantAnnotationIsASuppression in referral_test.go pins
// this for Go; this pins the same behaviour for a TypeScript line, so a
// declaration added there refers the change exactly as one in Go does.
func TestTheEquivalentMutantAnnotationIsASuppressionInTypeScript(t *testing.T) {
	d := Disqualifications(Change{
		Paths: []string{"src/a.ts"},
		Added: []DiffLine{{Path: "src/a.ts", Text: "\treturn n < 10; // " + annotation.Marker(annotation.Mutation) + "[the caller bounds n]"}},
	}, Disqualifiers{})
	if len(d) != 1 || d[0].Kind != "suppression added" {
		t.Fatalf("got %+v, want one \"suppression added\"", d)
	}
}
