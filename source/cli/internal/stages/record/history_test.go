package recordstages

import (
	"context"
	"reflect"
	"strings"
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

// recordCommit commits every change in dir and describes the commit made.
func recordCommit(t *testing.T, dir, message string) gitstate.Commit {
	t.Helper()
	recordGit(t, dir, "add", "-A")
	recordGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", message)
	c, err := gitstate.DescribeCommit(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// recordLedger is a ledger directory holding one entry per commit named, on
// branch main, each filed an hour apart and the last an hour before now.
func recordLedger(t *testing.T, now time.Time, commits ...string) string {
	t.Helper()
	store := t.TempDir()
	for i, commit := range commits {
		rec := ledger.Record{
			Kind:       ledger.KindEntry,
			At:         now.Add(-time.Duration(len(commits)-i) * time.Hour),
			Commit:     commit,
			Branch:     "main",
			Components: map[string]ledger.Component{"svc": {Tests: &junit.Counts{Total: 1}}},
		}
		if _, _, err := ledger.Append(store, []ledger.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// recordScalar is a fold carrying one scalar, which is enough for a recording
// to have history to append.
func recordScalar() Measurements {
	return Measurements{Tests: map[string]junit.Counts{"svc": {Total: 3}}}
}

// A branch with no finding history has nothing open, so every claim this scan
// makes is one that appeared here — carrying the path and rule a history view
// renders without a second lookup.
func TestEveryFindingOfAFirstRecordingAppears(t *testing.T) {
	a := recordGosecClaim("a.go", "first")
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "token"}

	got := recordedFindingEvents(t, Measurements{},
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
	stays, goes := recordGosecClaim("a.go", "stays"), recordGosecClaim("a.go", "goes")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: stays.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingAppeared, Fingerprint: goes.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := recordedFindingEvents(t, Measurements{},
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
	a := recordGosecClaim("a.go", "stays")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: a.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := recordedFindingEvents(t, Measurements{},
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

	got := recordedFindingEvents(t, Measurements{},
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
	perComponent, root := recordFindingCounts(t.TempDir(), decl, config.Default(), nil, false)

	got := recordedFindingEvents(t,
		Measurements{Tests: map[string]junit.Counts{"cli": {Total: 3}}},
		perComponent, root, nil, nil, prior)

	if got != nil {
		t.Errorf("events = %+v, want none: nothing was scanned", got)
	}
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

// A fold whose components carry no scalar appends nothing. A record naming a
// commit and holding no number is a point on no line, and writing one would
// make the history claim a measurement that was never taken.
func TestHistoryIsNotAppendedForAFoldWithNoScalars(t *testing.T) {
	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: t.TempDir(), Branch: "main",
		Folded: Measurements{Components: map[string]Measurement{
			"api": {Entry: gitstate.Entry{Producer: "go 1.26"}},
		}},
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	if out.Records != nil {
		t.Error("a fold carrying no scalar produced records")
	}
	if out.Reason != HistoryNoScalar {
		t.Errorf("Reason = %d, want HistoryNoScalar", out.Reason)
	}
}

// A root-scoped count is a scalar of its own: a repository whose every suite
// was carried and whose scan ran still has a point to put on that line.
func TestARootScopedCountAloneIsHistoryToAppend(t *testing.T) {
	dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})

	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: dir, Branch: "main", Root: map[string]int{"semgrep": 0},
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	if out.Reason != HistoryToAppend || out.Records == nil {
		t.Errorf("ComposeHistory = reason %d, records %t; want history to append", out.Reason, out.Records != nil)
	}
}

// A checkout that names no branch, and a caller that states none, files
// nothing: one filed under a guessed branch puts points on the wrong line.
func TestADetachedCheckoutWithNoBranchStatedAppendsNothing(t *testing.T) {
	dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
	recordGit(t, dir, "checkout", "--quiet", "--detach", "HEAD")

	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{Dir: dir, Folded: recordScalar()})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	if want := (ComposeHistoryOut{Reason: HistoryNoBranch}); out.Reason != want.Reason || out.Records != nil || out.Err != nil {
		t.Errorf("ComposeHistory = %+v, want %+v", out, want)
	}
}

// The caller's own statement names the line a detached checkout's record
// joins, and a checkout on a branch names its own when the caller states none.
func TestTheRecordIsFiledUnderTheStatedOrCheckedOutBranch(t *testing.T) {
	for _, tc := range []struct {
		name, detach, stated, want string
	}{
		{name: "the checked-out branch", want: "main"},
		{name: "a stated branch on a detached checkout", detach: "HEAD", stated: "release/1.x", want: "release/1.x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
			if tc.detach != "" {
				recordGit(t, dir, "checkout", "--quiet", "--detach", tc.detach)
			}
			out, err := ComposeHistory(context.Background(), ComposeHistoryIn{Dir: dir, Branch: tc.stated, Folded: recordScalar()})
			if err != nil {
				t.Fatalf("ComposeHistory: %v", err)
			}
			if out.Records == nil {
				t.Fatalf("no record to append: reason %d", out.Reason)
			}
			recs, err := out.Records(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if len(recs) != 1 || recs[0].Branch != tc.want {
				t.Errorf("records = %+v, want one filed under %s", recs, tc.want)
			}
		})
	}
}

// A commit git cannot describe is no record, with git's own reason carried
// for the command to say.
func TestACommitThatCannotBeDescribedAppendsNothingAndSaysWhy(t *testing.T) {
	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: t.TempDir(), Branch: "main", Folded: recordScalar(),
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	if out.Reason != HistoryUndescribed || out.Records != nil {
		t.Errorf("ComposeHistory = reason %d, records %t; want HistoryUndescribed and none", out.Reason, out.Records != nil)
	}
	if out.Err == nil || !strings.HasPrefix(out.Err.Error(), "git show HEAD: ") {
		t.Errorf("Err = %v, want DescribeCommit's own error", out.Err)
	}
}

// The record names the commit and never only the tree: a baseline answers
// "what was measured for this content", and history is a sequence of events.
func TestTheRecordDescribesTheCommitItIsFiledAgainst(t *testing.T) {
	dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
	head := recordCommit(t, dir, "the commit being recorded")
	root := map[string]int{"semgrep": 1}

	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: dir, Branch: "main", Folded: recordScalar(), Root: root,
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}
	recs, err := out.Records(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := []ledger.Record{{
		Kind: ledger.KindEntry, At: head.At, Commit: head.SHA, Parent: head.Parent, Tree: head.Tree,
		Branch:       "main",
		Components:   map[string]ledger.Component{"svc": {Tests: &junit.Counts{Total: 3}}},
		RootFindings: root,
	}}
	if !reflect.DeepEqual(recs, want) {
		t.Errorf("records = %+v, want %+v", recs, want)
	}
}

// Each call diffs against the branch it is handed, and never carries an
// earlier call's events: a retry fetches a branch another run may have
// advanced.
func TestEachAttemptDiffsAgainstTheBranchItIsHanded(t *testing.T) {
	dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
	head, err := gitstate.DescribeCommit(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	a := recordGosecClaim("a.go", "first")
	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{
		Dir: dir, Branch: "main",
		PerComponent: map[string]map[string]int{"cli": {"gosec": 1}},
		Found:        []finding.Finding{a},
	})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}

	fresh, err := out.Records(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || len(fresh[0].FindingEvents) != 1 {
		t.Fatalf("records over an empty branch = %+v, want one entry whose claim appeared", fresh)
	}

	advanced := t.TempDir()
	if _, _, err := ledger.Append(advanced, []ledger.Record{{
		Kind: ledger.KindEntry, At: head.At.Add(-time.Hour), Commit: head.Parent + "x", Branch: "main",
		FindingEvents: fresh[0].FindingEvents,
	}}); err != nil {
		t.Fatal(err)
	}
	retried, err := out.Records(advanced)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range retried {
		if rec.Kind == ledger.KindEntry && rec.FindingEvents != nil {
			t.Errorf("the retry's entry carries %+v, want nothing: the branch it was handed already holds the claim open", rec.FindingEvents)
		}
	}
}

// Whether a gap precedes the record is asked of the branch the write hands
// over: none when nothing is recorded yet or the last record is this commit or
// its parent, one of known width when the last recorded commit is an ancestor,
// and one of unknown width when it is not.
func TestAGapIsWrittenOnlyWhereTheLastRecordIsNotThisCommitsParent(t *testing.T) {
	dir, _ := recordRepo(t, map[string]string{"README.md": "a repository\n"})
	first := recordCommit(t, dir, "recorded")
	recordCommit(t, dir, "never recorded")
	head := recordCommit(t, dir, "the commit being recorded")

	out, err := ComposeHistory(context.Background(), ComposeHistoryIn{Dir: dir, Branch: "main", Folded: recordScalar()})
	if err != nil {
		t.Fatalf("ComposeHistory: %v", err)
	}

	for _, tc := range []struct {
		name    string
		store   string
		wantGap *ledger.Gap
	}{
		{name: "nothing recorded yet", store: recordLedger(t, head.At)},
		{name: "the parent recorded", store: recordLedger(t, head.At, first.SHA, head.Parent)},
		{name: "this commit recorded", store: recordLedger(t, head.At, head.SHA)},
		{name: "an ancestor recorded", store: recordLedger(t, head.At, first.SHA), wantGap: &ledger.Gap{
			From: first.SHA, Missing: 1,
			Reason: "1 commit(s) between the last recorded one and this one were never recorded",
		}},
		{name: "a commit off this history recorded", store: recordLedger(t, head.At, "0123456789abcdef0123456789abcdef01234567"), wantGap: &ledger.Gap{
			From:   "0123456789abcdef0123456789abcdef01234567",
			Reason: "the last recorded commit is not an ancestor of this one, so how many recordings are missing cannot be established",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs, err := out.Records(tc.store)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantGap == nil {
				if len(recs) != 1 || recs[0].Kind != ledger.KindEntry {
					t.Errorf("records = %+v, want the entry alone", recs)
				}
				return
			}
			if len(recs) != 2 {
				t.Fatalf("records = %+v, want a gap ahead of the entry", recs)
			}
			wantGap := ledger.Record{
				Kind: ledger.KindGap, At: head.At, Commit: head.SHA, Parent: head.Parent,
				Branch: "main", Gap: tc.wantGap,
			}
			if !reflect.DeepEqual(recs[0], wantGap) {
				t.Errorf("gap = %+v (%+v), want %+v (%+v)", recs[0], recs[0].Gap, wantGap, wantGap.Gap)
			}
			if recs[1].Kind != ledger.KindEntry || recs[1].Commit != head.SHA {
				t.Errorf("records[1] = %+v, want this commit's entry", recs[1])
			}
		})
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

// The events are sorted by gate, component, transition and fingerprint, so the
// same scan over the same history writes the same line; and one claim made
// twice appears once, as the first of it.
func TestFindingEventsAreSortedAndEachFingerprintAppearsOnce(t *testing.T) {
	first := finding.Finding{Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101", Site: "s"}
	again := first
	again.Rule = "G102"
	web := finding.Finding{Gate: "gosec", Component: "web", Path: "w.go", Rule: "G101", Site: "s"}
	root := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "s"}
	outside := finding.Finding{Gate: "biome", Component: "web", Path: "x.ts", Site: "s"}
	scope := map[ledger.FindingBucket]bool{
		{Gate: "gosec", Component: "cli"}: true,
		{Gate: "gosec", Component: "web"}: true,
		{Gate: "gitleaks"}:                true,
	}
	open := map[ledger.FindingBucket]map[string]bool{
		{Gate: "gosec", Component: "cli"}: {"v1:ffff": true, "v1:0000": true},
		{Gate: "biome", Component: "web"}: {"v1:1111": true},
	}

	got := findingEvents(scope, []finding.Finding{web, first, again, root, outside}, open)

	want := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: root.Fingerprint(), Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key"},
		{Transition: ledger.FindingAppeared, Fingerprint: first.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingResolved, Fingerprint: "v1:0000", Gate: "gosec", Component: "cli"},
		{Transition: ledger.FindingResolved, Fingerprint: "v1:ffff", Gate: "gosec", Component: "cli"},
		{Transition: ledger.FindingAppeared, Fingerprint: web.Fingerprint(), Gate: "gosec", Component: "web", Path: "w.go", Rule: "G101"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findingEvents = %+v, want %+v", got, want)
	}
}
