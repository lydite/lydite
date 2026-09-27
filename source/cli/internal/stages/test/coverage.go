package teststages

import (
	"context"
	"fmt"
	"io"
	"strings"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	testmeasure "lydite/lydite/internal/test/measure"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// CoverageIn is what was measured, and what the command decided about
// coverage before anything ran, so the gate does not re-derive it.
type CoverageIn struct {
	Dir  string
	Decl component.File
	// Own is what this run is responsible for. The gate reports one row per
	// component in it and nothing at all about any other, so every declared
	// component appears exactly once across a matrix of shards.
	Own          []component.Component
	Measurements []testmeasure.Measurement
	Config       config.Config
	// Instrument says the run took the instrumented variant. Off means no
	// coverage row is reported at all: a row saying `unmeasured` on every fast
	// local run trains readers to ignore the tag that exists to be noticed.
	Instrument bool
	// Gate is `--gate-coverage`: read the baseline, compare against it and
	// propose this tree's. Explicit, because measuring is local and gating
	// reads a shared branch — and every signal for "am I in CI" is unreliable
	// where lydite runs.
	Gate bool
	// BaseBranch is the `--base-branch` override, empty to discover it.
	BaseBranch string
	// Concurrency bounds a base-tree measurement exactly as the run it is
	// compared with was bounded.
	Concurrency int
	// Affected says the run narrowed by affected selection, the only
	// narrowing that licenses carrying an unmeasured component's baseline
	// forward: selection determined the change could not have broken it, so
	// it is unchanged from the tree that entry describes. `--component`
	// narrows for a different reason — the caller wanted these components run
	// — and says nothing about the others.
	Affected bool
	// Narrowed says `--component` was passed, so no figure over the
	// repository is answerable here.
	Narrowed bool
	// Logs opens each component's log when a base tree is measured.
	Logs Logs
	// Stderr is where every warning the gate raises goes, and the base tree's
	// toolchain provisioning with them.
	Stderr io.Writer
}

// CoverageOut is the coverage and CRAP section of the report, and the
// candidate baseline the run proposes.
type CoverageOut struct {
	// Rows is every coverage, patch, CRAP, floor and baseline row, in report
	// order. The record row, when there is one, follows the last of them.
	Rows []ui.Row
	// Findings is what the gated comparison found: each component's uncovered
	// changed lines, and each function over the CRAP threshold. Nil for a run
	// that compared nothing.
	Findings []finding.Finding
	// Candidate is what this run would record, nil when it compared nothing
	// or could not gate. Saving it is the command's, and what came of saving
	// it is the record row.
	Candidate *Candidate
	// NoSuiteRows are the coverage rows of each component in Own that
	// declares no suite, and follow the record row.
	NoSuiteRows []ui.Row
}

// Coverage reports — and, when asked, gates — this run's coverage and CRAP.
//
// A component declaring no suite is kept out of the measured set rather than
// handed to it as unmeasurable: a measurement with no language renders its
// complexity row as a raw command's, naming a command this component does not
// declare. It contributes to no composed figure either way — nothing could
// ever measure it, so it sits on neither side of any comparison. Its rows
// follow only a run that instruments, for the reason a suite's do.
//
// CRAP is reported here rather than in a stage of its own. It is computed from
// the coverage report, and each component's CRAP row sits beside its coverage
// row: a reader asking about one component should not have to pair rows
// separated by every other component's.
//
// It never returns an error. Everything that can go wrong here goes wrong
// after every suite has already run, so an error would discard a report that
// may have taken twenty minutes to produce — for a failure about the gate
// rather than about the code. A gate asked for and not run is a failing row
// instead.
func Coverage(ctx context.Context, in CoverageIn) (CoverageOut, error) {
	var out CoverageOut
	if len(in.Decl.Components) == 0 {
		return out, nil
	}
	if suites := withSuites(in.Own); len(suites) > 0 {
		out = coverageRows(ctx, in, suites)
	}
	if !in.Instrument {
		return out, nil
	}
	for _, c := range in.Own {
		if testrun.DeclaresNoSuite(c) {
			out.NoSuiteRows = append(out.NoSuiteRows, testrun.NoSuiteCoverageRows(c.Name, in.Config.Coverage.Floor)...)
		}
	}
	return out, nil
}

// withSuites is the components that declare a suite, in the order given.
func withSuites(cs []component.Component) []component.Component {
	out := make([]component.Component, 0, len(cs))
	for _, c := range cs {
		if !testrun.DeclaresNoSuite(c) {
			out = append(out, c)
		}
	}
	return out
}

// coverageRows is the section over own, the components that declare a suite.
//
// Three altitudes, all of them from the same per-component counts: the
// component, its language, and the repository. They cannot disagree, because
// each is a sum over a subset of one stored quantity rather than a separately
// computed number.
//
// A run that measured but did not gate says so. Every ungated row carries a
// status that is not a pass, because a workflow that forgot the flag would
// otherwise report exactly the green a gated run does — the failure
// wardnet/wardnet#957 shipped, where the patch gate never ran and the pull
// request comment read as though it had.
func coverageRows(ctx context.Context, in CoverageIn, own []component.Component) CoverageOut {
	if !in.Instrument {
		return CoverageOut{}
	}
	w := orDiscard(in.Stderr)
	floor, composed := in.Config.Coverage.Floor, !in.Narrowed
	ordered := inDeclarationOrder(own, in.Measurements, in.Affected)
	nameUnusedDeclarations(w, ordered)
	if !in.Gate {
		rows := ungatedRows(ordered, composed)
		rows = append(rows, ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value: "not read — pass --gate-coverage to compare against it"})
		return CoverageOut{Rows: append(rows, testmeasure.FloorRows(ordered, floor, composed)...)}
	}
	// Gated rows are kept only once the gate has succeeded. It can fail after
	// most of them are built — a failing `git diff` inside patchRows is the
	// live path — and keeping them would then put two rows per component under
	// one label, with contradictory statuses. A consumer keying rows by label
	// silently picks one of two answers.
	gated, err := gatedRows(ctx, w, in, ordered)
	if err != nil {
		// The measurements are still worth showing: they are what a reader
		// needs in order to act on the gate that could not run.
		rows := ungatedRows(ordered, composed)
		rows = append(rows, ui.Row{Status: ui.StatusFail, Label: "baseline",
			Value: "not gated", Detail: strings.Split(err.Error(), "\n")})
		return CoverageOut{Rows: append(rows, testmeasure.FloorRows(ordered, floor, composed)...)}
	}
	return gated
}

