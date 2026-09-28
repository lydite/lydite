package ledgerstages

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// A branch with no finding history has nothing open, so every claim this scan
// makes is one that appeared here — carrying the path and rule a history view
// renders without a second lookup.
func TestEveryFindingOfAFirstRecordingAppears(t *testing.T) {
	a := ledgerGosecClaim("a.go", "first")
	leak := finding.Finding{Gate: "gitleaks", Path: "b.env", Rule: "generic-api-key", Site: "token"}

	got := ledgerFindingEvents(t,
		map[string]ledger.Component{"cli": {Findings: map[string]int{"gosec": 1}}}, map[string]int{"gitleaks": 1},
		map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "cli"}: true, {Gate: "gitleaks"}: true},
		[]finding.Finding{a, leak})

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
	stays, goes := ledgerGosecClaim("a.go", "stays"), ledgerGosecClaim("a.go", "goes")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: stays.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
		{Transition: ledger.FindingAppeared, Fingerprint: goes.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := ledgerFindingEvents(t,
		map[string]ledger.Component{"cli": {Findings: map[string]int{"gosec": 1}}}, nil,
		map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "cli"}: true},
		[]finding.Finding{stays}, prior)

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
	a := ledgerGosecClaim("a.go", "stays")
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: a.Fingerprint(), Gate: "gosec", Component: "cli", Path: "a.go", Rule: "G101"},
	}

	got := ledgerFindingEvents(t,
		map[string]ledger.Component{"cli": {Findings: map[string]int{"gosec": 1}}}, nil,
		map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "cli"}: true},
		[]finding.Finding{a}, prior)

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

	got := ledgerFindingEvents(t,
		map[string]ledger.Component{"cli": {Findings: map[string]int{"gosec": 0}}}, map[string]int{"gitleaks": 0},
		map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "cli"}: true, {Gate: "gitleaks"}: true},
		nil, prior)

	if len(got) != 0 {
		t.Errorf("events = %+v, want none: every open finding sits in a bucket this recording did not measure", got)
	}
}

// No bucket in scope is no gate measured, so a recording carrying other
// scalars carries no finding events — not a resolution of every finding the
// branch held open.
func TestARecordingWithNoScanWritesNoFindingEvents(t *testing.T) {
	prior := []ledger.FindingEvent{
		{Transition: ledger.FindingAppeared, Fingerprint: "v1:0000000000000001", Gate: "gosec", Component: "cli", Path: "a.go"},
	}

	got := ledgerFindingEvents(t,
		map[string]ledger.Component{"cli": {Tests: &junit.Counts{Total: 3}}}, nil,
		map[ledger.FindingBucket]bool{}, nil, prior)

	if got != nil {
		t.Errorf("events = %+v, want none: nothing was scanned", got)
	}
}

// Nothing to append when no component, and no root-scoped gate, carries a
// scalar. A record naming a commit and holding no number is a point on no
// line, and writing one would make the history claim a measurement that was
// never taken.
func TestHistoryIsNotAppendedForARecordingWithNoScalars(t *testing.T) {
	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: t.TempDir(), BranchOverride: "main",
		Components: map[string]ledger.Component{},
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}
	if out.Records != nil {
		t.Error("a recording carrying no scalar produced records")
	}
	if out.Reason != (NoScalars{}) {
		t.Errorf("Reason = %#v, want NoScalars", out.Reason)
	}
}

// A root-scoped count is a scalar of its own: a repository whose every suite
// was carried and whose scan ran still has a point to put on that line.
func TestARootScopedCountAloneIsHistoryToAppend(t *testing.T) {
	dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})

	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: dir, BranchOverride: "main", RootFindings: map[string]int{"semgrep": 0},
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}
	if out.Reason != nil || out.Records == nil {
		t.Errorf("ComposeRecords = reason %#v, records %t; want history to append", out.Reason, out.Records != nil)
	}
}

// A checkout that names no branch, and a caller that states none, files
// nothing: one filed under a guessed branch puts points on the wrong line.
func TestADetachedCheckoutWithNoBranchStatedAppendsNothing(t *testing.T) {
	dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	ledgerGit(t, dir, "checkout", "--quiet", "--detach", "HEAD")

	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{Dir: dir, Components: ledgerScalar()})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}
	if want := (ComposeRecordsOut{Reason: NoBranch{}}); out.Reason != want.Reason || out.Records != nil {
		t.Errorf("ComposeRecords = %+v, want %+v", out, want)
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
			dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
			if tc.detach != "" {
				ledgerGit(t, dir, "checkout", "--quiet", "--detach", tc.detach)
			}
			out, err := ComposeRecords(context.Background(), ComposeRecordsIn{Dir: dir, BranchOverride: tc.stated, Components: ledgerScalar()})
			if err != nil {
				t.Fatalf("ComposeRecords: %v", err)
			}
			if out.Records == nil {
				t.Fatalf("no record to append: reason %#v", out.Reason)
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
	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: t.TempDir(), BranchOverride: "main", Components: ledgerScalar(),
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}
	undescribable, ok := out.Reason.(Undescribable)
	if !ok || out.Records != nil {
		t.Fatalf("ComposeRecords = reason %#v, records %t; want Undescribable and none", out.Reason, out.Records != nil)
	}
	if undescribable.Err == nil || !strings.HasPrefix(undescribable.Err.Error(), "git show HEAD: ") {
		t.Errorf("Err = %v, want DescribeCommit's own error", undescribable.Err)
	}
}

// The record names the commit and never only the tree: a baseline answers
// "what was measured for this content", and history is a sequence of events.
func TestTheRecordDescribesTheCommitItIsFiledAgainst(t *testing.T) {
	dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	head := ledgerCommit(t, dir, "the commit being recorded")
	root := map[string]int{"semgrep": 1}

	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: dir, BranchOverride: "main", Components: ledgerScalar(), RootFindings: root,
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
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
	dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	head, err := gitstate.DescribeCommit(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerGosecClaim("a.go", "first")
	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: dir, BranchOverride: "main",
		Components: map[string]ledger.Component{"cli": {Findings: map[string]int{"gosec": 1}}},
		Scope:      map[ledger.FindingBucket]bool{{Gate: "gosec", Component: "cli"}: true},
		Found:      []finding.Finding{a},
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
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
	dir, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	first := ledgerCommit(t, dir, "recorded")
	ledgerCommit(t, dir, "never recorded")
	head := ledgerCommit(t, dir, "the commit being recorded")

	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{Dir: dir, BranchOverride: "main", Components: ledgerScalar()})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}

	for _, tc := range []struct {
		name    string
		store   string
		wantGap *ledger.Gap
	}{
		{name: "nothing recorded yet", store: ledgerStore(t, head.At)},
		{name: "the parent recorded", store: ledgerStore(t, head.At, first.SHA, head.Parent)},
		{name: "this commit recorded", store: ledgerStore(t, head.At, head.SHA)},
		{name: "an ancestor recorded", store: ledgerStore(t, head.At, first.SHA), wantGap: &ledger.Gap{
			From: first.SHA, Missing: 1,
			Reason: "1 commit(s) between the last recorded one and this one were never recorded",
		}},
		{name: "a commit off this history recorded", store: ledgerStore(t, head.At, "0123456789abcdef0123456789abcdef01234567"), wantGap: &ledger.Gap{
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
