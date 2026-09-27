package recordstages

import (
	"context"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
)

// recordEntry is a coverage entry of covered lines out of total, measured by
// the one instrument every fixture here uses.
func recordEntry(covered, total int) gitstate.Entry {
	return gitstate.Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: "go 1.26"}
}

// recordMeasured is a Go component a run can measure.
func recordMeasured(name string) component.Component {
	return component.Component{Name: name, Dir: name, Runner: runner.GoTest, Args: []string{"./..."}}
}

// recordRemote is a repository with one commit pushed to a file:// origin —
// the remote the state branch is fetched from — and the tree it checks out.
func recordRemote(t *testing.T) (dir, tree string) {
	t.Helper()
	dir, tree = recordRepo(t, map[string]string{"README.md": "a repository\n"})
	origin := t.TempDir()
	recordGit(t, origin, "init", "--quiet", "--bare", "-b", "main", ".")
	recordGit(t, dir, "remote", "add", "origin", "file://"+origin)
	recordGit(t, dir, "push", "--quiet", "-u", "origin", "main")
	return dir, tree
}

// recordHolding is recordRemote with snap already recorded for its tree.
func recordHolding(t *testing.T, snap gitstate.Snapshot) (dir, tree string) {
	t.Helper()
	dir, tree = recordRemote(t)
	if _, err := gitstate.Write(context.Background(), dir, tree, snap, nil); err != nil {
		t.Fatalf("seeding the state branch: %v", err)
	}
	return dir, tree
}

// recordNoRestore is a RestoreToleratedDips a case with nothing on the branch
// must never reach.
func recordNoRestore(t *testing.T) RestoreToleratedDips {
	return func(gitstate.Baseline, gitstate.Baseline, float64) gitstate.Baseline {
		t.Fatal("RestoreToleratedDips: called with nothing on the branch to anchor to")
		return nil
	}
}

// recordKeepDips is a RestoreToleratedDips anchoring nothing.
func recordKeepDips(record, _ gitstate.Baseline, _ float64) gitstate.Baseline { return record }

// A fold holding no component is no baseline, and carries the fold's own
// reason for it. It is not a refusal: a run whose every suite failed still
// hands the history its test counts.
func TestDecideBaselineOfAFoldHoldingNoComponentIsNothingToRecord(t *testing.T) {
	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir:                  t.TempDir(),
		Declaration:          component.File{Components: []component.Component{recordMeasured("svc")}},
		Config:               config.Default(),
		Folded:               Measurements{Tree: "deadbeef", Reason: "the suite failed"},
		Head:                 "deadbeef",
		RestoreToleratedDips: recordNoRestore(t),
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}
	want := DecideBaselineOut{Verdict: VerdictNothingToRecord, Reason: "the suite failed"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DecideBaseline = %+v, want %+v", out, want)
	}
}

// A baseline missing a declared component gates on nothing, so the fold is
// refused and names every component it is missing — and the empty snapshot
// beside the refusal is what lets the history still land.
func TestDecideBaselineRefusesAFoldMissingADeclaredComponent(t *testing.T) {
	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir: t.TempDir(),
		Declaration: component.File{Components: []component.Component{
			recordMeasured("web"), recordMeasured("svc"), recordMeasured("api"),
		}},
		Config: config.Default(),
		Folded: Measurements{
			Tree:       "deadbeef",
			Components: map[string]Measurement{"svc": {Entry: recordEntry(1, 2)}},
			Snapshot:   gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)}},
		},
		Head:                 "deadbeef",
		RestoreToleratedDips: recordNoRestore(t),
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}
	want := DecideBaselineOut{Verdict: VerdictRefused, Missing: "api, web"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DecideBaseline = %+v, want %+v", out, want)
	}
}

