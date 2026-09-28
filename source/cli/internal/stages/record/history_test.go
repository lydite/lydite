package recordstages

import (
	"context"
	"reflect"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// recordedFindingEvents is the finding events the record this commit appends
// carries, diffed against a ledger already holding prior.
//
// The ledger is a directory of its own rather than the state branch, because
// what is under test is the diff against whatever the fetched branch holds,
// and the closure ComposeHistory returns is handed exactly that directory.
// Every prior record is filed an hour before this commit, which is what makes
// it history rather than this commit's own events read back.
func recordedFindingEvents(t *testing.T, folded Measurements, perComponent map[string]map[string]int,
	root map[string]int, found []finding.Finding, crashed []finding.Crash, prior ...[]ledger.FindingEvent) []ledger.FindingEvent {
	t.Helper()
	repo, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
	head, err := gitstate.DescribeCommit(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := t.TempDir()
	for i, events := range prior {
		rec := ledger.Record{
			Kind:          ledger.KindEntry,
			At:            head.At.Add(-time.Duration(len(prior)-i) * time.Hour),
			Commit:        "prior" + string(rune('a'+i)),
			Branch:        "main",
			FindingEvents: events,
		}
		if _, _, err := ledger.Append(store, []ledger.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: repo, Branch: "main", Folded: folded,
		PerComponent: perComponent, Root: root, Found: found, Crashed: crashed,
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	if out.Records == nil {
		t.Fatalf("no record to append: reason %d, %v", out.Reason, out.Err)
	}
	recs, err := out.Records(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Kind == ledger.KindEntry {
			return rec.FindingEvents
		}
	}
	t.Fatalf("no entry among %+v", recs)
	return nil
}

func recordGosecClaim(path, site string) finding.Finding {
	return finding.Finding{Gate: "gosec", Component: "cli", Path: path, Rule: "G101", Site: site}
}

// A bucket the scan names as crashed is not measured, however FindingCounts
// counts it: a scanner that crashed made no claim, so it counts 0 like a clean
// one, and reading that as measured would record every finding it held open as
// resolved and then as appeared again on the next clean run. Its open set
// carries over untouched — nothing resolves there, and a partial claim it did
// make does not appear — while a bucket beside it that finished is diffed as
// ever.
func TestACrashedBucketKeepsWhatItHeldOpen(t *testing.T) {
	held := recordGosecClaim("a.go", "held")
	fixed := finding.Finding{Gate: "govulncheck", Component: "cli", Path: "go.mod", Rule: "GO-1", Site: "GO-1\x1fm"}
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "held"}
	partial := finding.Finding{Gate: "gitleaks", Path: "c.env", Rule: "generic-api-key", Site: "partial"}
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: held.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingAppeared, Fingerprint: fixed.Fingerprint(), Gate: "govulncheck", Component: "cli", Path: "go.mod", Rule: "GO-1"},
		{Transition: ledger.FindingAppeared, Fingerprint: leak.Fingerprint(), Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key"},
	}

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

	got := recordedFindingEvents(t, Measurements{}, perComponent, root, read.Found, read.Crashed, prior)

	want := []ledger.FindingEvent{
		{Transition: ledger.FindingResolved, Fingerprint: fixed.Fingerprint(), Gate: "govulncheck", Component: "cli"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want only the finished bucket's resolution — nothing from a crashed one", got)
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
