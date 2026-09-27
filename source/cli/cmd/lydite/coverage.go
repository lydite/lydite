package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/crap"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	testmeasure "lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// measurement is one component's coverage outcome for this run. The type and
// every rule about what it means live in internal/test/measure; this package
// names it for the commands that build, fold and render one.
type measurement = testmeasure.Measurement

// scorableLang is testmeasure.ScorableLang.
func scorableLang(lang runner.Lang) bool { return testmeasure.ScorableLang(lang) }

// fromEntry is testmeasure.FromEntry.
func fromEntry(m measurement, e gitstate.Entry) measurement { return testmeasure.FromEntry(m, e) }

// unmeasuredComponent is testmeasure.UnmeasuredComponent.
func unmeasuredComponent(c component.Component, why string) measurement {
	return testmeasure.UnmeasuredComponent(c, why)
}

// unmeasurableComponent is testmeasure.UnmeasurableComponent.
func unmeasurableComponent(c component.Component, why string) measurement {
	return testmeasure.UnmeasurableComponent(c, why)
}

// langOf is testmeasure.LangOf.
func langOf(c component.Component) runner.Lang { return testmeasure.LangOf(c) }

// measure is testmeasure.Measure, reading the report under the environment
// childEnv composes for the component — the one its suite ran under.
func measure(ctx context.Context, root string, c component.Component, inv runner.Invocation, cfg config.Config, tc *toolchain.Env, instrument bool) measurement {
	return testmeasure.Measure(ctx, root, c, inv, cfg, tc, instrument, childEnv(tc, c, runner.Invocation{}))
}

// score is testmeasure.Score.
func score(root string, m measurement) (crap.Report, string) { return testmeasure.Score(root, m) }

// coverageOptions is what the command decided about coverage before anything
// ran, so the gate below does not re-derive it.
type coverageOptions struct {
	// Instrument runs each component's instrumented variant. Off means no
	// coverage row is emitted at all: a row saying `unmeasured` on every fast
	// local run trains readers to ignore the tag that exists to be noticed.
	Instrument bool
	// Gate reads the baseline, compares against it and records a new one.
	// Explicit, because measuring is local and gating pushes to a shared
	// branch — a developer's `lydite test` must never write to it, and every
	// signal for "am I in CI" is unreliable where lydite runs.
	Gate bool
	// BaseBranch is the --base-branch override, empty to discover it.
	BaseBranch string
	// Concurrency is the bound a baseline measurement runs under, so the run
	// that measures the base tree is bounded exactly as this one is.
	Concurrency int
	// Selected says the run narrowed by affected selection, which is the only
	// narrowing that licenses carrying an unmeasured component's baseline
	// forward: selection determined the change could not have broken it, so it
	// is unchanged from the tree that entry describes.
	//
	// `--component` narrows for a different reason — the caller wanted these
	// components run — and says nothing about the others. Carrying under it
	// would attribute the merge-base's number to a component whose code this
	// very change may have rewritten.
	Selected bool
	// Narrowed says `--component` was passed, which is what makes a
	// repository-wide figure unanswerable here. coverage(repo) and patch(repo)
	// sum every component; a shard holding two of four would publish its own
	// two under a label about the repository, and three shards would publish
	// three different answers to one question. `lydite test merge` emits them
	// once instead.
	Narrowed bool
}

