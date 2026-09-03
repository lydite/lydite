package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// measurement is one component's coverage outcome for this run.
//
// Every declared component produces exactly one, whether it ran or not. A
// component dropped from the set instead would leave a run that measured half
// the repository reading as a complete one, which is the failure every gate in
// this file is arranged around.
type measurement struct {
	// Name is the component's, which is what a baseline entry is keyed by:
	// two components may legitimately share a directory, and the name is the
	// only one of the two that is unique by construction.
	Name string
	// Dir is the component's directory relative to the scan root, which is
	// what scopes a diff's changed files to this component.
	Dir string
	// Lang is the language its runner implies, empty for a component that
	// declares a raw command.
	Lang runner.Lang
	// Lines is the measurement, zero when Why is set.
	Lines coverage.LineCount
	// Hits is the per-line data the patch gate reads, nil when Why is set.
	Hits coverage.LineHits
	// Why says why there is no measurement, and is empty exactly when there
	// is one. It is carried rather than inferred, because "this component was
	// not affected" and "this component's report could not be read" are the
	// same absence and want opposite reactions from a reader.
	Why string
	// Unmeasurable marks a component no run could ever measure: it declares a
	// raw command, or its runner's instrumented variant names no report. Its
	// absence from a baseline is permanent and expected rather than a gap one
	// run created, so it never blocks a recording.
	Unmeasurable bool
	// Carryable marks a component whose absence says nothing about its
	// content: this invocation did not select it, so it is unchanged from the
	// tree the baseline was recorded for and that entry still describes it.
	//
	// A component that ran and failed is not carryable, however tempting the
	// symmetry. Its content may be exactly what changed, so its old entry is a
	// guess — and one that renders as a pass, because a language whose only
	// component failed to build would otherwise report that component's last
	// good figure with a ✓ beside it.
	Carryable bool
}

// Measured reports whether this component produced a coverage measurement.
func (m measurement) Measured() bool { return m.Why == "" && m.Lines.Measured() }

// unmeasuredComponent is a component with no measurement, and the reason.
func unmeasuredComponent(c component.Component, why string) measurement {
	return measurement{Name: c.Name, Dir: c.Dir, Lang: langOf(c), Why: why}
}

// unmeasurableComponent is a component no run could ever measure, as opposed to
// one this run happened not to.
func unmeasurableComponent(c component.Component, why string) measurement {
	m := unmeasuredComponent(c, why)
	m.Unmeasurable = true
	return m
}

// langOf is the language a component's runner implies. A component declaring a
// raw command has none, which is also why it can have no instrumented variant.
func langOf(c component.Component) runner.Lang {
	if r, ok := runner.Lookup(c.Runner); ok {
		return r.Lang
	}
	return ""
}

// measure reads the report the component's instrumented invocation wrote.
//
// A component whose invocation names no coverage report is unmeasured with the
// reason said out loud — a raw `command:` has no instrumented variant to ask
// for, and no key exists to name where its coverage lands. Excluding it
// instead would drop it from the language and global figures silently, leaving
// a gate that covered fewer components than the repository has reading as a
// complete one.
func measure(ctx context.Context, root string, c component.Component, inv runner.Invocation, tc *toolchain.Env, instrument bool) measurement {
	switch {
	case !instrument:
		return unmeasuredComponent(c, "coverage is off for this run")
	case len(c.Command) > 0:
		return unmeasurableComponent(c, "the component declares a raw command, which has no instrumented variant")
	case inv.CoverageReport == "":
		return unmeasurableComponent(c, "the runner's instrumented variant names no coverage report")
	}
	rep, err := coverage.Measure(ctx, root, c.Dir, inv.CoverageReport, langOf(c), childEnv(tc, c, runner.Invocation{}))
	if err != nil {
		return unmeasuredComponent(c, err.Error())
	}
	if !rep.Lines.Measured() {
		return unmeasuredComponent(c, "the coverage report lists no coverable line")
	}
	return measurement{Name: c.Name, Dir: c.Dir, Lang: langOf(c), Lines: rep.Lines, Hits: rep.Hits}
}

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
func addCoverageRows(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir string, decl component.File, ms []measurement, cfg config.Config, opts coverageOptions) {
	if !opts.Instrument {
		return
	}
	ordered := inDeclarationOrder(decl, ms, opts.Selected)
	if !opts.Gate {
		ungatedRows(rep, ordered)
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value: "not read — pass --gate-coverage to compare against it"})
		floorRows(rep, ordered, cfg.Coverage.Floor)
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
		ungatedRows(rep, ordered)
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "baseline",
			Value: "not gated", Detail: strings.Split(err.Error(), "\n")})
		floorRows(rep, ordered, cfg.Coverage.Floor)
		return
	}
	for _, row := range gated.Rows() {
		rep.Add(row)
	}
}

