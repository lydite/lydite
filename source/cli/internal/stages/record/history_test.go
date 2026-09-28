package recordstages

import (
	"context"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

func recordGosecClaim(path, site string) finding.Finding {
	return finding.Finding{Gate: "gosec", Component: "cli", Path: path, Rule: "G101", Site: site}
}

// A bucket the scan names as crashed is not measured, however FindingCounts
// counts it: a scanner that crashed made no claim, so it counts 0 like a clean
// one, and reading that as measured would record every finding it held open as
// resolved and then as appeared again on the next clean run. It is left out of
// scope, so its open set carries over untouched — nothing resolves there, and a
// partial claim it did make does not appear — while a bucket beside it that
// finished stays in scope and is diffed as ever.
func TestACrashedBucketIsLeftOutOfScope(t *testing.T) {
	partial := finding.Finding{Gate: "gitleaks", Path: "c.env", Rule: "generic-api-key", Site: "partial"}

	// Through ReadReports, so the crash reaches the recording the way a scan
	// job's does: read beside the findings, out of the directory's scan
	// document.
	reader := &recordReader{t: t, scans: map[string]recordRead[Scan]{
		"scan": {value: Scan{
			Findings: []finding.Finding{partial},
			Crashed:  []finding.Crash{{Gate: "gosec", Component: "cli"}, {Gate: "gitleaks"}},
		}},
	}}
	read, err := ReadReports(context.Background(), ReadReportsIn{Reader: reader, Reports: []string{"scan"}})
	if err != nil {
		t.Fatalf("ReadReports: %v", err)
	}

	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}
	perComponent, root := recordFindingCounts(t.TempDir(), decl, config.Default(), read.Found, read.Scanned)
	if n, ok := perComponent["cli"]["gosec"]; !ok || n != 0 {
		t.Fatalf("gosec(cli) counts %d (keyed %v), want the 0 a crashed scanner still records", n, ok)
	}
	if _, ok := root["gitleaks"]; !ok {
		t.Fatalf("root counts %+v, want gitleaks keyed: a crashed scanner is still counted", root)
	}
	if _, ok := perComponent["cli"]["govulncheck"]; !ok {
		t.Fatalf("cli counts %+v, want govulncheck keyed beside the crashed gosec", perComponent["cli"])
	}

	out, err := ComposeLedgerInputs(context.Background(), ComposeLedgerInputsIn{
		PerComponent: perComponent, Root: root, Crashed: read.Crashed,
	})
	if err != nil {
		t.Fatalf("ComposeLedgerInputs: %v", err)
	}

	for _, crashed := range []ledger.FindingBucket{{Gate: "gosec", Component: "cli"}, {Gate: "gitleaks"}} {
		if out.Scope[crashed] {
			t.Errorf("scope holds %+v, which the scan named as crashed", crashed)
		}
	}
	if finished := (ledger.FindingBucket{Gate: "govulncheck", Component: "cli"}); !out.Scope[finished] {
		t.Errorf("scope = %+v, want the finished bucket %+v in it", out.Scope, finished)
	}
}

// A component contributing no scalar at all is a point on no line, and is
// dropped. One carrying any single scalar is kept: coverage without a score is
// every non-Go component, and test counts without coverage is every component
// whose suite went red.
func TestOnlyAComponentWithNoScalarAtAllIsDropped(t *testing.T) {
	folded := Measurements{
		Components: map[string]Measurement{
			"measured": {Entry: recordEntry(3, 4)},
			"scored": {Entry: gitstate.Entry{Producer: "go 1.26"},
				CRAP: &gitstate.CRAPEntry{Above: 0, Worst: 12.5}},
			"empty": {Entry: gitstate.Entry{Producer: "go 1.26"}},
		},
		Tests: map[string]junit.Counts{"red": {Total: 9, Failed: 2}},
	}
	got := historyComponents(folded, nil, nil)

	for _, name := range []string{"measured", "scored", "red"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s was dropped, but it carries a scalar", name)
		}
	}
	if _, ok := got["empty"]; ok {
		t.Error("a component carrying no scalar at all was kept")
	}
	if c := got["measured"]; c.Coverage == nil || c.Coverage.Covered != 3 {
		t.Errorf("measured = %+v, want its line counts", c.Coverage)
	}
	if c := got["scored"]; c.CRAP == nil || c.Coverage != nil {
		t.Errorf("scored = %+v, want a score and no coverage", c)
	}
	if c := got["red"]; c.Tests == nil || c.Tests.Failed != 2 {
		t.Errorf("red = %+v, want the counts from a suite that failed", c.Tests)
	}
}

// The counts a mutation run took reach the component they were taken for,
// including one the measurements never mention: the merge commit's diff decides
// what is mutated, and a component whose suite was carried is mutated all the
// same.
func TestTheMutantCountsReachTheComponentTheyWereTakenFor(t *testing.T) {
	folded := Measurements{
		Components: map[string]Measurement{
			"measured": {Entry: recordEntry(3, 4)},
		},
	}
	got := historyComponents(folded, nil, map[string]MutantCounts{
		"measured": {Killed: 4, TimedOut: 1, OutOfMemory: 2, Survived: 0, Unviable: 3, Acknowledged: 5, ElapsedSeconds: 90},
		"carried":  {Killed: 1},
	})

	// The time the run cost travels with the counts it bought: a mutation run
	// cannot be recomputed after the merge, so a cost this recording drops is
	// one nothing can ever measure again.
	want := ledger.Mutation{Killed: 4, TimedOut: 1, OutOfMemory: 2, Survived: 0, Unviable: 3, Acknowledged: 5, ElapsedSeconds: 90}
	if c := got["measured"]; c.Mutation == nil || *c.Mutation != want {
		t.Errorf("measured = %+v, want %+v", c.Mutation, want)
	}
	// A component the measurements say nothing about carries its mutation and
	// nothing else, rather than being dropped as a point on no line.
	c, ok := got["carried"]
	if !ok {
		t.Fatalf("a component known only from the counts was dropped: %+v", got)
	}
	if c.Mutation == nil || c.Mutation.Killed != 1 || c.Coverage != nil || c.Tests != nil {
		t.Errorf("carried = %+v, want its counts alone", c)
	}
}