// addCoverageRows renders — and, when asked, gates — this run's coverage.
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
// It returns nothing. Everything that can go wrong here goes wrong after every
// suite has already run, so a returned error would discard a report that may
// have taken twenty minutes to produce — no component rows, no logs named, no
// --json document, for a failure about the gate rather than about the code.
// The same reason the flag conflicts in newTestCmd are checked before the git
// walks: a run must not pay for its work and then throw the answer away.
//
// A gate the caller asked for and lydite could not run is a failing row, not an
// unmeasured one. The caller passed --gate-coverage; silently not gating is the
// state this whole file exists to make impossible.
// own is what this run is responsible for: its `--component` list, or the
// whole declaration. It reports one row per component in it and nothing at all
// about any other, so every declared component appears exactly once across a
// matrix of shards. It is a parameter rather than a field on the options
// because an empty set is a report about no component, and a caller must not be
// able to reach that by omission.
func addCoverageRows(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir string, decl component.File, own []component.Component, ms []measurement, cfg config.Config, opts coverageOptions) {
	if !opts.Instrument {
		return
	}
	ordered := inDeclarationOrder(own, ms, opts.Selected)
	nameUnusedDeclarations(cmd, ordered)
	if !opts.Gate {
		ungatedRows(rep, ordered, !opts.Narrowed)
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value: "not read — pass --gate-coverage to compare against it"})
		floorRows(rep, ordered, cfg.Coverage.Floor, !opts.Narrowed)
		return
	}
	// Into a report of its own, committed only once it succeeds. gatedRows
	// adds rows as it goes and can fail after most of them are in — a failing
	// `git diff` inside patchRows is the live path — and the recovery below
	// would then leave two rows per component under one label, with
	// contradictory statuses. A consumer keying rows by label silently picks
	// one of two answers.
	gated := ui.NewReport("test")
	if err := gatedRows(ctx, cmd, gated, dir, decl, ordered, cfg, opts); err != nil {
		// The measurements are still worth showing: they are what a reader
		// needs in order to act on the gate that could not run.
		ungatedRows(rep, ordered, !opts.Narrowed)
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "baseline",
			Value: "not gated", Detail: strings.Split(err.Error(), "\n")})
		floorRows(rep, ordered, cfg.Coverage.Floor, !opts.Narrowed)
		return
	}
	for _, row := range gated.Rows() {
		rep.Add(row)
	}
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
// Coverage is warned about in all three languages. The Go path resolves a
// declaration through go/ast and the Rust and TypeScript paths through
// tree-sitter, and both report an unmatched one the same way — a declaration
// that means one thing in Go and nothing elsewhere is what ADR 0034 rejected.
//
// Named rather than dropped, for the reason a mutation declaration covering no
// mutant is named: its author believes they have answered a finding, and
// nothing they can see says otherwise. The commonest cause is one written
// inside a body, where it reads perfectly and does nothing.
//
// Here rather than where the score is taken, because a base tree is measured
// through that same path and its report is discarded — so a declaration in a
// tree nobody is looking at is never reported as this run's, and the warning
// goes to the command's stderr with every other one rather than to os.Stderr
// directly.
func nameUnusedDeclarations(cmd *cobra.Command, ms []measurement) {
	for _, m := range ms {
		for _, where := range m.CRAP.Unused {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s covers no function, so nothing is excluded by it\n",
				where, annotation.Marker(annotation.CRAP))
		}
		for _, where := range m.Unused {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s: %s covers no function, so nothing is excluded by it\n",
				where, annotation.Marker(annotation.Coverage))
		}
	}
}