// inDeclarationOrder returns one measurement per declared component, in the
// order the file declares them.
//
// Every declared component, including one this invocation never selected:
// omitting the rest would make a narrowed run indistinguishable from a
// complete one. Declaration order rather than completion order, so two runs of
// one declaration produce the same document.
func inDeclarationOrder(decl component.File, ms []measurement, selected bool) []measurement {
	byName := make(map[string]measurement, len(ms))
	for _, m := range ms {
		byName[m.Name] = m
	}
	out := make([]measurement, 0, len(decl.Components))
	for _, c := range decl.Components {
		if m, ok := byName[c.Name]; ok {
			out = append(out, m)
			continue
		}
		// The only place a carryable measurement is made, and only when
		// affected selection is what left the component out. Everything else
		// in this file describes a component this run actually reached.
		m := unmeasuredComponent(c, "the component was not selected for this run")
		m.Carryable = selected
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
func ungatedRows(rep *ui.Report, ms []measurement) {
	ungatedComponentRows(rep, ms)
	for _, l := range languages(ms) {
		rep.Add(ungatedComposedRow(string(l)+" coverage", ms, byLang(l)))
	}
	rep.Add(ungatedComposedRow("coverage", ms, everything))
}

// ungatedComponentRows is the per-component half, shared by the two runs that
// compare nothing: one that was never asked to, and one on the default branch
// where HEAD is its own merge-base.
func ungatedComponentRows(rep *ui.Report, ms []measurement) {
	for _, m := range ms {
		if !m.Measured() {
			rep.Add(unmeasuredRow("coverage("+m.Name+")", m.Why))
			continue
		}
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "coverage(" + m.Name + ")", Value: lineValue(m.Lines)})
	}
}

// ungatedComposedRow renders one language's or the repository's figure for a
// run that measured without gating.
//
// A figure no component contributed to is `unmeasured`, never a 0.0% context
// row. 0/0 renders as 0.0%, which reads as a real measurement of no coverage
// at all — the language whose only component failed would report the worst
// possible number as though it had been measured, and a reader acting on it
// would go looking for missing tests rather than for the failing suite.
func ungatedComposedRow(label string, ms []measurement, in func(measurement) bool) ui.Row {
	lines, measured, _ := composed(ms, nil, in)
	total := count(ms, in)
	if !lines.Measured() {
		return unmeasuredRow(label, fmt.Sprintf("none of its %d component(s) produced a measurement", total))
	}
	return ui.Row{Status: ui.StatusContext, Label: label, Value: composedValue(lines, measured, 0, total)}
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
		ungatedRows(rep, ms)
		floorRows(rep, ms, cfg.Coverage.Floor)
		// The row is written from what recording actually did, not from what
		// it was about to attempt. Recording declines for three reasons — a
		// run that measured nothing, a component with no entry to record, a
		// push that never landed — and all three report to stderr, which the
		// pull-request comment does not render. A row claiming a recording
		// that did not happen is the same untruth as a gate that did not run
		// reading as one that passed.
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
			Value:  "not read — HEAD is its own merge-base, so there is no earlier measurement to compare against",
			Detail: []string{"nothing was gated: this tree is the one a later change is measured against, and recording it is what this run is for"}})
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "record",
			Value: recordThisTree(ctx, cmd, dir, decl, ms, previousTreeBaseline(ctx, dir), cfg.Coverage.Tolerance)})
		return nil
	}

	baseline, err := baselineFor(ctx, cmd, rep, dir, base, opts)
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
		if lines, ok := baseline[m.Name]; ok && lines.Measured() {
			current[i] = measurement{Name: m.Name, Dir: m.Dir, Lang: m.Lang, Lines: lines}
			carried[m.Name] = true
		}
	}

	for _, m := range ms {
		rep.Add(componentRow(m, baseline, cfg.Coverage.Tolerance))
	}
	for _, l := range languages(ms) {
		rep.Add(composedRow(string(l)+" coverage", current, carried, baseline, byLang(l), cfg.Coverage.Tolerance))
	}
	rep.Add(composedRow("coverage", current, carried, baseline, everything, cfg.Coverage.Tolerance))

	if err := patchRows(ctx, cmd, rep, dir, base, ms, baseline, cfg); err != nil {
		return err
	}
	floorRows(rep, ms, cfg.Coverage.Floor)
	// `record` and not `baseline`: the two are different events in one run —
	// the baseline read this change is gated against, and the entry this
	// change leaves for the next one. Sharing a label would put two rows under
	// it, which is what a consumer keying rows by label cannot survive.
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "record",
		Value: recordThisTree(ctx, cmd, dir, decl, ms, baseline, cfg.Coverage.Tolerance)})
	return nil
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
func previousTreeBaseline(ctx context.Context, dir string) gitstate.Baseline {
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD~1")
	if err != nil {
		return nil
	}
	baseline, hit, err := gitstate.ReadBaseline(ctx, dir, tree)
	if err != nil || !hit {
		return nil
	}
	return baseline
}