// nameUnusedDeclarations says which `[lydite:exclude_from_crap]` and
// `[lydite:exclude_from_coverage]` declarations documented no function.
//
// Both gates, in one place, because both are read off the one report this run
// produced and a reader fixing a misplaced declaration does not care which gate
// it named. Each is warned about by the gate it belongs to and only there:
// internal/crap holds the CRAP declarations to covering a function and leaves
// the coverage ones alone, so one typo is reported once.
//
// Named rather than dropped, for the reason a mutation declaration covering no
// mutant is named: its author believes they have answered a finding, and
// nothing they can see says otherwise. The commonest cause is one written
// inside a body, where it reads perfectly and does nothing.
//
// Here rather than where the score is taken, because a base tree is measured
// through that same path and its report is discarded — so a declaration in a
// tree nobody is looking at is never reported as this run's.
func nameUnusedDeclarations(w io.Writer, ms []testmeasure.Measurement) {
	for _, m := range ms {
		for _, where := range m.CRAP.Unused {
			_, _ = fmt.Fprintf(w, "warning: %s: %s covers no function, so nothing is excluded by it\n",
				where, annotation.Marker(annotation.CRAP))
		}
		for _, where := range m.Unused {
			_, _ = fmt.Fprintf(w, "warning: %s: %s covers no function, so nothing is excluded by it\n",
				where, annotation.Marker(annotation.Coverage))
		}
	}
}

// inDeclarationOrder returns one measurement per component in own, in the
// order the file declares them.
//
// Every component in that set, including one this invocation never selected:
// omitting the rest would make a narrowed run indistinguishable from a
// complete one. Nothing outside it, because a shard that padded a row for
// every *declared* component publishes rows about components other shards are
// running. Declaration order rather than completion order, so two runs of one
// declaration produce the same document.
func inDeclarationOrder(own []component.Component, ms []testmeasure.Measurement, selected bool) []testmeasure.Measurement {
	byName := make(map[string]testmeasure.Measurement, len(ms))
	for _, m := range ms {
		byName[m.Name] = m
	}
	out := make([]testmeasure.Measurement, 0, len(own))
	for _, c := range own {
		if m, ok := byName[c.Name]; ok {
			out = append(out, m)
			continue
		}
		// The only place a carryable measurement is made, and only when
		// affected selection is what left the component out. Everything else
		// here describes a component this run actually reached.
		m := testmeasure.UnmeasuredComponent(c, "the component was not selected for this run")
		m.Carryable = selected
		m.Unselected = true
		out = append(out, m)
	}
	return out
}