// inDeclarationOrder returns one measurement per component this run is
// responsible for, in the order the file declares them.
//
// Every component in that set, including one this invocation never selected:
// omitting the rest would make a narrowed run indistinguishable from a
// complete one. Nothing outside it, because a shard that padded a row for
// every *declared* component publishes rows about components other shards are
// running, and the merged document then holds one answer per shard under each
// label. Declaration order rather than completion order, so two runs of one
// declaration produce the same document.
func inDeclarationOrder(own []component.Component, ms []measurement, selected bool) []measurement {
	byName := make(map[string]measurement, len(ms))
	for _, m := range ms {
		byName[m.Name] = m
	}
	out := make([]measurement, 0, len(own))
	for _, c := range own {
		if m, ok := byName[c.Name]; ok {
			out = append(out, m)
			continue
		}
		// The only place a carryable measurement is made, and only when
		// affected selection is what left the component out. Everything else
		// in this file describes a component this run actually reached.
		m := unmeasuredComponent(c, "the component was not selected for this run")
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
func ungatedRows(rep *ui.Report, ms []measurement, composed bool) {
	ungatedComponentRows(rep, ms)
	// Only a run responsible for the whole declaration answers about the
	// repository. A shard's own components are not it.
	//
	// No carried entries, because these are the measurements as taken: a
	// component this run did not select is unmeasured here rather than
	// standing in for the baseline's counts, so nothing in ms is carried and
	// the map would be empty anyway. The fold passes what it carried, since
	// its measurements arrive with those counts already substituted in.
	if composed {
		rep.Add(ungatedComposedRow(repoLabel("coverage"), ms, nil, everything))
		if row, ok := crapSummaryOf(ms, nil, nil); ok {
			rep.Add(row)
		}
	}
}

// ungatedComponentRows is the per-component half, shared by the two runs that
// compare nothing: one that was never asked to, and one on the default branch
// where HEAD is its own merge-base.
func ungatedComponentRows(rep *ui.Report, ms []measurement) {
	for _, m := range ms {
		if !m.Measured() {
			rep.Add(unmeasuredRow("coverage("+m.Name+")", m.Why))
		} else {
			rep.Add(ui.Row{Status: ui.StatusContext, Label: "coverage(" + m.Name + ")", Value: lineValue(m.Lines)})
		}
		// Beside its component's coverage row rather than in a block of its
		// own, because the two are one question about one component: a reader
		// asking about it should not have to pair rows separated by every
		// other component's.
		row, _ := crapRow(m, nil, false)
		rep.Add(row)
	}
}

// ungatedComposedRow is testmeasure.UngatedComposedRow.
func ungatedComposedRow(label string, ms []measurement, carried map[string]bool, in func(measurement) bool) ui.Row {
	return testmeasure.UngatedComposedRow(label, ms, carried, in)
}

// gatedRows reads the baseline, compares every altitude against it, gates the
// patch and the floor, and records this tree's measurement.
func gatedRows(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir string, decl component.File, ms []measurement, cfg config.Config, opts coverageOptions) error {
	base, err := gitstate.ResolveBaseSHA(ctx, dir, opts.BaseBranch)
	if err != nil {
		return fmt.Errorf("--gate-coverage needs the merge-base with the base branch, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	// On the default branch HEAD is its own merge-base, so the tree this run
	// just measured IS the tree a baseline would be read for. Reading it would
	// miss on the first build and measure the whole repository a second time,
	// in a throwaway worktree, to reproduce the numbers already in hand.
	//
	// There is nothing to gate against either — the current commit is the
	// baseline — so the figures render the way an ungated run's do, and the
	// measurement is recorded. Rendering them as passes would claim a
	// comparison that did not happen.
	headTree, headErr := gitstate.TreeSHA(ctx, dir, "HEAD")
	baseTree, baseErr := gitstate.TreeSHA(ctx, dir, base)
	if headErr == nil && baseErr == nil && headTree == baseTree {
		ungatedRows(rep, ms, !opts.Narrowed)
		floorRows(rep, ms, cfg.Coverage.Floor, !opts.Narrowed)
		// The row is written from what the run actually established, not from
		// what it was about to attempt. A candidate is declined for three
		// reasons — a run that measured nothing, a component with no entry to
		// record, a tree that would not resolve — and all three report to
		// stderr, which the pull-request comment does not render. A row
		// claiming a recording that did not happen is the same untruth as a
		// gate that did not run reading as one that passed.
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value:  "not read — HEAD is its own merge-base, so there is no earlier measurement to compare against",
			Detail: []string{"nothing was gated: this tree is the one a later change is measured against, and recording it is what this run is for"}})
		// No patch parts: HEAD is its own merge-base, so the diff this
		// figure would be composed over is empty.
		doc, value := candidateThisTree(ctx, cmd, dir, decl, ms, previousTreeBaseline(ctx, dir), gitstate.Snapshot{}, false, nil, cfg.Coverage.Tolerance)
		rep.Add(candidateRow(cmd, dir, doc, value))
		return nil
	}

	snap, err := baselineFor(ctx, cmd, rep, dir, base, opts)
	if err != nil {
		return err
	}

	// Carried entries are what makes a narrowed run compose honestly: a
	// component this run did not measure contributes the counts already
	// recorded for the tree it is unchanged from, and every composed row says
	// how many of each it is made of.
	current := make([]measurement, len(ms))
	carried := map[string]bool{}
	for i, m := range ms {
		current[i] = m
		if m.Measured() {
			continue
		}
		if !m.Carryable {
			continue
		}
		if lines, ok := snap.Coverage[m.Name]; ok && lines.Measured() {
			current[i] = fromEntry(m, lines)
			carried[m.Name] = true
		}
	}

	// Patch is computed before any row is added so each component's coverage
	// and patch land together. A reader asking about one component should not
	// have to pair two rows separated by every other component's.
	patch, parts, patchFound, err := patchRows(ctx, cmd, dir, base, ms, snap.Coverage, cfg)
	if err != nil {
		return err
	}
	rep.AddFindings(patchFound...)
	for _, m := range ms {
		rep.Add(componentRow(m, snap.Coverage, cfg.Coverage.Tolerance))
		if row, ok := patch[m.Name]; ok {
			rep.Add(row)
		}
		row, findings := crapRow(m, snap.CRAP, true)
		rep.Add(row)
		rep.AddFindings(findings...)
	}
	// The two rows no shard can produce. Both sum every component, so a run
	// responsible for part of the declaration answers about the part and
	// labels it the repository; `lydite test merge` composes them once from
	// every shard's measurements instead.
	if !opts.Narrowed {
		composedRows(rep, current, carried, snap.Coverage, parts, cfg)
		if row, ok := crapSummaryOf(ms, carried, snap.CRAP); ok {
			rep.Add(row)
		}
	}
	floorRows(rep, ms, cfg.Coverage.Floor, !opts.Narrowed)
	// `record` and not `baseline`: the two are different events in one run —
	// the baseline read this change is gated against, and the entry this
	// change leaves for the next one. Sharing a label would put two rows under
	// it, which is what a consumer keying rows by label cannot survive.
	doc, value := candidateThisTree(ctx, cmd, dir, decl, ms, snap, snap, true, parts, cfg.Coverage.Tolerance)
	rep.Add(candidateRow(cmd, dir, doc, value))
	return nil
}

// reasonOnly is a candidate that establishes nothing, and why.
//
// It still names the tree it was taken on. `record` binds a candidate to the
// checkout it may be landed from and refuses one that names no tree at all, so
// a reason-carrying document without it is unreadable — and the command that
// would have shown the reason fails with "no candidate found" instead, which
// says the run produced nothing rather than what it produced nothing about.
//
// A tree that will not resolve leaves it empty, which is the one case where
// there is genuinely nothing to bind to.
//
// It still carries the test counts, because a run in which every suite failed
// is precisely the run that establishes no baseline and has the counts a
// history most wants. Dropping them here would lose the numbers for every red
// commit, which are the ones a trend line exists to show.
func reasonOnly(ctx context.Context, w io.Writer, dir, prefix, reason string, ms []measurement) (measurementsDoc, string) {
	tree, _ := gitstate.TreeSHA(ctx, dir, "HEAD")
	return measurementsDoc{Tree: tree, Reason: reason, Tests: testCounts(w, ms)}, prefix + " — " + reason
}

// candidateRow saves what this run would record and says so.
//
// The row is `record` rather than `candidate`, because what a reader wants to
// know is whether the next change has a baseline to gate against, and the
// answer travels through this file whether or not the write lands here.
func candidateRow(cmd *cobra.Command, root string, doc measurementsDoc, value string) ui.Row {
	if err := writeMeasurements(root, doc); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the candidate baseline: %v\n", err)
		return ui.Row{Status: ui.StatusContext, Label: "record",
			Value: "not recorded — the candidate baseline could not be written, so there is nothing for `lydite test record` to land"}
	}
	return ui.Row{Status: ui.StatusContext, Label: "record", Value: value}
}

// previousTreeBaseline is the entry recorded for the commit immediately before
// this one, used only to anchor a tolerated dip when recording on a branch that
// is its own base.
//
// That path reads no baseline — there is nothing to gate against — so without
// this the anchoring is a no-op and a raw within-tolerance dip is recorded
// against nothing. Each merge then dips up to one tolerance from the last
// recorded value and passes, which is the per-merge downward ratchet the
// anchoring exists to prevent, on the one path with no prior number in hand.
// A squash merge hides it, because the tree the pull request already anchored
// is the tree that lands; a rebase merge or a direct push does not.
//
// The commit immediately before, and never the nearest ancestor that happens
// to have an entry. A fixed one step back is reproducible from the change
// itself; a walk makes the number depend on how far back history had one,
// which is what ADR 0019 rejected for gating. This anchors a recording and
// gates nothing — a miss simply means no anchor, exactly as before.
func previousTreeBaseline(ctx context.Context, dir string) gitstate.Snapshot {
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD~1")
	if err != nil {
		return gitstate.Snapshot{}
	}
	// Every metric, because a component affected selection did not run carries
	// each of them forward from here, and an anchor holding half a tree's
	// state silently drops the other half.
	snap, err := gitstate.ReadSnapshot(ctx, dir, tree)
	if err != nil {
		return gitstate.Snapshot{}
	}
	return snap
}

// baselineFor reads the base tree's baseline, and measures it when there is
// none.
//
// A miss is resolved by measuring, never by substituting the nearest ancestor
// that happens to have an entry: which number a change is judged against would
// then depend on how far back history had one, which is not reproducible from
// the change itself.
func baselineFor(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir, base string, opts coverageOptions) (gitstate.Snapshot, error) {
	tree, err := gitstate.TreeSHA(ctx, dir, base)
	if err != nil {
		tree = ""
	}
	cached, err := gitstate.ReadSnapshot(ctx, dir, tree, base)
	if err != nil {
		return gitstate.Snapshot{}, err
	}
	// The coverage baseline alone decides whether the base tree is measured
	// again. CRAP is its own document with its own miss, and a miss there does
	// not measure: every repository has a coverage baseline and no CRAP one
	// the first time it runs a lydite that computes CRAP, and re-measuring the
	// base tree for a metric that gates nothing yet would charge every one of
	// them a full suite run for it. Those components read `new` and gate
	// nothing for one change, which is the shape a changed producer already
	// has.
	if len(cached.Coverage) > 0 {
		return cached, nil
	}
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
		Value:  fmt.Sprintf("not cached for %s — measuring it now", shortSHA(base)),
		Detail: []string{"the first change against this base tree pays this cost"}})
	// A cache miss measures, and the measurement answers both metrics at
	// once: CRAP is computed from the coverage report, so a base tree
	// measured for one is measured for the other with nothing further to run.
	snap, err := measureBaseTree(ctx, cmd, dir, base, opts)
	if err != nil {
		return gitstate.Snapshot{}, err
	}
	// A base tree nothing could be measured at gates against nothing, and the
	// run says so rather than comparing against an empty baseline — which
	// renders every component new and enforces nothing.
	if len(snap.Coverage) == 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: measured no coverage at all at %s. The gate cannot compare against a baseline of nothing; fix what failed above and the next run measures it again.\n", base)
	}
	// The measurement is not cached, because `lydite test` writes nothing to
	// the lydite branch. It runs the repository's own suites and any
	// setup/teardown shell the declaration carries, and a job doing that must
	// not also hold a token that can push — which is the whole of what makes
	// the recording a separate command.
	//
	// What fills this cache is `lydite test record` on a tree that has
	// already merged, so the cost here is bounded by how often a base tree is
	// one nothing recorded: the first change after adopting the gate, and any
	// change based on a commit whose own run never landed its entry.
	return snap, nil
}