// A tree the branch holds nothing for records the fold, narrowed to what the
// tree declares under both metrics, with nothing to anchor it against.
func TestDecideBaselineOfATreeTheBranchHoldsNothingForRecordsTheFold(t *testing.T) {
	dir, tree := recordRemote(t)

	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir:         dir,
		Declaration: component.File{Components: []component.Component{recordMeasured("svc")}},
		Config:      config.Default(),
		Folded: Measurements{
			Tree:       tree,
			Components: map[string]Measurement{"svc": {Entry: recordEntry(1, 2)}, "ghost": {Entry: recordEntry(3, 3)}},
			Snapshot: gitstate.Snapshot{
				Coverage: gitstate.Baseline{"svc": recordEntry(1, 2), "ghost": recordEntry(3, 3)},
				CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1, Worst: 42}, "ghost": {Above: 2}},
			},
		},
		Head:                 tree,
		RestoreToleratedDips: recordNoRestore(t),
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}
	want := DecideBaselineOut{Verdict: VerdictToRecord, Snapshot: gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1, Worst: 42}},
	}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DecideBaseline = %+v, want %+v", out, want)
	}
}

// A tree the branch already holds something for is merged onto rather than
// replaced: an entry the fold does not carry survives while the tree still
// declares it, one the tree no longer declares dies, and the fold's own
// coverage is anchored against the branch's before it lands.
func TestDecideBaselineMergesOntoWhatTheBranchHolds(t *testing.T) {
	dir, tree := recordHolding(t, gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2), "docs": recordEntry(5, 10), "gone": recordEntry(3, 3)},
		CRAP:     gitstate.CRAPBaseline{"docs": {Above: 1, Worst: 31}, "gone": {Above: 4}},
	})
	cfg := config.Default()
	cfg.Coverage.Tolerance = 0.25
	anchored := recordEntry(9, 10)
	type restoreCall struct {
		record, baseline gitstate.Baseline
		tolerance        float64
	}
	var calls []restoreCall
	restore := func(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline {
		calls = append(calls, restoreCall{record, baseline, tolerance})
		return gitstate.Baseline{"svc": anchored}
	}

	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir: dir,
		Declaration: component.File{Components: []component.Component{
			recordMeasured("svc"),
			// Declared and never measured: its absence from the fold is no gap,
			// and its entry on the branch is kept.
			{Name: "docs", Dir: "docs", Command: []string{"make", "docs"}},
		}},
		Config: cfg,
		Folded: Measurements{
			Tree:       tree,
			Components: map[string]Measurement{"svc": {Entry: recordEntry(2, 2)}},
			Snapshot: gitstate.Snapshot{
				Coverage: gitstate.Baseline{"svc": recordEntry(2, 2)},
				CRAP:     gitstate.CRAPBaseline{"svc": {Above: 0, Worst: 3}},
			},
		},
		Head:                 tree,
		RestoreToleratedDips: restore,
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}

	wantCalls := []restoreCall{{
		record:    gitstate.Baseline{"svc": recordEntry(2, 2)},
		baseline:  gitstate.Baseline{"svc": recordEntry(1, 2), "docs": recordEntry(5, 10), "gone": recordEntry(3, 3)},
		tolerance: 0.25,
	}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("RestoreToleratedDips calls = %+v, want %+v", calls, wantCalls)
	}
	want := DecideBaselineOut{Verdict: VerdictToRecord, Snapshot: gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": anchored, "docs": recordEntry(5, 10)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 0, Worst: 3}, "docs": {Above: 1, Worst: 31}},
	}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DecideBaseline = %+v, want %+v", out, want)
	}
}

// A measurement the branch already holds byte for byte is unchanged — and is
// still the snapshot handed to the write, which pushes only when the history
// beside it has something to say.
func TestDecideBaselineOfAMeasurementTheBranchAlreadyHoldsIsUnchanged(t *testing.T) {
	held := gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)}}
	dir, tree := recordHolding(t, held)

	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir:         dir,
		Declaration: component.File{Components: []component.Component{recordMeasured("svc")}},
		Config:      config.Default(),
		Folded: Measurements{
			Tree:       tree,
			Components: map[string]Measurement{"svc": {Entry: recordEntry(1, 2)}},
			Snapshot:   held,
		},
		Head:                 tree,
		RestoreToleratedDips: recordKeepDips,
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}
	want := DecideBaselineOut{Verdict: VerdictUnchanged, Snapshot: gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
		CRAP:     gitstate.CRAPBaseline{},
	}}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("DecideBaseline = %+v, want %+v", out, want)
	}
}

