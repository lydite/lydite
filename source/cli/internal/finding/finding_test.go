package finding

import (
	"strings"
	"testing"
)

// base is a finding with every identifying ingredient set, so a test that
// varies one field is varying only that one.
func base() Finding {
	return Finding{
		Gate:      "mutation",
		Component: "cli",
		Path:      "internal/runner/runner.go",
		Line:      412,
		Message:   "a survivor",
		Site:      "relational < >=",
	}
}

func TestAFingerprintSurvivesAnEditAboveIt(t *testing.T) {
	moved := base()
	moved.Line = 998
	moved.EndLine = 1002

	if got, want := moved.Fingerprint(), base().Fingerprint(); got != want {
		t.Errorf("a claim that moved down the file was re-identified: %s, want %s", got, want)
	}
}

func TestRewordingADiagnosticDoesNotReidentifyIt(t *testing.T) {
	reworded := base()
	reworded.Message = "an entirely different sentence"
	reworded.Severity = "critical"
	reworded.Detail = []string{"a call stack the tool grew later"}

	if got, want := reworded.Fingerprint(), base().Fingerprint(); got != want {
		t.Errorf("rewording the claim re-identified it: %s, want %s", got, want)
	}
}

func TestAFingerprintChangesWithTheClaimItself(t *testing.T) {
	for _, tc := range []struct {
		name string
		with func(*Finding)
	}{
		{"the gate", func(f *Finding) { f.Gate = "crap" }},
		{"the component", func(f *Finding) { f.Component = "web" }},
		{"the path", func(f *Finding) { f.Path = "internal/runner/other.go" }},
		{"the site", func(f *Finding) { f.Site = "relational > <=" }},
		{"the ordinal", func(f *Finding) { f.Ordinal = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base()
			tc.with(&changed)
			if changed.Fingerprint() == base().Fingerprint() {
				t.Errorf("changing %s left the fingerprint %s", tc.name, changed.Fingerprint())
			}
		})
	}
}

func TestIngredientsCannotRunTogether(t *testing.T) {
	left, right := base(), base()
	left.Gate, left.Component = "ab", "c"
	right.Gate, right.Component = "a", "bc"

	if left.Fingerprint() == right.Fingerprint() {
		t.Errorf("two different claims hashed alike: %s", left.Fingerprint())
	}
}

func TestAFingerprintCarriesTheVersionItWasDerivedUnder(t *testing.T) {
	got := base().Fingerprint()
	if !strings.HasPrefix(got, Version+":") {
		t.Errorf("fingerprint %q does not name the version it came from", got)
	}
	if rest := strings.TrimPrefix(got, Version+":"); len(rest) != 16 {
		t.Errorf("fingerprint body is %d characters, want 16: %q", len(rest), rest)
	}
}

func TestIdenticalSitesInOneFileAreToldApart(t *testing.T) {
	// Two mutants of two identical comparisons: everything a fingerprint
	// reads is the same, and only source order separates them.
	findings := []Finding{
		{Gate: "mutation", Component: "cli", Path: "a.go", Line: 10, Site: "relational < >="},
		{Gate: "mutation", Component: "cli", Path: "a.go", Line: 40, Site: "relational < >="},
		{Gate: "mutation", Component: "cli", Path: "b.go", Line: 10, Site: "relational < >="},
	}
	Number(findings)

	if got := []int{findings[0].Ordinal, findings[1].Ordinal, findings[2].Ordinal}; got[0] != 0 || got[1] != 1 || got[2] != 0 {
		t.Fatalf("ordinals are %v, want [0 1 0] — the third is in another file and starts again", got)
	}
	seen := map[string]bool{}
	for _, f := range findings {
		if seen[f.Fingerprint()] {
			t.Errorf("two findings share the fingerprint %s", f.Fingerprint())
		}
		seen[f.Fingerprint()] = true
	}
}

func TestNumberingIsStableAcrossRuns(t *testing.T) {
	build := func() []Finding {
		return []Finding{
			{Path: "a.go", Site: "x"},
			{Path: "a.go", Site: "y"},
			{Path: "a.go", Site: "x"},
		}
	}
	first, second := build(), build()
	Number(first)
	Number(second)

	for i := range first {
		if first[i].Ordinal != second[i].Ordinal {
			t.Errorf("finding %d numbered %d then %d", i, first[i].Ordinal, second[i].Ordinal)
		}
	}
	if first[2].Ordinal != 1 {
		t.Errorf("the second `x` in a.go is ordinal %d, want 1", first[2].Ordinal)
	}
}

func TestAnchoredSaysHowPreciselyAClaimReachesTheChange(t *testing.T) {
	changed := map[string][]int{"touched.go": {10, 11, 40}}
	findings := []Finding{
		{Path: "touched.go", Line: 10},
		{Path: "touched.go", Line: 25},
		{Path: "untouched.go", Line: 10},
	}
	Anchored(findings, changed)

	want := []Anchor{AnchorLine, AnchorFile, AnchorNowhere}
	for i, w := range want {
		if findings[i].Anchor != w {
			t.Errorf("finding %d anchored %q, want %q", i, findings[i].Anchor, w)
		}
	}
}

func TestAMultiLineClaimReachesTheChangeAnywhereInItsSpan(t *testing.T) {
	// The change touched one line in the middle of an untested run. The run
	// is the claim, so the whole of it is anchorable.
	findings := []Finding{{Path: "a.go", Line: 20, EndLine: 30}}
	Anchored(findings, map[string][]int{"a.go": {25}})

	if findings[0].Anchor != AnchorLine {
		t.Errorf("a span containing a changed line anchored %q, want %q", findings[0].Anchor, AnchorLine)
	}
}

func TestASpanThatMissesEveryChangedLineAnchorsToItsFile(t *testing.T) {
	findings := []Finding{{Path: "a.go", Line: 20, EndLine: 30}}
	Anchored(findings, map[string][]int{"a.go": {5, 60}})

	if findings[0].Anchor != AnchorFile {
		t.Errorf("a span beside the change anchored %q, want %q", findings[0].Anchor, AnchorFile)
	}
}

func TestNormaliseKeepsTheTextAndNotItsLayout(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\t\tif a < b {", "if a < b {"},
		{"if   a  <  b {", "if a < b {"},
		{"  if a < b {  ", "if a < b {"},
		{"if a <\n\tb {", "if a < b {"},
	} {
		if got := Normalise(tc.in); got != tc.want {
			t.Errorf("Normalise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