// measureBaseTree checks the base commit out into a throwaway worktree and
// measures it through the same path this command just took.
//
// The same path, deliberately: it starts the compose services each component
// declares, installs what each runner needs, and reads the report the
// instrumented variant wrote. Invoking coverage tooling directly instead is
// what produced a failed measurement for every suite that needs a database,
// which is one of the roads to an empty baseline cached as real.
func measureBaseTree(ctx context.Context, cmd *cobra.Command, dir, base string, opts coverageOptions) (gitstate.Snapshot, error) {
	// Nothing here may return a baseline missing a component it was supposed
	// to measure. A bare worktree is where a measurement most often fails —
	// no container runtime for a component's services, an install that fails
	// there, a cold instrumentation build that times out — and a partial
	// entry, once cached, reads as a hit for every later change: the missing
	// component is new forever, and its language's row and the global row
	// refuse to compare and stop gating, silently and with a green verdict.
	// The same rule recordThisTree applies to what a run records.
	tmp, err := os.MkdirTemp("", "lydite-baseline-*")
	if err != nil {
		return gitstate.Snapshot{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// A context of its own, for the reason every compose teardown here has
	// one: the run's may already be cancelled, and an interrupt would kill the
	// removal and then delete the directory anyway — leaving a registered
	// worktree pointing at nothing, which every later `git worktree add` and
	// `git worktree list` in that repository trips over until someone prunes.
	defer func() {
		_ = executil.RunQuiet(context.WithoutCancel(ctx), dir, "git", "worktree", "remove", "--force", tmp)
	}()

	if r := executil.RunQuiet(ctx, dir, "git", "worktree", "add", "--detach", tmp, base); !r.Ok() {
		return gitstate.Snapshot{}, fmt.Errorf("checking out %s to measure its baseline: %w", shortSHA(base), r.Err)
	}
	// A worktree holds the whole repository, and the scan root may sit below
	// it. Measuring at the worktree root instead would look for
	// `.lydite/components.yml` where there is none, find no components, and
	// hand back an empty baseline — so every component reads as new, every
	// composed row refuses to compare, and the gate passes having compared
	// nothing. A monorepo run as `--dir source` is the shape, and it is the
	// shape `ChangedLines` and `selectAffected` already account for.
	//
	// Worse where the repository also has a declaration at its own root: the
	// base tree would then be measured through a different repository's
	// components.
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("locating the scan root inside the repository: %w", err)
	}
	root := filepath.Join(tmp, filepath.FromSlash(prefix))
	// The base tree's own declaration and configuration, never this branch's.
	// A component this change adds did not exist there, and one it renames is
	// a different component; measuring the base tree through the branch's
	// declaration would attribute one component's coverage to another.
	// Read leniently, because this tree is being measured rather than
	// configuring lydite: it is guaranteed to be the tree written for the
	// version before whatever this one rejects, and the first pull request
	// that removes a retired key has exactly that tree as its base.
	baseCfg, err := config.LoadHistorical(root)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("reading %s at %s: %w", config.FileName, shortSHA(base), err)
	}
	// Read leniently for the reason the configuration beside it is: a key this
	// version stopped reading is one the base tree still carries, and refusing
	// to measure it leaves the change gating against nothing.
	decl, err := component.LoadHistorical(root)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("reading %s at %s: %w", component.FileName, shortSHA(base), err)
	}
	if len(decl.Components) == 0 {
		return gitstate.Snapshot{}, nil
	}
	// Its own report, discarded. The base tree's rows describe a run nobody
	// asked for, and adding them to this run's report would put a second set
	// of component rows beside the ones the reader is looking at.
	scratch := ui.NewReport("baseline")
	// The base tree's own toolchains, from its own declaration and its own
	// config. A component this change adds did not exist there, and one whose
	// engines.node this change raises needs the version the base tree asked
	// for — measuring it under the branch's would be measuring a tree that
	// never existed.
	//
	// A failure here warns and measures with what is on PATH rather than
	// ending the run. The only thing that can fail is a `toolchain.go` or
	// `toolchain.node` override the base tree wrote and lydite cannot parse,
	// and the author of a historical tree is not being addressed and cannot
	// act — the argument config.LoadHistorical is built on, which erroring
	// here would undo one layer up. The branch's own override is still
	// checked, by the resolution this run did for itself, so a repository
	// carrying a bad value is told about it exactly once and on the tree
	// whose author can fix it.
	// The whole of the base tree's declaration, because that is what this
	// measurement runs: a baseline missing a component reads as a cache hit
	// on every later change, so a shard measures the base tree completely or
	// not at all.
	envs, err := ensureToolchains(ctx, cmd, root, baseCfg, componentUnits(decl.Components))
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: could not resolve the base tree's toolchains (%v) — measuring it with what is on PATH\n", err)
		envs = nil
	}
	ms := runComponents(ctx, root, decl.Components, nil, nil, baseCfg, envs, opts.Concurrency, false, true, scratch)

	// The worktree — and every log written into it — is removed on the way
	// out, so the tail under a failing row is the only account of what went
	// wrong that can outlive this function. It goes into the warning rather
	// than being deleted with the directory that holds it.
	tails := map[string][]string{}
	for _, row := range scratch.Rows() {
		if name, ok := strings.CutPrefix(row.Label, "test("); ok && row.Status == ui.StatusFail {
			tails[strings.TrimSuffix(name, ")")] = row.Detail
		}
	}

	out := baseTreeBaseline(cmd.ErrOrStderr(), base, ms, tails)
	// The same predicate a run applies to what it records, over the same two
	// things: the measurements, and what came of them. Returned empty rather
	// than partial, so the caller's existing refusal to cache an empty
	// baseline covers this too — the next change measures the tree again,
	// which is slower and correct.
	//
	// Asked of the coverage half alone, because that is the half a partial
	// entry poisons: a composed figure refuses to compare unless the baseline
	// covers every component in it, so one missing component silently stops
	// the repository-wide rows gating. CRAP has no composed gate, so a
	// component missing from it reports `new` on the next change, gates
	// nothing for that one change, and is recorded — which heals rather than
	// persisting.
	if _, blocked := recordingBlockedBy(ms, out); blocked {
		return gitstate.Snapshot{}, nil
	}
	return gitstate.Snapshot{Coverage: out, CRAP: baseTreeCRAP(ms)}, nil
}

