package licence

import (
	"slices"
	"testing"
)

func dep(pkg, version, lic string) Dependency {
	return Dependency{Package: pkg, Version: version, Licence: lic}
}

// A module that classifies several licence files is offering a choice, so the
// ids become an OR and any one allowed id passes it. Joined by AND, or reduced
// to the first id read, `gopkg.in/yaml.v2` fails an allow-list holding MIT.
func TestExpressionJoinsDetectedIdsAsAnOr(t *testing.T) {
	expr := Expression([]string{"Apache-2.0", "MIT"})
	if want := "Apache-2.0 OR MIT"; expr != want {
		t.Fatalf("Expression = %q, want %q", expr, want)
	}
	policy := NewPolicy([]string{"MIT"})
	if got := policy.Evaluate(dep("gopkg.in/yaml.v2", "v2.4.0", expr)); got != Conforms {
		t.Fatalf("a module carrying an allowed id among others = %q, want %q", got, Conforms)
	}
}

// The ids arrive in whatever order a directory walk produced, and the
// expression reaches a finding's Site. Unsorted, one module's identity changes
// with the filesystem and every claim about it re-anchors.
func TestExpressionOrdersAndDeduplicatesDetectedIds(t *testing.T) {
	forward := Expression([]string{"MIT", "Apache-2.0", "MIT"})
	backward := Expression([]string{"Apache-2.0", "MIT"})
	if forward != backward {
		t.Fatalf("Expression is order-dependent: %q vs %q", forward, backward)
	}
	if want := "Apache-2.0 OR MIT"; forward != want {
		t.Fatalf("Expression = %q, want %q", forward, want)
	}
}

// A module whose files classified as nothing has no expression to evaluate,
// and an empty string would be read by the policy as a licence rather than as
// the absence of one.
func TestExpressionOfNothingDetectedIsUnknown(t *testing.T) {
	for name, ids := range map[string][]string{
		"nil":            nil,
		"empty":          {},
		"blank":          {"", "   "},
		"blank and none": {" "},
	} {
		if got := Expression(ids); got != Unknown {
			t.Errorf("%s: Expression = %q, want %q", name, got, Unknown)
		}
	}
}

// Two licences against one package are two claims. A site that carried only
// the package would make them one and the report would drop the second.
func TestSiteSeparatesTwoLicencesAgainstOnePackage(t *testing.T) {
	first := Pair{Package: "yaml", Licence: "Apache-2.0"}
	second := Pair{Package: "yaml", Licence: "MPL-2.0"}
	if first.Site() == second.Site() {
		t.Fatalf("two licences against one package share the site %q", first.Site())
	}
	other := Pair{Package: "serde", Licence: "Apache-2.0"}
	if first.Site() == other.Site() {
		t.Fatalf("two packages under one licence share the site %q", first.Site())
	}
}

// The version locates a claim and never identifies it: keyed on the version,
// every bump of a grandfathered dependency reads as a new pair.
func TestSetKeysOnPackageAndLicenceAndNotVersion(t *testing.T) {
	bumped := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"), dep("cbindgen", "0.27.0", "MPL-2.0"))
	if bumped.Len() != 1 {
		t.Fatalf("a bump of one dependency holds %d pairs, want 1", bumped.Len())
	}
	if got := bumped.Dependencies()[0].Version; got != "0.26.0" {
		t.Fatalf("the set kept version %q, want the first named, 0.26.0", got)
	}
	relicensed := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"), dep("cbindgen", "0.26.0", "GPL-3.0-only"))
	if relicensed.Len() != 2 {
		t.Fatalf("one package under two licences holds %d pairs, want 2", relicensed.Len())
	}
}

// A map iterates in no order, so an unsorted set reports one tree's claims in
// a different order on every run.
func TestSetDependenciesAreOrderedByPackageThenLicence(t *testing.T) {
	set := NewSet(
		dep("serde", "1.0.0", "MPL-2.0"),
		dep("cbindgen", "0.26.0", "MPL-2.0"),
		dep("cbindgen", "0.26.0", "GPL-3.0-only"),
	)
	var got []string
	for _, d := range set.Dependencies() {
		got = append(got, d.Package+" "+d.Licence)
	}
	want := []string{"cbindgen GPL-3.0-only", "cbindgen MPL-2.0", "serde MPL-2.0"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// A component with no non-conforming dependency produces the zero Set, and
// every reader of it — including the base side of a comparison — reaches it
// without anything having allocated a map.
func TestTheZeroSetIsEmptyAndUsable(t *testing.T) {
	var zero Set
	if zero.Len() != 0 {
		t.Errorf("Len = %d, want 0", zero.Len())
	}
	if zero.Has(Pair{Package: "cbindgen", Licence: "MPL-2.0"}) {
		t.Error("the zero set claims to hold a pair")
	}
	if got := len(zero.Dependencies()); got != 0 {
		t.Errorf("Dependencies holds %d, want 0", got)
	}
	current := NewSet(dep("cbindgen", "0.26.0", "MPL-2.0"))
	if got := current.Without(zero).Len(); got != 1 {
		t.Errorf("subtracting the zero set left %d pairs, want 1", got)
	}
	if got := zero.Without(current).Len(); got != 0 {
		t.Errorf("the zero set minus a set holds %d pairs, want 0", got)
	}
}