// baselineFor reads the base tree's baseline, and measures it when there is
// none.
//
// A miss is resolved by measuring, never by substituting the nearest ancestor
// that happens to have an entry: which number a change is judged against would
// then depend on how far back history had one, which is not reproducible from
// the change itself.
func baselineFor(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir, base string, opts coverageOptions) (gitstate.Baseline, error) {
	tree, err := gitstate.TreeSHA(ctx, dir, base)
	if err != nil {
		tree = ""
	}
	baseline, hit, err := gitstate.ReadBaseline(ctx, dir, tree, base)
	if err != nil {
		return nil, err
	}
	if hit {
		return baseline, nil
	}
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "baseline",
		Value:  fmt.Sprintf("not cached for %s — measuring it now", shortSHA(base)),
		Detail: []string{"the first change against this base tree pays this cost"}})
	baseline, err = measureBaseTree(ctx, cmd, dir, base, opts)
	if err != nil {
		return nil, err
	}
	// Never cache a baseline that measured nothing. Cached, it is
	// indistinguishable from a real entry: every later change gets a cache
	// hit, reports every component as new, and the gate enforces nothing —
	// silently, permanently, with no way to self-heal. wardnet's state branch
	// accumulated nine of them. Measuring again next time is the strictly
	// better failure.
	if len(baseline) == 0 {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: measured no coverage at all at %s, so it was not cached as a baseline. The gate cannot compare against a baseline of nothing; fix what failed above and the next run measures it again.\n", base)
		return baseline, nil
	}
	if err := gitstate.WriteBaseline(ctx, dir, firstNonEmpty(tree, base), baseline); err != nil {
		// Best-effort: the baseline is already in hand and the gate below is
		// what matters. A write that never lands costs the next run the same
		// measurement, not a wrong verdict.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to cache the coverage baseline for %s: %v\n", base, err)
	}
	return baseline, nil
}

// measureBaseTree checks the base commit out into a throwaway worktree and
// measures it through the same path this command just took.
//
// The same path, deliberately: it starts the compose services each component
// declares, installs what each runner needs, and reads the report the
// instrumented variant wrote. Invoking coverage tooling directly instead is
// what produced a failed measurement for every suite that needs a database,
// which is one of the roads to an empty baseline cached as real.
func measureBaseTree(ctx context.Context, cmd *cobra.Command, dir, base string, opts coverageOptions) (gitstate.Baseline, error) {
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
		return nil, err
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
		return nil, fmt.Errorf("checking out %s to measure its baseline: %w", shortSHA(base), r.Err)
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
		return nil, fmt.Errorf("locating the scan root inside the repository: %w", err)
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
		return nil, fmt.Errorf("reading %s at %s: %w", config.FileName, shortSHA(base), err)
	}
	// Read leniently for the reason the configuration beside it is: a key this
	// version stopped reading is one the base tree still carries, and refusing
	// to measure it leaves the change gating against nothing.
	decl, err := component.LoadHistorical(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s at %s: %w", component.FileName, shortSHA(base), err)
	}
	if len(decl.Components) == 0 {
		return nil, nil
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
	envs, err := ensureToolchains(ctx, cmd, root, baseCfg, componentUnits(decl))
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

	out := gitstate.Baseline{}
	for _, m := range ms {
		if m.Measured() {
			out[m.Name] = m.Lines
			continue
		}
		// Named on stderr rather than dropped in silence, and the tail with
		// it: the worktree holding the log is removed on the way out, so this
		// is the only account of the failure that outlives this function.
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: the base tree at %s could not be measured for component %q: %s\n", shortSHA(base), m.Name, m.Why)
		for _, line := range tails[m.Name] {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", line)
		}
	}
	// The same predicate a run applies to what it records, over the same two
	// things: the measurements, and what came of them. Returned empty rather
	// than partial, so the caller's existing refusal to cache an empty
	// baseline covers this too — the next change measures the tree again,
	// which is slower and correct.
	if _, blocked := recordingBlockedBy(ms, out); blocked {
		return nil, nil
	}
	return out, nil
}