// A recording that read no counts records none, and never a zeroed run: a
// component nothing mutated is absent from the counts, and zeros there would
// read as a suite that killed every mutant.
func TestARecordingThatReadNoCountsRecordsNoMutation(t *testing.T) {
	folded := Measurements{
		Components: map[string]Measurement{"measured": {Entry: recordEntry(3, 4)}},
		Tests:      map[string]junit.Counts{"red": {Total: 9, Failed: 2}},
	}
	for name, c := range historyComponents(folded, nil, nil) {
		if c.Mutation != nil {
			t.Errorf("%s = %+v, want no mutation from a recording that read none", name, c.Mutation)
		}
	}
}

// The history records what was measured, never what a baseline anchored: a
// within-tolerance dip is anchored so the gate does not ratchet down, and a
// line drawn through the anchored number is one nothing ever measured.
func TestHistoryComponentsRecordsTheUnanchoredCounts(t *testing.T) {
	measured := recordEntry(799, 1000).LineCount
	folded := Measurements{Components: map[string]Measurement{
		"dipped": {Entry: recordEntry(800, 1000), Unanchored: &measured},
		"steady": {Entry: recordEntry(5, 10)},
	}}

	got := historyComponents(folded, nil, nil)

	want := map[string]ledger.Component{
		"dipped": {Producer: "go 1.26", Coverage: &ledger.Lines{Covered: 799, Total: 1000}},
		"steady": {Producer: "go 1.26", Coverage: &ledger.Lines{Covered: 5, Total: 10}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("historyComponents = %+v, want %+v", got, want)
	}
}

// Every scalar reaches the component it was taken for, whichever document it
// came out of, and a component the counts alone name carries its counts alone.
func TestHistoryComponentsCarriesEveryScalarToItsComponent(t *testing.T) {
	folded := Measurements{
		Components: map[string]Measurement{
			"svc": {Entry: recordEntry(1, 2), CRAP: &gitstate.CRAPEntry{Above: 2, Worst: 40}},
		},
		Tests: map[string]junit.Counts{"svc": {Total: 4, Failed: 1}},
	}
	perComponent := map[string]map[string]int{"svc": {"gosec": 2}, "web": {"biome": 0}}

	got := historyComponents(folded, perComponent, nil)

	want := map[string]ledger.Component{
		"svc": {
			Producer: "go 1.26",
			Coverage: &ledger.Lines{Covered: 1, Total: 2},
			CRAP:     &ledger.CRAP{Above: 2, Worst: 40},
			Tests:    &junit.Counts{Total: 4, Failed: 1},
			Findings: map[string]int{"gosec": 2},
		},
		"web": {Findings: map[string]int{"biome": 0}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("historyComponents = %+v, want %+v", got, want)
	}
}

// A bucket is measured when a count keyed it, for a component or over the
// repository, and not when the scan names it as crashed.
func TestFindingScopeIsEveryCountedBucketLessTheCrashedOnes(t *testing.T) {
	got := findingScope(
		map[string]map[string]int{"cli": {"gosec": 0, "govulncheck": 2}, "web": {"biome": 1}},
		map[string]int{"semgrep": 0, "gitleaks": 3},
		[]finding.Crash{{Gate: "govulncheck", Component: "cli"}, {Gate: "gitleaks"}, {Gate: "biome"}},
	)
	want := map[ledger.FindingBucket]bool{
		{Gate: "gosec", Component: "cli"}: true,
		{Gate: "biome", Component: "web"}: true,
		{Gate: "semgrep"}:                 true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findingScope = %+v, want %+v", got, want)
	}
}

// Every value a history is composed from comes out of the one stage: each
// component's scalars, the root-scoped counts as they were counted, and the
// buckets in scope.
func TestComposeLedgerInputsCarriesTheScalarsTheCountsAndTheScope(t *testing.T) {
	folded := Measurements{Components: map[string]Measurement{"svc": {Entry: recordEntry(1, 2)}}}
	perComponent := map[string]map[string]int{"svc": {"gosec": 2}}
	root := map[string]int{"gitleaks": 1}
	mutants := map[string]MutantCounts{"svc": {Killed: 3}}

	got, err := ComposeLedgerInputs(context.Background(), ComposeLedgerInputsIn{
		Folded: folded, PerComponent: perComponent, Root: root, Mutants: mutants,
	})
	if err != nil {
		t.Fatalf("ComposeLedgerInputs: %v", err)
	}
	want := ComposeLedgerInputsOut{
		Components:   historyComponents(folded, perComponent, mutants),
		RootFindings: map[string]int{"gitleaks": 1},
		Scope:        map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "svc"}: true, {Gate: "gitleaks"}: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ComposeLedgerInputs = %+v, want %+v", got, want)
	}
}