// Sameness is asked after the anchoring, so a re-run whose within-tolerance dip
// anchors back to what the branch holds is the same measurement, not a new one.
func TestDecideBaselineOfADipAnchoredBackToTheBranchIsUnchanged(t *testing.T) {
	held := gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": recordEntry(800, 1000)}}
	dir, tree := recordHolding(t, held)
	restore := func(_, baseline gitstate.Baseline, _ float64) gitstate.Baseline {
		return gitstate.Baseline{"svc": baseline["svc"]}
	}

	out, err := DecideBaseline(context.Background(), DecideBaselineIn{
		Dir:         dir,
		Declaration: component.File{Components: []component.Component{recordMeasured("svc")}},
		Config:      config.Default(),
		Folded: Measurements{
			Tree:       tree,
			Components: map[string]Measurement{"svc": {Entry: recordEntry(799, 1000)}},
			Snapshot:   gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": recordEntry(799, 1000)}},
		},
		Head:                 tree,
		RestoreToleratedDips: restore,
	})
	if err != nil {
		t.Fatalf("DecideBaseline: %v", err)
	}
	if out.Verdict != VerdictUnchanged {
		t.Errorf("Verdict = %v, want VerdictUnchanged", out.Verdict)
	}
	if got := out.Snapshot.Coverage["svc"]; got != recordEntry(800, 1000) {
		t.Errorf("svc = %+v, want the anchored entry the branch holds", got)
	}
}

// No decision is not a decision to record.
func TestTheZeroVerdictIsNoVerdict(t *testing.T) {
	var v Verdict
	for _, decided := range []Verdict{VerdictToRecord, VerdictUnchanged, VerdictRefused, VerdictNothingToRecord} {
		if v == decided {
			t.Errorf("the zero Verdict is %v", decided)
		}
	}
}

// A baseline missing a declared component gates on nothing, and the fold is
// where a sharded run's completeness can be judged at all.
func TestMissingFromRecordNamesADeclaredComponentTheFoldLacks(t *testing.T) {
	decl := component.File{Components: []component.Component{recordMeasured("svc")}}
	full := Measurements{Components: map[string]Measurement{"svc": {Entry: recordEntry(2, 4)}}}
	if gap, blocked := MissingFromRecord(decl, full); blocked {
		t.Errorf("a complete fold was blocked by %q", gap)
	}
	empty := Measurements{Components: map[string]Measurement{}}
	gap, blocked := MissingFromRecord(decl, empty)
	if !blocked || gap != "svc" {
		t.Errorf("MissingFromRecord = (%q, %v), want svc named as the gap", gap, blocked)
	}
}

// Every gap is named, sorted, in one string.
func TestMissingFromRecordNamesEveryGapInOrder(t *testing.T) {
	decl := component.File{Components: []component.Component{
		recordMeasured("web"), recordMeasured("svc"), recordMeasured("api"),
	}}
	gap, blocked := MissingFromRecord(decl, Measurements{Components: map[string]Measurement{"svc": {}}})
	if !blocked || gap != "api, web" {
		t.Errorf("MissingFromRecord = (%q, %v), want (%q, true)", gap, blocked, "api, web")
	}
}

// A component nothing could ever measure is not a gap. Its absence is
// permanent and expected rather than something one run created, so it must not
// block every recording forever.
func TestMissingFromRecordIsNotBlockedByAComponentNothingCanMeasure(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "docs", Dir: "docs", Command: []string{"make", "docs"}},
	}}
	if gap, blocked := MissingFromRecord(decl, Measurements{}); blocked {
		t.Errorf("a component declaring a raw command blocked recording as %q", gap)
	}
}