// componentRow gates one component against its own baseline entry.
func componentRow(m measurement, baseline gitstate.Baseline, tolerance float64) ui.Row {
	label := "coverage(" + m.Name + ")"
	base, hasBase := baseline[m.Name]
	if !m.Measured() {
		row := unmeasuredRow(label, m.Why)
		if m.Carryable && hasBase && base.Measured() {
			// Named, because this is the entry the composed figures below are
			// built from. A carried number that says nothing about itself is
			// one a reader takes for a measurement.
			row.Value += fmt.Sprintf(" — carrying the baseline's %.1f%% forward", base.Percent())
		}
		return row
	}
	pct := m.Lines.Percent()
	if !hasBase || !base.Measured() {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: lineValue(m.Lines) + ", no baseline yet"}
	}
	if regressedBeyond(pct, base.Percent(), tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, regressed %.1f%%", lineValue(m.Lines), base.Percent(), base.Percent()-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", lineValue(m.Lines), base.Percent())}
}

// composedRow gates a language, or the repository, against the same subset of
// the baseline it is composed from.
//
// The baseline side sums exactly the components the current side covers, so a
// run that measured three of four compares like with like. Summing the whole
// baseline instead would compare this run's three components against the base
// tree's four, and every narrowed run would read as a regression the size of
// the component it did not run.
func composedRow(label string, current []measurement, carried map[string]bool, baseline gitstate.Baseline, in func(measurement) bool, tolerance float64) ui.Row {
	lines, measured, carriedN := composed(current, carried, in)
	total := count(current, in)
	if !lines.Measured() {
		return unmeasuredRow(label, "no component in it produced a measurement")
	}
	var baseLines coverage.LineCount
	covered := 0
	var missing []string
	for _, m := range current {
		if !in(m) || !m.Lines.Measured() {
			continue
		}
		if b, ok := baseline[m.Name]; ok && b.Measured() {
			baseLines = baseLines.Add(b)
			covered++
			continue
		}
		missing = append(missing, m.Name)
	}
	value := composedValue(lines, measured, carriedN, total)
	// Compared only when the baseline covers every component this figure is
	// composed from. A partial comparison is a different quantity, and
	// rendering one as a comparison is the class of error this file is
	// arranged around: a newly declared component adds its lines to this side
	// and nothing to the other, so the figure would move by the size of the
	// component rather than by anything anyone did to the code.
	//
	// Which components are missing is named, because without it the row says
	// only that something is absent and a reader cannot tell a one-off — a
	// component declared by this very change — from a baseline that has been
	// broken for months.
	if covered != measured+carriedN {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: fmt.Sprintf("%s, no baseline yet for %s", value, strings.Join(missing, ", "))}
	}
	pct, basePct := lines.Percent(), baseLines.Percent()
	if regressedBeyond(pct, basePct, tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, regressed %.1f%%", value, basePct, basePct-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", value, basePct)}
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
func patchRows(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir, base string, ms []measurement, baseline gitstate.Baseline, cfg config.Config) error {
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
		return nil
	}
	// One diff for every component, partitioned below. All of them measure
	// the same range, and asking git once per component would pay for the
	// same walk N times to get N subsets of one answer.
	changed, err := coverage.ChangedLines(ctx, dir, base, exts...)
	if err != nil {
		return err
	}

	var parts []patchPart
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
			rep.Add(unmeasuredRow(label, fmt.Sprintf("%d changed line(s) across %d file(s), and no per-line coverage: %s", lines, len(scoped), m.Why)))
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: this change touches component %q and its patch coverage could not be measured — %s\n", m.Name, m.Why)
			continue
		}
		hit, total := coverage.PatchPercent(scoped, m.Hits)
		if total == 0 {
			// Every changed line is a comment, a blank, or something the
			// report has no entry for. There is no coverable line to gate.
			continue
		}
		rep.Add(patchRow(label, hit, total, baseline[m.Name], cfg.Coverage.Patch.Tolerance))
		parts = append(parts, patchPart{Name: m.Name, Lang: m.Lang, Hit: hit, Total: total, Base: baseline[m.Name]})
	}

	// Nothing of a language changed means no row for it. Silent, because a row
	// on every change that happened not to touch a language is the noise that
	// trains readers to skip the rows that matter.
	for _, l := range patchLanguages(parts) {
		rep.Add(composedPatchRow(string(l)+" patch", inLang(parts, l), cfg.Coverage.Patch.Tolerance))
	}
	if len(parts) > 0 {
		rep.Add(composedPatchRow("patch", parts, cfg.Coverage.Patch.Tolerance))
	}
	return nil
}

