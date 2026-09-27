package teststages

import (
	"context"
	"fmt"
	"io"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	testmeasure "lydite/lydite/internal/test/measure"
)

// Candidate is what this run would record as the baseline for the tree it
// measured, and the words the record row shows once it is saved.
//
// It is the parts of the document `lydite test record` lands and `lydite test
// merge` composes from, rather than that document: the command saves it, and
// only the command knows whether the save succeeded, which is what the record
// row finally says.
//
// A Candidate whose Reason is set establishes nothing, and its document names
// only Tree, Reason and Tests. Any other is the whole document: Record, what
// was measured before a tolerated dip was anchored, which entries were carried
// rather than measured, the CRAP scores, the baseline each entry was gated
// against, the patch counts and the test counts.
type Candidate struct {
	// Tree is the tree the measurements describe, which binds the document to
	// the checkout it may be recorded from. Empty only when HEAD's tree would
	// not resolve.
	Tree string
	// Reason says why there is nothing to record, and is empty exactly when
	// there is something.
	Reason string
	// Record is what would be recorded: every measured entry, and every
	// carried one, with a within-tolerance dip anchored back to the baseline.
	Record gitstate.Baseline
	// Measured is Record before the anchoring — what the repository-wide
	// figures sum.
	Measured gitstate.Baseline
	// Carried marks the entries inherited from the base tree because
	// affected selection did not run their component.
	Carried map[string]bool
	// Scores is the CRAP baseline beside Record.
	Scores gitstate.CRAPBaseline
	// GatedAgainst is the baseline this run compared with, nil where HEAD is
	// its own merge-base. It is not the anchor: on the default branch the
	// anchor is the previous commit's entry, which gates nothing, and stored
	// as a comparison it would have the fold publish a verdict against a tree
	// that is not any merge-base.
	GatedAgainst gitstate.Baseline
	// Gated says this run compared against a baseline at all, which is not
	// the same as any component having found an entry: a first adoption
	// compares every component against nothing and must still fold as a
	// gated run.
	Gated bool
	// Parts is each component's contribution to patch(repo).
	Parts []testmeasure.PatchPart
	// Tests is what became of each component's suite.
	Tests map[string]junit.Counts
	// Value is what the record row says once the candidate is saved.
	Value string
}

