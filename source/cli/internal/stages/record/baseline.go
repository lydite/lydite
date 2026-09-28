package recordstages

import (
	"context"
	"sort"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
)

// RestoreToleratedDips returns record with every component whose coverage
// dipped below its baseline by no more than the tolerance restored to the
// baseline's percentage. It is `lydite test`'s own anchoring, handed in so that
// the rule a run gates by and the rule a recording lands by are one rule.
type RestoreToleratedDips func(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline

// Verdict is what DecideBaseline makes of a fold. The zero value is no verdict
// at all, so an outcome nothing decided is never read as one to record.
type Verdict int

const (
	// VerdictToRecord is a baseline that differs from what the branch holds
	// for this tree, and is landed.
	VerdictToRecord Verdict = iota + 1
	// VerdictUnchanged is a baseline the branch already holds byte for byte.
	VerdictUnchanged
	// VerdictRefused is a fold missing a declared component, which becomes no
	// baseline at all.
	VerdictRefused
	// VerdictNothingToRecord is a fold holding no component, which becomes no
	// baseline at all.
	VerdictNothingToRecord
)

// DecideBaselineIn is the fold, and the tree it is bound to.
type DecideBaselineIn struct {
	// Dir is the checkout being recorded.
	Dir string
	// Declaration and Config are LoadDeclarationOut's.
	Declaration component.File
	Config      config.Config
	// Folded is FoldMeasurementsOut.Folded.
	Folded Measurements
	// Head is BindTreeOut.Head, the tree the baseline is keyed by.
	Head                 string
	RestoreToleratedDips RestoreToleratedDips
}

// DecideBaselineOut is what the fold may be recorded as, and why.
type DecideBaselineOut struct {
	Verdict Verdict
	// Snapshot is what the write lands for this tree: the fold merged onto
	// whatever the branch already holds, for VerdictToRecord and
	// VerdictUnchanged, and empty for every other verdict.
	Snapshot gitstate.Snapshot
	// Missing names every declared component the fold has no entry for,
	// comma-separated. Set for VerdictRefused alone.
	Missing string
	// Reason is the fold's own statement of why it holds no component. Set
	// for VerdictNothingToRecord alone, and written by a run the recording
	// did not perform, so it may carry anything that run's inputs carried.
	Reason string
}

// DecideBaseline is what the fold may be recorded as, merged onto whatever the
// tree already holds — and, when it may not be recorded at all, why, beside an
// empty snapshot.
//
// A refusal is a verdict rather than an error because the recording reached an
// answer: the documents were read and something about them says they must not
// become a baseline. The quality history is decided independently of it. The
// two are different policies over one branch: a baseline is a cache, refused
// whenever it would be partial, because a partial one reads as a cache hit and
// gates every later change on nothing, while a record is not recomputable at
// all and is appended whether or not the run adds up to a baseline.
//
// It writes nothing. Whether there is a baseline to land and whether there is
// history to append are separate questions with separate answers, and the one
// write after both is what makes them one commit — an empty snapshot is a
// legitimate answer here, and is what lets a refusal and an append land
// through it.
func DecideBaseline(ctx context.Context, in DecideBaselineIn) (DecideBaselineOut, error) {
	if len(in.Folded.Components) == 0 {
		// A fold with no component may still hold test counts: a run whose
		// every suite failed establishes no baseline by construction and knows
		// exactly how many tests went red, which is the data point a history
		// most wants and the one nothing can recover later.
		return DecideBaselineOut{Verdict: VerdictNothingToRecord, Reason: in.Folded.Reason}, nil
	}

	if gap, blocked := MissingFromRecord(in.Declaration, in.Folded); blocked {
		return DecideBaselineOut{Verdict: VerdictRefused, Missing: gap}, nil
	}

	// The declaration bounds what may be recorded, on both sides of the merge
	// below. The fold is written by runs the recording did not perform, so a
	// name in it that the tree does not declare is a component nothing can
	// ever measure again — the same thing an entry left behind by a deleted
	// component is, and it is dropped for the same reason.
	record := declaredOnly(in.Declaration, in.Folded.Snapshot)

	// Merged onto whatever this tree already holds, never skipped because it
	// holds something: a re-run that measured more than the last one must not
	// be refused for finding an entry there.
	existing := existingSnapshot(ctx, in.Dir, in.Head)
	if !existing.Recorded() {
		return DecideBaselineOut{Verdict: VerdictToRecord, Snapshot: record}, nil
	}
	// Anchored against what this tree already holds, not only against what
	// the measuring run compared with. The same tree is the same content, so
	// a difference between two measurements of it is the measurement noise
	// the tolerance exists for — and without this a recording that
	// re-measures a tree an earlier one already recorded replaces the
	// anchored high-water entry with a raw dipped one, handing the next
	// change a lowered number to gate against. That is the per-merge ratchet
	// RestoreToleratedDips exists to prevent.
	//
	// No CRAP equivalent, and none is wanted: the score is a count of
	// functions, so two measurements of one tree differ only if what is
	// measured changed. There is no sub-tenth noise for a tolerance to
	// absorb, and a tolerance over an integer count would be a free function
	// above the threshold per merge.
	record.Coverage = in.RestoreToleratedDips(record.Coverage, existing.Coverage, in.Config.Coverage.Tolerance)
	merged := declaredOnly(in.Declaration, existing)
	for name, e := range record.Coverage {
		merged.Coverage[name] = e
	}
	for name, e := range record.CRAP {
		merged.CRAP[name] = e
	}
	if sameSnapshot(merged, existing) {
		// The same bytes as the branch already holds. It is still handed to
		// the write, which stages it, finds nothing changed and pushes only if
		// the history beside it did — so a recording that adds a record to an
		// already-recorded tree is one commit carrying only the record.
		return DecideBaselineOut{Verdict: VerdictUnchanged, Snapshot: merged}, nil
	}
	return DecideBaselineOut{Verdict: VerdictToRecord, Snapshot: merged}, nil
}

// MissingFromRecord names a declared component the fold has no entry for.
//
// The same rule a single run applies to what it would record, asked again here
// because a sharded run's completeness is a property of the fold and of no
// document in it: each shard is missing most components until they are put
// together.
//
// A component nothing could ever measure is not a gap — a raw `command:`, or a
// runner whose instrumented variant names no report — and it is recognised the
// same way `lydite test` recognises it, from the declaration alone.
func MissingFromRecord(decl component.File, folded Measurements) (string, bool) {
	var gaps []string
	for _, c := range decl.Components {
		if _, ok := folded.Components[c.Name]; ok {
			continue
		}
		if UnmeasurableByDeclaration(c) {
			continue
		}
		gaps = append(gaps, c.Name)
	}
	if len(gaps) == 0 {
		return "", false
	}
	sort.Strings(gaps)
	return strings.Join(gaps, ", "), true
}

// UnmeasurableByDeclaration reports whether no run could ever measure this
// component, from what it declares and nothing else — which is all a
// recording has, since it runs none of them.
func UnmeasurableByDeclaration(c component.Component) bool {
	if len(c.Command) > 0 {
		return true
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return true
	}
	inv, ok := r.Build(runner.Instrumented, c.Args)
	return !ok || inv.CoverageReport == ""
}

// declaredOnly is a baseline narrowed to the components the tree declares.
//
// It is applied to everything that reaches the branch, so the declaration read
// from the recorded tree is the whole of what may appear under that tree's
// key. A component the declaration no longer holds dies with it rather than
// leaving a tail of entries nobody can measure, and a name a document
// invented never arrives.
func declaredOnly(decl component.File, snap gitstate.Snapshot) gitstate.Snapshot {
	out := gitstate.Snapshot{
		Coverage: make(gitstate.Baseline, len(snap.Coverage)),
		CRAP:     make(gitstate.CRAPBaseline, len(snap.CRAP)),
	}
	for name, e := range snap.Coverage {
		if Declares(decl, name) {
			out.Coverage[name] = e
		}
	}
	for name, e := range snap.CRAP {
		if Declares(decl, name) {
			out.CRAP[name] = e
		}
	}
	return out
}

// Declares reports whether the declaration still holds a component by name.
func Declares(decl component.File, name string) bool {
	for _, c := range decl.Components {
		if c.Name == name {
			return true
		}
	}
	return false
}

// existingSnapshot is what the branch already holds for this tree, across every
// metric, so a recording merges onto it rather than replacing it.
//
// A read that fails is an empty snapshot rather than an error: the question is
// only whether there is something to merge onto, and a run that cannot answer
// it records what it measured — which is the state the tree would have been
// left in had nothing been there.
func existingSnapshot(ctx context.Context, dir, tree string) gitstate.Snapshot {
	snap, err := gitstate.ReadSnapshot(ctx, dir, tree)
	if err != nil {
		return gitstate.Snapshot{}
	}
	return snap
}

// sameSnapshot reports whether two snapshots hold the same entries under every
// metric, so a run that would rewrite a tree's state byte for byte does not
// push to do it.
func sameSnapshot(a, b gitstate.Snapshot) bool {
	return sameEntries(a.Coverage, b.Coverage) && sameEntries(a.CRAP, b.CRAP)
}

// sameEntries reports whether two of one metric's baselines hold the same
// entries.
func sameEntries[M ~map[string]E, E comparable](a, b M) bool {
	if len(a) != len(b) {
		return false
	}
	for name, entry := range a {
		if other, ok := b[name]; !ok || other != entry {
			return false
		}
	}
	return true
}