// patchPart is one component's contribution to a composed patch figure: the
// changed lines it had, how many of them the report covers, and the baseline
// that component is held to.
type patchPart struct {
	Name  string
	Lang  runner.Lang
	Hit   int
	Total int
	Base  coverage.LineCount
}

// composedPatchRow gates a language's, or the repository's, changed lines.
//
// It exists because the per-component rows and the aggregate rows between them
// still leave a hole. The aggregate says the repository did not get worse
// overall; the per-component patch rows say each component's new code met that
// component's own standard. Neither answers the question a reviewer actually
// has about a change spanning several components — was the new code in this
// change tested — and a change adding untested code to three components can
// clear every per-component row on tolerance and still be the change that
// should not merge.
//
// Summed over changed lines rather than averaged over components, for the
// reason ADR 0007 gives for the aggregate: a mean of percentages lets a
// two-line component outvote a two-hundred-line one.
//
// The baseline side sums exactly the components the current side covers, and a
// figure whose baseline does not cover all of them is reported rather than
// compared — the same rule composedRow follows, for the same reason. A
// component with no baseline contributes its new lines to the numerator and
// nothing to the comparison, which would read as movement nobody caused.
func composedPatchRow(label string, parts []patchPart, tolerance float64) ui.Row {
	var hit, total int
	var base coverage.LineCount
	var missing []string
	for _, p := range parts {
		hit += p.Hit
		total += p.Total
		if p.Base.Measured() {
			base = base.Add(p.Base)
			continue
		}
		missing = append(missing, p.Name)
	}
	pct := float64(hit) / float64(total) * 100
	counts := fmt.Sprintf("%.1f%% (%d/%d new lines), %d component(s)", pct, hit, total, len(parts))
	if len(missing) > 0 {
		return ui.Row{Status: ui.StatusNew, Label: label,
			Value: fmt.Sprintf("%s, no baseline yet for %s", counts, strings.Join(missing, ", "))}
	}
	basePct := base.Percent()
	if regressedBeyond(pct, basePct, tolerance) {
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%, below it by %.1f%%", counts, basePct, basePct-pct)}
	}
	return ui.Row{Status: ui.StatusPass, Label: label,
		Value: fmt.Sprintf("%s, baseline %.1f%%", counts, basePct)}
}