// ungatedRows reports what was measured, and that nothing was compared.
//
// StatusContext and never StatusPass: nothing was gated, so a run that
// measured 40% would otherwise render the same glyph as one that measured 95%,
// and a workflow missing --gate-coverage would report the green a gated run
// reports.
//
// Only a run responsible for the whole declaration answers about the
// repository, and with no carried entries: these are the measurements as
// taken, so a component this run did not select is unmeasured here rather
// than standing in for the baseline's counts.
func ungatedRows(ms []testmeasure.Measurement, composed bool) []ui.Row {
	rows := ungatedComponentRows(ms)
	if composed {
		rows = append(rows, testmeasure.UngatedComposedRow(testmeasure.RepoLabel("coverage"), ms, nil, testmeasure.Everything))
		if row, ok := testmeasure.CRAPSummaryOf(ms, nil, nil); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// ungatedComponentRows is the per-component half, shared by the two runs that
// compare nothing: one that was never asked to, and one on the default branch
// where HEAD is its own merge-base.
//
// Each component's CRAP row sits beside its coverage row rather than in a
// block of its own, because the two are one question about one component.
func ungatedComponentRows(ms []testmeasure.Measurement) []ui.Row {
	rows := make([]ui.Row, 0, 2*len(ms))
	for _, m := range ms {
		if !m.Measured() {
			rows = append(rows, testmeasure.UnmeasuredRow("coverage("+m.Name+")", m.Why))
		} else {
			rows = append(rows, ui.Row{Status: ui.StatusContext, Label: "coverage(" + m.Name + ")", Value: testmeasure.LineValue(m.Lines)})
		}
		row, _ := testmeasure.CRAPRow(m, nil, false)
		rows = append(rows, row)
	}
	return rows
}

// gatedRows reads the baseline, compares every altitude against it, gates the
// patch and the floor, and proposes this tree's measurement.
func gatedRows(ctx context.Context, w io.Writer, in CoverageIn, ms []testmeasure.Measurement) (CoverageOut, error) {
	cfg := in.Config
	floor, tolerance, composed := cfg.Coverage.Floor, cfg.Coverage.Tolerance, !in.Narrowed
	base, err := gitstate.ResolveBaseSHA(ctx, in.Dir, in.BaseBranch)
	if err != nil {
		return CoverageOut{}, fmt.Errorf("--gate-coverage needs the merge-base with the base branch, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	// On the default branch HEAD is its own merge-base, so the tree this run
	// just measured IS the tree a baseline would be read for. Reading it would
	// miss on the first build and measure the whole repository a second time,
	// in a throwaway worktree, to reproduce the numbers already in hand.
	//
	// There is nothing to gate against either — the current commit is the
	// baseline — so the figures render the way an ungated run's do, and the
	// measurement is proposed. Rendering them as passes would claim a
	// comparison that did not happen.
	headTree, headErr := gitstate.TreeSHA(ctx, in.Dir, "HEAD")
	baseTree, baseErr := gitstate.TreeSHA(ctx, in.Dir, base)
	if headErr == nil && baseErr == nil && headTree == baseTree {
		rows := ungatedRows(ms, composed)
		rows = append(rows, testmeasure.FloorRows(ms, floor, composed)...)
		// This row says only that nothing was compared. Whether anything is
		// recorded is the record row's to say, written from what the run
		// actually established rather than from what it was about to attempt:
		// a candidate is declined for three reasons — a run that measured
		// nothing, a component with no entry to record, a tree that would not
		// resolve — and all three report to stderr, which the pull-request
		// comment does not render.
		rows = append(rows, ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value:  "not read — HEAD is its own merge-base, so there is no earlier measurement to compare against",
			Detail: []string{"nothing was gated: this tree is the one a later change is measured against, and recording it is what this run is for"}})
		// No patch parts: HEAD is its own merge-base, so the diff this figure
		// would be composed over is empty.
		candidate := candidateThisTree(ctx, w, in.Dir, in.Decl, ms, previousTreeBaseline(ctx, in.Dir), gitstate.Snapshot{}, false, nil, tolerance)
		return CoverageOut{Rows: rows, Candidate: &candidate}, nil
	}

	snap, rows, err := baselineFor(ctx, w, in.Dir, base, in.Concurrency, in.Logs)
	if err != nil {
		return CoverageOut{}, err
	}

	// Carried entries are what makes a narrowed run compose honestly: a
	// component this run did not measure contributes the counts already
	// recorded for the tree it is unchanged from, and every composed row says
	// how many of each it is made of.
	current := make([]testmeasure.Measurement, len(ms))
	carried := map[string]bool{}
	for i, m := range ms {
		current[i] = m
		if m.Measured() || !m.Carryable {
			continue
		}
		if lines, ok := snap.Coverage[m.Name]; ok && lines.Measured() {
			current[i] = testmeasure.FromEntry(m, lines)
			carried[m.Name] = true
		}
	}

	// Patch is computed before any row is built so each component's coverage
	// and patch land together.
	patch, parts, found, err := patchRows(ctx, w, in.Dir, base, ms, snap.Coverage, cfg)
	if err != nil {
		return CoverageOut{}, err
	}
	for _, m := range ms {
		rows = append(rows, testmeasure.ComponentRow(m, snap.Coverage, tolerance))
		if row, ok := patch[m.Name]; ok {
			rows = append(rows, row)
		}
		row, findings := testmeasure.CRAPRow(m, snap.CRAP, true)
		rows = append(rows, row)
		found = append(found, findings...)
	}
	// The two figures no shard can produce. Both sum every component, so a
	// run responsible for part of the declaration would answer about the part
	// and label it the repository; `lydite test merge` composes them once from
	// every shard's measurements instead.
	if composed {
		rows = append(rows, testmeasure.ComposedRows(current, carried, snap.Coverage, parts, cfg)...)
		if row, ok := testmeasure.CRAPSummaryOf(ms, carried, snap.CRAP); ok {
			rows = append(rows, row)
		}
	}
	rows = append(rows, testmeasure.FloorRows(ms, floor, composed)...)
	candidate := candidateThisTree(ctx, w, in.Dir, in.Decl, ms, snap, snap, true, parts, tolerance)
	return CoverageOut{Rows: rows, Findings: found, Candidate: &candidate}, nil
}

// patchRows gates each component's changed lines against that component's own
// baseline.
//
// Per component, and against that component's own baseline rather than a
// repository-wide one: a change to a well-tested component held to the
// repository's average is held to nothing, and one to a poorly tested
// component is failed for reaching the standard it already has.
//
// A component whose files the diff touched but which produced no per-line data
// is reported as unmeasured, never skipped in silence. A silent skip reads as
// "patch coverage passed" in the pull request comment, which is what
// wardnet/wardnet#957 shipped while Codecov failed the same diff.
func patchRows(ctx context.Context, w io.Writer, dir, base string, ms []testmeasure.Measurement, baseline gitstate.Baseline, cfg config.Config) (map[string]ui.Row, []testmeasure.PatchPart, []finding.Finding, error) {
	byComponent := map[string]ui.Row{}
	wanted := map[runner.Lang]bool{
		runner.Go:         cfg.Coverage.Patch.Go.Enabled,
		runner.Rust:       cfg.Coverage.Patch.Rust.Enabled,
		runner.TypeScript: cfg.Coverage.Patch.TypeScript.Enabled,
	}
	var exts []string
	for _, m := range ms {
		if wanted[m.Lang] {
			exts = append(exts, runner.SourceExtsFor(m.Lang)...)
		}
	}
	if len(exts) == 0 {
		return byComponent, nil, nil, nil
	}
	// One diff for every component, partitioned below. All of them measure
	// the same range, and asking git once per component would pay for the
	// same walk N times to get N subsets of one answer.
	changed, err := coverage.ChangedLines(ctx, dir, base, exts...)
	if err != nil {
		return nil, nil, nil, err
	}

	var parts []testmeasure.PatchPart
	var findings []finding.Finding
	for _, m := range ms {
		if !wanted[m.Lang] {
			continue
		}
		label := "patch(" + m.Name + ")"
		scoped := testmeasure.ScopeToComponent(changed, m)
		if len(scoped) == 0 {
			// Nothing of this component's changed, so there is nothing to
			// gate. Silent, because a row per untouched component on every
			// change is the noise that trains readers to skip the rows that
			// matter.
			continue
		}
		if !m.Measured() {
			lines := 0
			for _, l := range scoped {
				lines += len(l)
			}
			byComponent[m.Name] = testmeasure.UnmeasuredRow(label, fmt.Sprintf("%d changed line(s) across %d file(s), and no per-line coverage: %s", lines, len(scoped), m.Why))
			_, _ = fmt.Fprintf(w, "warning: this change touches component %q and its patch coverage could not be measured — %s\n", m.Name, m.Why)
			continue
		}
		hit, total := coverage.PatchPercent(scoped, m.Hits)
		if total == 0 {
			// Every changed line is a comment, a blank, or something the
			// report has no entry for. There is no coverable line to gate.
			continue
		}
		// The same comparability rule the aggregate applies. Patch gates
		// against the component's aggregate baseline percentage, so a
		// baseline taken by another instrument is as incomparable here as it
		// is there, and an unmeasured base already renders as new.
		base, _ := testmeasure.ComparableBase(m, baseline)
		row := testmeasure.PatchRow(label, hit, total, base.LineCount, cfg.Coverage.Patch.Tolerance)
		byComponent[m.Name] = row
		findings = append(findings, testmeasure.PatchFindings(row, dir, m, scoped)...)
		parts = append(parts, testmeasure.PatchPart{Name: m.Name, Lang: m.Lang, Hit: hit, Total: total, Base: base.LineCount})
	}
	return byComponent, parts, findings, nil
}
