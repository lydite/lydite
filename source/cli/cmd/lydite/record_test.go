package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
	"lydite/lydite/internal/ui"
)

// recordedFindingEvents is the finding events the record this commit appends
// carries, diffed against a ledger already holding prior.
//
// The ledger is a directory of its own rather than the state branch, because
// what is under test is the diff against whatever the fetched branch holds,
// and the closure historyRecords returns is handed exactly that directory.
// Every prior record is filed an hour before this commit, which is what makes
// it history rather than this commit's own events read back.
func recordedFindingEvents(t *testing.T, folded measurementsDoc, perComponent map[string]map[string]int,
	root map[string]int, found []finding.Finding, crashed []finding.Crash, prior ...[]ledger.FindingEvent) []ledger.FindingEvent {
	t.Helper()
	repo := gitRepo(t, map[string]string{"README.md": "a repository\n"})
	commitAll(t, repo, "the commit being recorded")
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

	records, why := historyRecords(context.Background(), repo, "main", folded, perComponent, root, found, crashed, nil)
	if records == nil {
		t.Fatalf("no record to append: %s", why)
	}
	recs, err := records(store)
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

func gosecClaim(path, site string) finding.Finding {
	return finding.Finding{Gate: "gosec", Component: "cli", Path: path, Rule: "G101", Site: site}
}

// A branch with no finding history has nothing open, so every claim this scan
// makes is one that appeared here — carrying the path and rule a history view
// renders without a second lookup.
func TestEveryFindingOfAFirstRecordingAppears(t *testing.T) {
	a := gosecClaim("a.go", "first")
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "token"}

	got := recordedFindingEvents(t, measurementsDoc{},
		map[string]map[string]int{"cli": {"gosec": 1}}, map[string]int{"gitleaks": 1},
		[]finding.Finding{a, leak}, nil)

	want := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: leak.Fingerprint(), Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key"},
		{Transition: ledger.FindingAppeared, Fingerprint: a.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want %+v", got, want)
	}
}

// A claim the branch held open and this scan of the same bucket no longer
// makes has resolved; a claim both hold has not changed, and costs the record
// nothing — growth follows churn, not the size of the open set.
func TestAFindingThatIsGoneResolvesAndOneThatStaysWritesNothing(t *testing.T) {
	stays, goes := gosecClaim("a.go", "stays"), gosecClaim("a.go", "goes")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: stays.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingAppeared, Fingerprint: goes.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := recordedFindingEvents(t, measurementsDoc{},
		map[string]map[string]int{"cli": {"gosec": 1}}, nil,
		[]finding.Finding{stays}, nil, prior)

	want := []ledger.FindingEvent{
		{Transition: ledger.FindingResolved, Fingerprint: goes.Fingerprint(), Gate: "gosec", Component: "cli"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want only the resolution of the claim that is gone", got)
	}
}

// A scan identical to what the branch holds open is a record with no finding
// events at all.
func TestAnUnchangedScanWritesNoFindingEvents(t *testing.T) {
	a := gosecClaim("a.go", "stays")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: a.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := recordedFindingEvents(t, measurementsDoc{},
		map[string]map[string]int{"cli": {"gosec": 1}}, nil,
		[]finding.Finding{a}, nil, prior)

	if len(got) != 0 {
		t.Errorf("events = %+v, want none: nothing appeared and nothing resolved", got)
	}
}

// A bucket this recording did not measure — a gate switched off, a component
// not scanned, a root-scoped gate that did not run — keeps what it held open.
// Its findings' absence from this scan is the gate not looking, and a
// resolution recorded there would close a finding nothing looked for.
func TestABucketThisRecordingDidNotMeasureResolvesNothing(t *testing.T) {
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: "v1:0000000000000001", Gate: "staticcheck", Component: "cli", Path: "a.go"},
		{Transition: ledger.FindingAppeared, Fingerprint: "v1:0000000000000002", Gate: "gosec", Component: "web", Path: "b.go"},
		{Transition: ledger.FindingAppeared, Fingerprint: "v1:0000000000000003", Gate: "semgrep", Path: "c.go"},
	}

	got := recordedFindingEvents(t, measurementsDoc{},
		map[string]map[string]int{"cli": {"gosec": 0}}, map[string]int{"gitleaks": 0},
		nil, nil, prior)

	if len(got) != 0 {
		t.Errorf("events = %+v, want none: every open finding sits in a bucket this recording did not measure", got)
	}
}

// No scan document read is no gate measured, so a recording carrying other
// scalars carries no finding events — not a resolution of every finding the
// branch held open.
func TestARecordingWithNoScanWritesNoFindingEvents(t *testing.T) {
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: "v1:0000000000000001", Gate: "gosec", Component: "cli", Path: "a.go"},
	}
	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}
	perComponent, root := findingCounts(t.TempDir(), decl, config.Default(), nil, false)

	got := recordedFindingEvents(t,
		measurementsDoc{Tests: map[string]junit.Counts{"cli": {Total: 3}}},
		perComponent, root, nil, nil, prior)

	if got != nil {
		t.Errorf("events = %+v, want none: nothing was scanned", got)
	}
}

// A bucket the scan names as crashed is not measured, however findingCounts
// counts it: a scanner that crashed made no claim, so it counts 0 like a clean
// one, and reading that as measured would record every finding it held open as
// resolved and then as appeared again on the next clean run. Its open set
// carries over untouched — nothing resolves there, and a partial claim it did
// make does not appear — while a bucket beside it that finished is diffed as
// ever.
func TestACrashedBucketKeepsWhatItHeldOpen(t *testing.T) {
	held := gosecClaim("a.go", "held")
	fixed := finding.Finding{Gate: "govulncheck", Component: "cli", Path: "go.mod", Rule: "GO-1", Site: "GO-1\x1fm"}
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "held"}
	partial := finding.Finding{Gate: "gitleaks", Path: "c.env", Rule: "generic-api-key", Site: "partial"}
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: held.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingAppeared, Fingerprint: fixed.Fingerprint(), Gate: "govulncheck", Component: "cli", Path: "go.mod", Rule: "GO-1"},
		{Transition: ledger.FindingAppeared, Fingerprint: leak.Fingerprint(), Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key"},
	}

	// Through the document, so the crash reaches the recording the way a scan
	// job's does: written beside the findings, read back by readReports.
	dir := t.TempDir()
	scan := ui.NewReport("scan")
	scan.AddFindings(partial)
	scan.AddCrashed(finding.Crash{Gate: "gosec", Component: "cli"}, finding.Crash{Gate: "gitleaks"})
	f, err := os.Create(filepath.Join(dir, documentName("scan"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	if err := scan.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	read := readReports(ui.NewReport("record"), []string{dir})

	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}
	perComponent, root := findingCounts(t.TempDir(), decl, config.Default(), read.found, read.scanned)
	if n, ok := perComponent["cli"]["gosec"]; !ok || n != 0 {
		t.Fatalf("gosec(cli) counts %d (keyed %v), want the 0 a crashed scanner still records", n, ok)
	}

	got := recordedFindingEvents(t, measurementsDoc{}, perComponent, root, read.found, read.crashed, prior)

	want := []ledger.FindingEvent{
		{Transition: ledger.FindingResolved, Fingerprint: fixed.Fingerprint(), Gate: "govulncheck", Component: "cli"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %+v, want only the finished bucket's resolution — nothing from a crashed one", got)
	}
}