// patchLanguages is the languages that actually had changed lines, in a stable
// order so two runs of the same change render identically.
func patchLanguages(parts []patchPart) []runner.Lang {
	seen := map[runner.Lang]bool{}
	var out []runner.Lang
	for _, l := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript} {
		for _, p := range parts {
			if p.Lang == l && !seen[l] {
				seen[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

func inLang(parts []patchPart, l runner.Lang) []patchPart {
	var out []patchPart
	for _, p := range parts {
		if p.Lang == l {
			out = append(out, p)
		}
	}
	return out
}

// patchRow renders one component's patch verdict. The gate is that component's
// own aggregate baseline: patch coverage has no baseline of its own, and its
// tolerance is deliberately separate, so loosening the noisy aggregate knob
// never weakens the untested-new-code check.
func patchRow(label string, hit, total int, base coverage.LineCount, tolerance float64) ui.Row {
	pct := float64(hit) / float64(total) * 100
	counts := fmt.Sprintf("%.1f%% (%d/%d new lines)", pct, hit, total)
	switch {
	case !base.Measured():
		return ui.Row{Status: ui.StatusNew, Label: label, Value: counts + ", no baseline yet"}
	case regressedBeyond(pct, base.Percent(), tolerance):
		return ui.Row{Status: ui.StatusFail, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%", counts, base.Percent())}
	default:
		return ui.Row{Status: ui.StatusPass, Label: label,
			Value: fmt.Sprintf("%s, baseline %.1f%%", counts, base.Percent())}
	}
}

// scopeToComponent narrows a repository-wide changed-line map to the files
// this component's coverage can speak for: those under its directory, written
// in a language its runner implies.
//
// Both halves are needed. A component's report says nothing about a file
// outside its directory, and a repository declaring a Go and a TypeScript
// component over one root would otherwise score each against the other's
// changed files.
//
// A component rooted at the scan root claims every path under it, which is
// correct here and is not the question affected selection asks: this is
// "whose coverage report could contain this file", not "was this path
// understood".
func scopeToComponent(changed map[string][]int, m measurement) map[string][]int {
	exts := runner.SourceExtsFor(m.Lang)
	prefix := path.Clean(m.Dir)
	out := map[string][]int{}
	for file, lines := range changed {
		if !hasExt(file, exts) {
			continue
		}
		if prefix != "." && !strings.HasPrefix(file, prefix+"/") {
			continue
		}
		out[file] = lines
	}
	return out
}

func hasExt(file string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(file, e) {
			return true
		}
	}
	return false
}

// floorRows gates every measured component against coverage.floor.
//
// The floor has no baseline and no comparison against last time. The aggregate
// asks "is this worse than it was"; the floor asks "is this below the bar",
// which the repository states once and every component meets or does not.
// Ratcheting it against a prior value would make a component that has never
// had tests permanently acceptable, which is the gap it exists to close — and
// it is why the floor gates whether or not this run reads a baseline at all.
//
// The unit is the component, which is coarser than the crate or package the
// floor gated before. An untested crate inside a workspace still contributes
// its lines as uncovered and still drags its component's figure down in
// proportion to its size; what a component-level floor cannot catch is a
// *small* untested sub-unit. A repository that wants crate-level floors
// declares those crates as components, which is a statement about what it
// wants tested made in the file whose history records exactly that.
func floorRows(rep *ui.Report, ms []measurement, floor float64) {
	if floor <= 0 || len(ms) == 0 {
		return
	}
	// Only components a run could ever measure are in the denominator. A raw
	// `command:` is not a gap in this run's coverage, and counting it renders
	// a complete run as `1 of 2 component(s)` — the "N of M" shape that exists
	// so a partial run cannot read as a repository-wide pass, saying the
	// opposite of what happened. recordingBlockedBy already excludes them from
	// the same question.
	gateable := 0
	for _, m := range ms {
		if !m.Unmeasurable {
			gateable++
		}
	}
	below, cleared := 0, 0
	for _, m := range ms {
		if m.Unmeasurable {
			// Named, because a component the floor can never apply to is worth
			// knowing about — but never as a gap this run left.
			rep.Add(unmeasuredRow("floor("+m.Name+")", fmt.Sprintf("the %.1f%% floor cannot apply: %s", floor, m.Why)))
			continue
		}
		if !m.Measured() {
			// Never folded into the passing count, and never a failure: a
			// gate that did not run must be visibly distinct from one that
			// passed and from one that failed.
			rep.Add(unmeasuredRow("floor("+m.Name+")", fmt.Sprintf("the %.1f%% floor was not applied: %s", floor, m.Why)))
			continue
		}
		// Display precision, like every other comparison here: a component
		// printed as meeting the floor must never be failed for a difference
		// the report cannot show.
		if math.Round(m.Lines.Percent()*10) >= math.Round(floor*10) {
			cleared++
			continue
		}
		below++
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "floor(" + m.Name + ")",
			Value: fmt.Sprintf("%s, floor %.1f%%", lineValue(m.Lines), floor)})
	}
	if below > 0 {
		return
	}
	// A floor that cleared nothing examined nothing, whatever the count reads
	// like. Without this a run where every component's report was unreadable
	// renders `✓ floor … 0 of 4 component(s) at or above 80.0%` — a tick on a
	// gate that looked at no component at all, which is the rule this file is
	// arranged around, inverted.
	if cleared == 0 {
		rep.Add(unmeasuredRow("floor", fmt.Sprintf("no component was measured, so the %.1f%% floor was applied to none of them", floor)))
		return
	}
	// "N of M" rather than a bare count: the two differ exactly when some
	// component went ungated, so a partial run cannot read as a
	// repository-wide pass.
	rep.Add(ui.Row{Status: ui.StatusPass, Label: "floor",
		Value: fmt.Sprintf("%d of %d component(s) at or above %.1f%%", cleared, gateable, floor)})
}

// recordThisTree writes what this run measured as the baseline for the tree it
// measured.
//
// On a pull request HEAD is the merged result, and a squash merge lands a
// commit carrying exactly that tree — so this is already the baseline for the
// commit this change is about to become, measured by the pipeline that knows
// how to measure it. That is what removes the obligation to run on the default
// branch, an obligation a repository running CI only on pull requests never
// meets.
//
// Best-effort throughout: the gate has already reached its verdict, and a
// write that never lands costs the next run a measurement rather than a wrong
// answer.
// It returns what it did, in the words a row shows, so no caller can announce
// a recording that did not happen.
func recordThisTree(ctx context.Context, cmd *cobra.Command, dir string, decl component.File, ms []measurement, baseline gitstate.Baseline, tolerance float64) string {
	declared := make(map[string]bool, len(decl.Components))
	for _, c := range decl.Components {
		declared[c.Name] = true
	}
	record := gitstate.Baseline{}
	for _, m := range ms {
		if m.Measured() {
			record[m.Name] = m.Lines
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
		if lines, ok := baseline[m.Name]; ok && m.Carryable && lines.Measured() && declared[m.Name] {
			record[m.Name] = lines
		}
	}
	if len(record) == 0 {
		return "nothing to record — no component produced a measurement"
	}
	// A run that could not measure a component it was supposed to has not
	// established this tree's baseline, and recording a partial one is worse
	// than recording none: ReadBaseline reports a hit for any non-empty entry,
	// so the missing component reads as new on every later change — and
	// because a composed figure refuses to compare unless the baseline covers
	// every component in it, that language's row and the global row stop
	// gating too. Silently, and with no way to notice.
	//
	// Recording nothing leaves the next change a clean cache miss, which
	// measures the base tree. That is slower and correct.
	//
	// A component nothing could ever measure — a raw `command:`, a runner
	// naming no report — does not block it. It contributes to neither side of
	// any comparison, so its absence from the baseline is permanent and
	// expected rather than a gap this run created.
	if gap, blocked := recordingBlockedBy(ms, record); blocked {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: component %q has no coverage to record (%s), so this tree's coverage was not recorded — the next change against it measures the tree instead of gating against a baseline missing a component\n",
			gap.Name, gap.Why)
		return fmt.Sprintf("not recorded — %s has no coverage to record, and a baseline missing a component gates on nothing", gap.Name)
	}
	record = withToleratedDipsRestored(record, baseline, tolerance)
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD")
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not resolve this tree, so its coverage was not recorded: %v\n", err)
		return "not recorded — this tree could not be resolved"
	}
	// Merged onto whatever this tree already holds, never skipped because it
	// holds something: a re-run that measures more than the last one must not
	// be refused for finding an entry there.
	//
	// It does not make a sharded run record. A shard narrows with
	// `--component`, which leaves every other component with no entry and no
	// licence to carry one, so recordingBlockedBy above returns first and the
	// shard records nothing at all. That is deliberate for now — a shard has
	// measured part of a tree and cannot honestly claim the tree — and
	// `lydite test merge` is what will fold a sharded run into one document
	// and one recording.
	if existing, hit, _ := gitstate.ReadBaseline(ctx, dir, tree); hit {
		// Anchored against what this tree already holds as well as against the
		// base baseline. The same tree is the same content, so a difference
		// between two measurements of it is the measurement noise the
		// tolerance exists for — and without this a run on the default branch
		// re-measures a tree a pull request already recorded and replaces the
		// anchored high-water entry with a raw dipped one, handing the next
		// change a lowered number to gate against. That is the per-merge
		// ratchet withToleratedDipsRestored exists to prevent, reintroduced
		// one path over.
		record = withToleratedDipsRestored(record, existing, tolerance)
		merged := gitstate.Baseline{}
		for name, lines := range existing {
			if declared[name] {
				merged[name] = lines
			}
		}
		for name, lines := range record {
			merged[name] = lines
		}
		if len(merged) == len(existing) && sameCounts(merged, existing) {
			return fmt.Sprintf("%s already holds this measurement", shortSHA(tree))
		}
		record = merged
	}
	if err := gitstate.WriteBaseline(ctx, dir, tree, record); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to record this tree's coverage for %s: %v\n", tree, err)
		return "not recorded — the write to the " + gitstate.BranchName + " branch did not land"
	}
	return "recorded for " + shortSHA(tree)
}