// baseTreeCRAP is what the base-tree run scored, for the components it scored.
//
// No warning of its own: a component that could not be scored either could not
// be measured — baseTreeBaseline has already named it — or is in a language
// lydite scores none of, which is permanent and expected.
func baseTreeCRAP(ms []measurement) gitstate.CRAPBaseline {
	out := gitstate.CRAPBaseline{}
	for _, m := range ms {
		if m.Scored() {
			out[m.Name] = m.CRAPEntry()
		}
	}
	return out
}

// baseTreeBaseline is what a base-tree run measured, and the account of what it
// did not.
//
// A component nothing could ever measure is neither: its absence is permanent
// and expected rather than a gap this run created, so warning about it would
// print what reads as a measurement failure on every baseline computation,
// forever, about a state the repository stated on purpose. recordingBlockedBy
// already exempts it and floorRows already excludes it; this is the third
// place the same distinction has to be drawn.
//
// Everything else that produced no measurement is named on stderr rather than
// dropped in silence, and the tail with it: the worktree holding the log is
// removed on the way out, so this is the only account of the failure that
// outlives the measurement.
func baseTreeBaseline(w io.Writer, base string, ms []measurement, tails map[string][]string) gitstate.Baseline {
	out := gitstate.Baseline{}
	for _, m := range ms {
		switch {
		case m.Measured():
			out[m.Name] = m.Entry()
		case m.Unmeasurable:
		default:
			_, _ = fmt.Fprintf(w, "warning: the base tree at %s could not be measured for component %q: %s\n", shortSHA(base), m.Name, m.Why)
			for _, line := range tails[m.Name] {
				_, _ = fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
	return out
}

// crapRow is testmeasure.CRAPRow.
func crapRow(m measurement, baseline gitstate.CRAPBaseline, gated bool) (ui.Row, []finding.Finding) {
	return testmeasure.CRAPRow(m, baseline, gated)
}

// crapValue is testmeasure.CRAPValue.
func crapValue(rep crap.Report) string { return testmeasure.CRAPValue(rep) }

// worstOffenders is how many functions a failing CRAP row names.
const worstOffenders = testmeasure.WorstOffenders

// crapSummaryRow is testmeasure.CRAPSummaryRow.
func crapSummaryRow(scorable int, scored, carried []gitstate.CRAPEntry) (ui.Row, bool) {
	return testmeasure.CRAPSummaryRow(scorable, scored, carried)
}

// crapSummaryOf is testmeasure.CRAPSummaryOf.
func crapSummaryOf(ms []measurement, carried map[string]bool, anchor gitstate.CRAPBaseline) (ui.Row, bool) {
	return testmeasure.CRAPSummaryOf(ms, carried, anchor)
}

// componentRow is testmeasure.ComponentRow.
func componentRow(m measurement, baseline gitstate.Baseline, tolerance float64) ui.Row {
	return testmeasure.ComponentRow(m, baseline, tolerance)
}

// comparableBase is testmeasure.ComparableBase.
func comparableBase(m measurement, baseline gitstate.Baseline) (gitstate.Entry, bool) {
	return testmeasure.ComparableBase(m, baseline)
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
func patchRows(ctx context.Context, cmd *cobra.Command, dir, base string, ms []measurement, baseline gitstate.Baseline, cfg config.Config) (map[string]ui.Row, []patchPart, []finding.Finding, error) {
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

	var parts []patchPart
	var findings []finding.Finding
	for _, m := range ms {
		if !wanted[m.Lang] {
			continue
		}
		label := "patch(" + m.Name + ")"
		scoped := scopeToComponent(changed, m)
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
			byComponent[m.Name] = unmeasuredRow(label, fmt.Sprintf("%d changed line(s) across %d file(s), and no per-line coverage: %s", lines, len(scoped), m.Why))
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: this change touches component %q and its patch coverage could not be measured — %s\n", m.Name, m.Why)
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
		base, _ := comparableBase(m, baseline)
		row := patchRow(label, hit, total, base.LineCount, cfg.Coverage.Patch.Tolerance)
		byComponent[m.Name] = row
		findings = append(findings, patchFindings(row, dir, m, scoped)...)
		parts = append(parts, patchPart{Name: m.Name, Lang: m.Lang, Hit: hit, Total: total, Base: base.LineCount})
	}
	return byComponent, parts, findings, nil
}

// patchFindings is testmeasure.PatchFindings.
func patchFindings(row ui.Row, dir string, m measurement, scoped map[string][]int) []finding.Finding {
	return testmeasure.PatchFindings(row, dir, m, scoped)
}

// composedRows adds testmeasure.ComposedRows to rep, in the order it returns
// them.
func composedRows(rep *ui.Report, current []measurement, carried map[string]bool, baseline gitstate.Baseline, parts []patchPart, cfg config.Config) {
	for _, row := range testmeasure.ComposedRows(current, carried, baseline, parts, cfg) {
		rep.Add(row)
	}
}

// patchPart is one component's contribution to a composed patch figure.
type patchPart = testmeasure.PatchPart

// patchRow is testmeasure.PatchRow.
func patchRow(label string, hit, total int, base coverage.LineCount, tolerance float64) ui.Row {
	return testmeasure.PatchRow(label, hit, total, base, tolerance)
}

// scopeToComponent is testmeasure.ScopeToComponent.
func scopeToComponent(changed map[string][]int, m measurement) map[string][]int {
	return testmeasure.ScopeToComponent(changed, m)
}

// floorRows adds testmeasure.FloorRows to rep, in the order it returns them.
func floorRows(rep *ui.Report, ms []measurement, floor float64, summary bool) {
	for _, row := range testmeasure.FloorRows(ms, floor, summary) {
		rep.Add(row)
	}
}

// floorSummaryRow is testmeasure.FloorSummaryRow.
func floorSummaryRow(ms []measurement, floor float64) (ui.Row, bool) {
	return testmeasure.FloorSummaryRow(ms, floor)
}

// candidateThisTree is what this run would record as the baseline for the tree
// it measured, and the words the row shows.
//
// On a pull request HEAD is the merged result, and a squash merge lands a
// commit carrying exactly that tree — so this is already the baseline for the
// commit this change is about to become, measured by the pipeline that knows
// how to measure it. That is what removes the obligation to run on the default
// branch, an obligation a repository running CI only on pull requests never
// meets.
//
// It writes nothing to the lydite branch, and neither does anything else
// `lydite test` reaches. Measuring runs each component's suite and any
// setup/teardown shell the declaration carries, so on a pull request it runs
// the pull request's own code; a token that can push has no business in that
// job. `lydite test record` lands this document, executing none of it.
//
// It returns what it did, in the words a row shows, so no caller can announce
// a recording that did not happen.
// anchor is the baseline a within-tolerance dip is restored against, and
// gatedAgainst is the baseline this run actually compared with — the same map
// on the gated path, and nil where HEAD is its own merge-base. They are
// separated because the second travels into the document, and `lydite test
// merge` composes a *gated* repository-wide figure from whatever it finds
// there. On the default branch the anchor is the previous commit's entry,
// which gates nothing; stored as a comparison it would have the fold publish a
// verdict against a tree that is not any merge-base, over a run whose every
// row says nothing was gated.
// gated says this run compared against a baseline at all, which is not the
// same as any component having found an entry: a first adoption compares every
// component against nothing and must still fold as a gated run.
func candidateThisTree(ctx context.Context, cmd *cobra.Command, dir string, decl component.File, ms []measurement, anchor, gatedAgainst gitstate.Snapshot, gated bool, parts []patchPart, tolerance float64) (measurementsDoc, string) {
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
		return reasonOnly(ctx, cmd.ErrOrStderr(), dir, "nothing to record", "no component produced a measurement", ms)
	}
	scores := crapRecord(ms, record, carried, anchor.CRAP)
	// Held before the anchoring, because the two are different quantities: the
	// anchored entry is what gets recorded, and what was measured is what the
	// repository-wide figures sum.
	measured := record
	record = withToleratedDipsRestored(record, anchor.Coverage, tolerance)
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD")
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not resolve this tree, so its coverage was not recorded: %v\n", err)
		return measurementsDoc{Reason: "this tree could not be resolved"}, "not recorded — this tree could not be resolved"
	}
	doc := measurementsFrom(tree, record, measured, carried, scores, gatedAgainst.Coverage, gated, parts, testCounts(cmd.ErrOrStderr(), ms))

	// A run that could not measure a component it was supposed to has not
	// established this tree's baseline, and recording a partial one is worse
	// than recording none: ReadBaseline reports a hit for any non-empty entry,
	// so the missing component reads as new on every later change — and
	// because a composed figure refuses to compare unless the baseline covers
	// every component in it, that language's row and the global row stop
	// gating too. Silently, and with no way to notice.
	//
	// The refusal travels as a reason on a document that still carries every
	// component this run did measure. Emptying it instead loses those counts
	// for good: the document is the only channel by which a shard's numbers
	// reach `lydite test merge`, so one component's unreadable report would
	// drop every other component of that shard out of `coverage(repo)` — which
	// is then composed over a strict subset and rendered as a pass. What must
	// not be recorded is decided by `lydite test record`, against the
	// declaration of the tree being recorded, and it refuses this document by
	// name.
	//
	// A component nothing could ever measure — a raw `command:`, a runner
	// naming no report — does not block it. It contributes to neither side of
	// any comparison, so its absence from the baseline is permanent and
	// expected rather than a gap this run created.
	if gap, blocked := recordingBlockedBy(ms, record); blocked {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: component %q has no coverage to record (%s), so this tree's coverage was not recorded — the next change against it measures the tree instead of gating against a baseline missing a component\n",
			gap.Name, gap.Why)
		reason := fmt.Sprintf(
			"%s has no coverage to record, and a baseline missing a component gates on nothing", gap.Name)
		// Not set on the document. `foldMeasurements` surfaces a reason only
		// when the fold holds no component, and this document holds every
		// component the run did measure — so a reason set here reaches no
		// reader. What does reach one is this row, and, on the fold side,
		// `missingFromRecord` recomputing the same refusal from the
		// declaration and naming the same component.
		return doc, "not recorded — " + reason
	}
	// Against the declaration, not against what this run was responsible for.
	// A document covering part of the tree is expected — a shard covers its
	// slice, and completeness is the fold's question — but a row saying only
	// how many entries it holds announces a recording `lydite test record`
	// may then refuse, which is the untruth this whole file is arranged to
	// avoid. The two numbers differ exactly when the fold has something left
	// to decide.
	return doc, fmt.Sprintf("%d of %d component(s) ready for %s — `lydite test record` lands it",
		len(record), recordable(decl), shortSHA(tree))
}

// crapRecord is testmeasure.CRAPRecord.
func crapRecord(ms []measurement, record gitstate.Baseline, carried map[string]bool, anchor gitstate.CRAPBaseline) gitstate.CRAPBaseline {
	return testmeasure.CRAPRecord(ms, record, carried, anchor)
}

// recordable is testmeasure.Recordable.
func recordable(decl component.File) int { return testmeasure.Recordable(decl) }

// sameEntries is testmeasure.SameEntries.
func sameEntries[M ~map[string]E, E comparable](a, b M) bool { return testmeasure.SameEntries(a, b) }

// recordingBlockedBy is testmeasure.RecordingBlockedBy.
func recordingBlockedBy(ms []measurement, record gitstate.Baseline) (measurement, bool) {
	return testmeasure.RecordingBlockedBy(ms, record)
}

// withToleratedDipsRestored is testmeasure.WithToleratedDipsRestored.
func withToleratedDipsRestored(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline {
	return testmeasure.WithToleratedDipsRestored(record, baseline, tolerance)
}

// everything is testmeasure.Everything.
func everything(m measurement) bool { return testmeasure.Everything(m) }

// repoLabel is testmeasure.RepoLabel.
func repoLabel(metric string) string { return testmeasure.RepoLabel(metric) }

// lineValue is testmeasure.LineValue.
func lineValue(c coverage.LineCount) string { return testmeasure.LineValue(c) }

// unmeasuredRow is testmeasure.UnmeasuredRow.
func unmeasuredRow(label, why string) ui.Row { return testmeasure.UnmeasuredRow(label, why) }

// firstNonEmpty returns the first non-empty string, so a tree lookup that
// failed falls back to the commit SHA rather than writing an empty key.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// newRemovedCoverageCmd keeps the name `lydite coverage` addressable, so a
// workflow still invoking it is told what happened.
//
// Cobra's answer to an unknown command names the binary and lists what it does
// accept, which leaves a consumer to guess whether the command was renamed,
// dropped, or never existed. That guess is the same failure a silently ignored
// config key produces, one layer out: the flags this command carried are
// refused by name for exactly that reason, and the command that carried them
// should not be less clear than its flags.
//
// It accepts unknown flags so `lydite coverage --source=report` reaches this
// message rather than cobra's flag parser, which would report an unknown flag
// on a command that no longer exists at all.
func newRemovedCoverageCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "coverage",
		Hidden:             true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true,
		Short:              "removed — lydite test measures and gates coverage",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("`lydite coverage` is no longer a command: `lydite test` measures each component's coverage from its runner's instrumented variant, " +
				"and `lydite test --gate-coverage` gates it against the baseline\n" +
				"       --source, --tests, --go-report, --rust-report and --rust-lcov-report went with it; lydite writes every report itself\n" +
				"       see docs/adr/0019-coverage-per-component-gated-by-lydite-test.md")
		},
	}
}
