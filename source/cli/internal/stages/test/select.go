package teststages

import (
	"context"
	"fmt"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/gitstate"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// SelectAffectedIn is the responsibility set, and whether and against what it
// is narrowed.
type SelectAffectedIn struct {
	Dir  string
	Decl component.File
	Own  []component.Component
	// Affected is `--affected`: of Own, run only what the change against the
	// merge-base could have broken.
	Affected bool
	// BaseBranch is the `--base-branch` override, empty to discover it.
	BaseBranch string
}

// SelectAffectedOut is what runs, and the rows selection contributes.
type SelectAffectedOut struct {
	// Selected is what runs: Own, or the part of it selection chose.
	Selected []component.Component
	// Ordered and Skipped are empty unless selection ran. Skipped holds the
	// test row of each component in Own that selection left out, keyed by
	// name, and Ordered is the declaration those rows interleave into — with
	// nothing skipped, the order of Selected is already the order of Own.
	Ordered []component.Component
	Skipped map[string]ui.Row
	// Rows is the select row when selection ran, and empty otherwise.
	Rows []ui.Row
}

// SelectAffected narrows the run to what the change could have broken, when
// asked to.
//
// Nothing to select from is not the same as a change that selected nothing,
// and selection is not asked which it is. Running it over an empty declaration
// would render a select row claiming the diff was empty beside a row saying no
// components are declared — two rows contradicting each other — and would pay
// a git fetch to do it, which on a shallow or fork checkout turns a report that
// renders into a hard error before any row is written.
//
// Selection is computed over the whole declaration and intersected with Own
// afterwards, so the select row says the same thing in every shard. Computing
// it over the slice would give each shard its own counts and reasons, and the
// fold has no way to tell that from shards that saw different trees.
func SelectAffected(ctx context.Context, in SelectAffectedIn) (SelectAffectedOut, error) {
	if !in.Affected || len(in.Decl.Components) == 0 {
		return SelectAffectedOut{Selected: in.Own}, nil
	}
	res, err := selectAffected(ctx, in.Dir, in.Decl, in.BaseBranch)
	if err != nil {
		return SelectAffectedOut{}, err
	}
	// The same label shape a suite row takes, so every component produces
	// exactly one test(<name>) row whether it ran or not. They are handed to
	// the run rather than reported here so they interleave into declaration
	// order: reported here they would all precede the suites, and a
	// declaration of a, b, c with only b affected would document b last.
	//
	// Scoped to Own, so `test(a): not affected` is emitted by the one shard
	// that owns a. A component declaring no suite takes the row its
	// declaration gives it whether or not selection reached it, so the row
	// reads the same here as in a fold, where no shard ran it.
	skipped := make(map[string]ui.Row, len(res.Skipped))
	for _, c := range testrun.Intersect(res.Skipped, in.Own) {
		if testrun.DeclaresNoSuite(c) {
			skipped[c.Name] = testrun.NoSuiteTestRow(kind, c.Name)
			continue
		}
		skipped[c.Name] = ui.Row{Status: ui.StatusUnmeasured, Label: testrun.TestLabel(c.Name), Value: "not affected"}
	}
	return SelectAffectedOut{
		Selected: testrun.Intersect(res.Selected, in.Own),
		Ordered:  in.Own,
		Skipped:  skipped,
		Rows:     []ui.Row{testrun.SelectRow(res, len(in.Decl.Components))},
	}, nil
}

// selectAffected is the selection against the merge-base with the base branch.
//
// An unresolvable merge-base is an error rather than a fallback, in either
// direction. Falling back to nothing is the failure selection exists to avoid;
// falling back to everything is safe but makes the optimisation stop happening
// with no symptom other than a slow job, which is how a shallow checkout goes
// unnoticed for months. `lydite scan --diff-base auto` refuses for the same
// reason, and a shallow checkout is a fixable misconfiguration.
func selectAffected(ctx context.Context, dir string, file component.File, baseBranch string) (affected.Result, error) {
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		// The base branch is resolved inside, so an undiscoverable one
		// already arrives here as an error naming --base-branch. What is
		// left to say is the other cause, which produces the same
		// merge-base failure from a branch that resolved perfectly.
		return affected.Result{}, fmt.Errorf("--affected needs the merge-base with the base branch, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	return testrun.AffectedFrom(ctx, dir, file, base)
}