// candidateThisTree is what this run would record as the baseline for the tree
// it measured.
//
// On a pull request HEAD is the merged result, and a squash merge lands a
// commit carrying exactly that tree — so this is already the baseline for the
// commit this change is about to become, measured by the pipeline that knows
// how to measure it. That is what removes the obligation to run on the default
// branch, an obligation a repository running CI only on pull requests never
// meets.
//
// Nothing here writes to the lydite branch. Measuring runs each component's
// suite and any setup/teardown shell the declaration carries, so on a pull
// request it runs the pull request's own code; a token that can push has no
// business in that job. `lydite test record` lands the document, executing
// none of it.
//
// anchor is the baseline a within-tolerance dip is restored against, and
// gatedAgainst the one this run actually compared with — the same snapshot on
// the gated path, and empty where HEAD is its own merge-base.
func candidateThisTree(ctx context.Context, w io.Writer, dir string, decl component.File, ms []testmeasure.Measurement, anchor, gatedAgainst gitstate.Snapshot, gated bool, parts []testmeasure.PatchPart, tolerance float64) Candidate {
	declared := make(map[string]bool, len(decl.Components))
	for _, c := range decl.Components {
		declared[c.Name] = true
	}
	carried := map[string]bool{}
	record := gitstate.Baseline{}
	for _, m := range ms {
		if m.Measured() {
			record[m.Name] = m.Entry()
			continue
		}
		// A component this run did not select keeps the entry the base tree
		// had, because it is unchanged from it. Recording only what was
		// measured would drop it, and every later change would see it as new
		// and gate it against nothing — permanently, since each run would
		// drop it again.
		//
		// Only a component this run did not select. One that ran and failed
		// may be exactly what changed, so recording its old entry under this
		// tree would attribute a number to content that never produced it.
		//
		// A component the base tree had and this tree does not declare is
		// gone: it is not in ms at all, so its entry dies with it rather than
		// leaving the baseline a tail of components nobody can measure.
		if lines, ok := anchor.Coverage[m.Name]; ok && m.Carryable && lines.Measured() && declared[m.Name] {
			record[m.Name] = lines
			carried[m.Name] = true
		}
	}
	if len(record) == 0 {
		return reasonOnly(ctx, w, dir, "nothing to record", "no component produced a measurement", ms)
	}
	scores := testmeasure.CRAPRecord(ms, record, carried, anchor.CRAP)
	// Held before the anchoring, because the two are different quantities: the
	// anchored entry is what gets recorded, and what was measured is what the
	// repository-wide figures sum.
	measured := record
	record = testmeasure.WithToleratedDipsRestored(record, anchor.Coverage, tolerance)
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD")
	if err != nil {
		_, _ = fmt.Fprintf(w, "warning: could not resolve this tree, so its coverage was not recorded: %v\n", err)
		return Candidate{Reason: "this tree could not be resolved", Value: "not recorded — this tree could not be resolved"}
	}
	candidate := Candidate{
		Tree:         tree,
		Record:       record,
		Measured:     measured,
		Carried:      carried,
		Scores:       scores,
		GatedAgainst: gatedAgainst.Coverage,
		Gated:        gated,
		Parts:        parts,
		Tests:        testmeasure.TestCounts(w, ms),
	}

	// A run that could not measure a component it was supposed to has not
	// established this tree's baseline, and recording a partial one is worse
	// than recording none: a composed figure refuses to compare unless the
	// baseline covers every component in it, so the missing component's
	// language row and the global row would stop gating — silently, and with
	// no way to notice.
	//
	// The refusal travels in the record row, on a candidate that still carries
	// every component this run did measure. Emptying it instead loses those
	// counts for good: the document is the only channel by which a shard's
	// numbers reach `lydite test merge`, so one component's unreadable report
	// would drop every other component of that shard out of coverage(repo) —
	// which is then composed over a strict subset and rendered as a pass. What
	// must not be recorded is decided by `lydite test record`, against the
	// declaration of the tree being recorded, and it refuses this document by
	// name.
	//
	// A component nothing could ever measure does not block it. It contributes
	// to neither side of any comparison, so its absence from the baseline is
	// permanent and expected rather than a gap this run created.
	if gap, blocked := testmeasure.RecordingBlockedBy(ms, record); blocked {
		_, _ = fmt.Fprintf(w,
			"warning: component %q has no coverage to record (%s), so this tree's coverage was not recorded — the next change against it measures the tree instead of gating against a baseline missing a component\n",
			gap.Name, gap.Why)
		candidate.Value = "not recorded — " + fmt.Sprintf(
			"%s has no coverage to record, and a baseline missing a component gates on nothing", gap.Name)
		return candidate
	}
	// Against the declaration, not against what this run was responsible for.
	// A document covering part of the tree is expected — a shard covers its
	// slice, and completeness is the fold's question — but a row saying only
	// how many entries it holds announces a recording `lydite test record`
	// may then refuse. The two numbers differ exactly when the fold has
	// something left to decide.
	candidate.Value = fmt.Sprintf("%d of %d component(s) ready for %s — `lydite test record` lands it",
		len(record), testmeasure.Recordable(decl), shortSHA(tree))
	return candidate
}

// reasonOnly is a candidate that establishes nothing, and why.
//
// It still names the tree it was taken on. `record` binds a candidate to the
// checkout it may be landed from and refuses one that names no tree at all, so
// a reason-carrying document without it is unreadable — and the command that
// would have shown the reason fails with "no candidate found" instead. A tree
// that will not resolve leaves it empty, the one case where there is genuinely
// nothing to bind to.
//
// It still carries the test counts, because a run in which every suite failed
// is precisely the run that establishes no baseline and has the counts a
// history most wants.
func reasonOnly(ctx context.Context, w io.Writer, dir, prefix, reason string, ms []testmeasure.Measurement) Candidate {
	tree, _ := gitstate.TreeSHA(ctx, dir, "HEAD")
	return Candidate{Tree: tree, Reason: reason, Tests: testmeasure.TestCounts(w, ms), Value: prefix + " — " + reason}
}