func TestUnmeasurableByDeclarationReadsTheDeclarationAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    component.Component
		want bool
	}{
		{"a raw command", component.Component{Name: "docs", Command: []string{"make", "docs"}}, true},
		{"a runner lydite does not know", component.Component{Name: "odd", Runner: "no-such-runner"}, true},
		{"a runner that writes a coverage report", recordMeasured("svc"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnmeasurableByDeclaration(tc.c); got != tc.want {
				t.Errorf("UnmeasurableByDeclaration = %v, want %v", got, tc.want)
			}
		})
	}
}

// A component the declaration no longer holds does not survive in the entry.
// Otherwise a baseline accumulates a tail of components nobody can measure,
// and each one blocks nothing while quietly widening what a composed figure
// sums.
func TestDeclaresNamesOnlyADeclaredComponent(t *testing.T) {
	decl := component.File{Components: []component.Component{{Name: "svc", Dir: "svc", Runner: "go-test"}}}
	if !Declares(decl, "svc") {
		t.Error("Declares says a declared component is absent")
	}
	if Declares(decl, "gone") {
		t.Error("Declares says an undeclared component is present")
	}
}

func TestDeclaredOnlyNarrowsBothMetricsToTheDeclaration(t *testing.T) {
	decl := component.File{Components: []component.Component{recordMeasured("svc")}}
	got := declaredOnly(decl, gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2), "gone": recordEntry(3, 4)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}, "gone": {Above: 2}},
	})
	want := gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declaredOnly = %+v, want %+v", got, want)
	}
	// Both maps exist whatever was narrowed, so a merge can write into them.
	if empty := declaredOnly(decl, gitstate.Snapshot{}); empty.Coverage == nil || empty.CRAP == nil {
		t.Errorf("declaredOnly of nothing = %+v, want two empty maps", empty)
	}
}

// A read that fails is nothing to merge onto, never an error.
func TestExistingSnapshotOfAnUnreadableBranchIsEmpty(t *testing.T) {
	if got := existingSnapshot(context.Background(), t.TempDir(), "deadbeef"); got.Recorded() {
		t.Errorf("existingSnapshot = %+v, want nothing from a directory that is no repository", got)
	}
}

func TestExistingSnapshotReadsWhatTheBranchHoldsForTheTree(t *testing.T) {
	held := gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1, Worst: 31}},
	}
	dir, tree := recordHolding(t, held)
	if got := existingSnapshot(context.Background(), dir, tree); !reflect.DeepEqual(got, held) {
		t.Errorf("existingSnapshot = %+v, want %+v", got, held)
	}
}

func TestSameSnapshotComparesEveryMetric(t *testing.T) {
	base := gitstate.Snapshot{
		Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
		CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
	}
	for _, tc := range []struct {
		name  string
		other gitstate.Snapshot
		want  bool
	}{
		{"the same entries", gitstate.Snapshot{
			Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
			CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
		}, true},
		{"a different coverage entry", gitstate.Snapshot{
			Coverage: gitstate.Baseline{"svc": recordEntry(2, 2)},
			CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
		}, false},
		{"a different CRAP entry", gitstate.Snapshot{
			Coverage: gitstate.Baseline{"svc": recordEntry(1, 2)},
			CRAP:     gitstate.CRAPBaseline{"svc": {Above: 2}},
		}, false},
		{"another component", gitstate.Snapshot{
			Coverage: gitstate.Baseline{"api": recordEntry(1, 2)},
			CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
		}, false},
		{"an extra component", gitstate.Snapshot{
			Coverage: gitstate.Baseline{"svc": recordEntry(1, 2), "api": recordEntry(1, 2)},
			CRAP:     gitstate.CRAPBaseline{"svc": {Above: 1}},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameSnapshot(base, tc.other); got != tc.want {
				t.Errorf("sameSnapshot = %v, want %v", got, tc.want)
			}
		})
	}
	// An absent metric and an empty one hold the same entries: none.
	if !sameSnapshot(gitstate.Snapshot{}, gitstate.Snapshot{Coverage: gitstate.Baseline{}, CRAP: gitstate.CRAPBaseline{}}) {
		t.Error("sameSnapshot says a nil metric differs from an empty one")
	}
}