// sameCounts reports whether two baselines hold the same entries, so a run
// that would rewrite a tree's entry byte for byte does not push to do it.
func sameCounts(a, b gitstate.Baseline) bool {
	if len(a) != len(b) {
		return false
	}
	for name, lines := range a {
		if other, ok := b[name]; !ok || other != lines {
			return false
		}
	}
	return true
}

// recordingBlockedBy names the component that stops this run establishing the
// tree's baseline, if any: one with no entry in what is about to be recorded.
//
// The question is asked of the record and not of the flags, which is the
// difference between a component that carried forward and one that was merely
// entitled to. A deselected component whose baseline entry does not exist
// carries nothing, so recording anyway would write the same gap forward on
// every merge — and it can then never heal, because each run reproduces it
// from the last.
//
// A component nothing could ever measure is the one exemption: it contributes
// to neither side of any comparison, so its absence is permanent and expected
// rather than a gap a run created.
func recordingBlockedBy(ms []measurement, record gitstate.Baseline) (measurement, bool) {
	for _, m := range ms {
		if m.Unmeasurable {
			continue
		}
		if _, ok := record[m.Name]; !ok {
			return m, true
		}
	}
	return measurement{}, false
}

// withToleratedDipsRestored returns record with every component whose coverage
// dipped below its baseline by no more than the tolerance restored to the
// baseline's counts.
//
// Recording the dipped number verbatim turns the tolerance into an unbounded
// downward ratchet: each change may dip up to the tolerance and pass, the
// recorded baseline follows it down, and the next change gets another free dip
// from the lower floor — coverage bleeds one tolerance per merge with every
// gate green. Anchoring to the high-water mark caps the total tolerated drift
// at one tolerance. A dip beyond the tolerance is recorded as measured: it
// failed visibly on the change that introduced it, so accepting it is a
// deliberate reset rather than leakage.
func withToleratedDipsRestored(record, baseline gitstate.Baseline, tolerance float64) gitstate.Baseline {
	out := make(gitstate.Baseline, len(record))
	for name, lines := range record {
		out[name] = lines
		b, ok := baseline[name]
		if !ok || !b.Measured() || !lines.Measured() {
			continue
		}
		if lines.Percent() < b.Percent() && !regressedBeyond(lines.Percent(), b.Percent(), tolerance) {
			out[name] = atPercentOf(b.Percent(), lines.Total)
		}
	}
	return out
}

// atPercentOf is the ratio to anchor to, expressed over the size the component
// actually is now.
//
// Writing the baseline's own counts back would freeze the component's *weight*
// as well as its ratio: a component that grew from 1,000 lines to 2,000 while
// dipping inside the tolerance would be recorded as 1,000 lines. The language
// and global baselines are sums of these counts, so that stale weight then
// decides how much this component counts towards a figure describing a tree it
// no longer matches — and the distortion compounds over successive
// within-tolerance merges. The anchor is the percentage; the size is this
// tree's.
func atPercentOf(pct float64, total int) coverage.LineCount {
	return coverage.LineCount{Covered: int(math.Round(pct / 100 * float64(total))), Total: total}
}

// regressedBeyond reports whether cur dipped below base by more than tolerance
// percentage points, compared at the report's display precision (tenths) so
// what is shown and what is gated always agree. A raw float subtraction
// decides an exactly-at-tolerance dip by representation noise, failing one
// "regressed 0.1%" while passing an identical-looking other.
func regressedBeyond(cur, base, tolerance float64) bool {
	return math.Round((base-cur)*10) > math.Round(tolerance*10)
}

// composed sums the counts of every measured component the predicate selects,
// splitting the contributors into those this run measured and those whose
// counts were carried forward from the baseline.
func composed(ms []measurement, carried map[string]bool, in func(measurement) bool) (coverage.LineCount, int, int) {
	var lines coverage.LineCount
	fresh, old := 0, 0
	for _, m := range ms {
		if !in(m) || !m.Lines.Measured() {
			continue
		}
		lines = lines.Add(m.Lines)
		if carried[m.Name] {
			old++
			continue
		}
		fresh++
	}
	return lines, fresh, old
}

func count(ms []measurement, in func(measurement) bool) int {
	n := 0
	for _, m := range ms {
		if in(m) {
			n++
		}
	}
	return n
}

func everything(measurement) bool { return true }

func byLang(l runner.Lang) func(measurement) bool {
	return func(m measurement) bool { return m.Lang == l }
}

// languages returns every language present in the declaration, sorted, so two
// runs of one declaration produce the same rows in the same order.
func languages(ms []measurement) []runner.Lang {
	seen := map[runner.Lang]bool{}
	var out []runner.Lang
	for _, m := range ms {
		if m.Lang == "" || seen[m.Lang] {
			continue
		}
		seen[m.Lang] = true
		out = append(out, m.Lang)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// lineValue renders a measurement the way every row shows it: the percentage,
// and the counts it came from. The counts are there because they are what the
// baseline stores and what the composed figures are summed from — a reader
// checking a composed number against its parts needs both.
func lineValue(c coverage.LineCount) string {
	return fmt.Sprintf("%.1f%% (%d/%d lines)", c.Percent(), c.Covered, c.Total)
}

// composedValue renders a language or global figure, and says what it is made
// of. A composed figure that does not say how much of it this run measured is
// indistinguishable from one that measured everything.
func composedValue(lines coverage.LineCount, measured, carried, total int) string {
	if carried == 0 {
		return fmt.Sprintf("%s, %d of %d component(s)", lineValue(lines), measured, total)
	}
	return fmt.Sprintf("%s, %d of %d component(s), %d carried forward", lineValue(lines), measured+carried, total, carried)
}

// unmeasuredRow is the shape every "this did not run" row takes: amber, never
// a vote, and carrying the reason rather than only the absence.
func unmeasuredRow(label, why string) ui.Row {
	return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "not measured — " + why}
}

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
