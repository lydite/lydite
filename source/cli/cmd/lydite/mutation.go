package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/annotation"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// newMutationCmd asks whether each component's suite would notice if its own
// changed code were wrong.
//
// It is a peer of scan, test and review rather than a flag on `lydite test`,
// and the reason is a compilation. ADR 0016 puts every check for one component
// in one job because they share one; mutation does not — the coverage gate
// builds the instrumented variant once, and mutation builds the plain variant
// once per mutant. A flag would have made each test job serially longer by the
// most expensive thing lydite runs, inheriting a coupling whose justification
// does not apply.
//
// It waits on nothing. A mutant is meaningless against an already-red suite,
// so a run needs a passing baseline before it mutates anything, and the
// instrumented variant is both that baseline and where the executed lines come
// from. So each component's instrumented run happens here, once, and the test
// matrix's copy of it runs beside this one rather than ahead of it.
func newMutationCmd() *cobra.Command {
	var dir string
	var components []string
	var asJSON, noColor, stream, onlyAffected, declined, noGate bool
	var concurrency, baseBranch, baseSHA, memory string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:           "mutation",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Change each component's changed code and ask whether its tests notice",
		Long: `Mutate the lines this change touched, and run each component's suite against them.

Coverage measures execution. A test that calls a function and asserts nothing
scores full marks on every line it touches; a mutant is a single deliberate
change to one of those lines, and the suite is asked whether anything fails. A
change nothing notices is a survivor, and a survivor is the only outcome that
fails the gate.

Mutants come only from lines in the change against the base — the merge-base
with the base branch, or the commit --base-sha names — and only
from lines the component's own coverage reports as executed. There is no
whole-repository mode: it would run for hours on any mature codebase, and a
mutant on an uncovered line cannot be killed by construction.

An author who believes a survivor is unkillable declares it with a
` + mutationMarker + ` comment beside it. The declared mutant is generated,
counted and never run — and, because internal/referral reads the same token as
a suppression, declaring one refers the change to a human.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streamDiagnostics(asJSON)
			rep := ui.NewReport("mutation")

			// Checked before anything else in this RunE, and answered with
			// no component loaded, planned, scheduled or built: a repository
			// that told CI not to run mutation this run has nothing here to
			// measure, and the row says so rather than the section going
			// missing the way an unrun job would leave it. See ADR 0044.
			if declined {
				rep.Add(ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"})
				return renderReport(cmd, rep, dir, asJSON, noColor)
			}

			// The same interrupt handling `lydite test` installs, and for the
			// same reason: this command starts a component's compose stack
			// and removes it in a deferred teardown, so a signal that skipped
			// those defers would leave one stack per started component
			// holding the ports the next run has to bind.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				stop() // [lydite:exclude_from_mutation][its only effect is to restore the default disposition for the second interrupt, and a test that observed that would be a test signalling the test binary to death]
			}()

			limit, err := resolveConcurrency(concurrency)
			if err != nil {
				return err
			}
			if timeout < 0 {
				return fmt.Errorf("--timeout must not be negative, got %s", timeout)
			}
			maxMemory, err := parseBytes(memory)
			if err != nil {
				return fmt.Errorf("--memory: %w", err)
			}
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			file, err := component.Load(dir)
			if err != nil {
				return err
			}
			own, err := file.Select(components)
			if err != nil {
				return err
			}
			if len(file.Components) == 0 {
				rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "mutation",
					Value: "no components declared in " + component.FileName})
				return renderReport(cmd, rep, dir, asJSON, noColor)
			}
			envs, err := ensureToolchains(ctx, cmd, dir, cfg, componentUnits(own))
			if err != nil {
				return err
			}

			// Mutation is diff-scoped always, so the base is not something this
			// command can do without: an unresolvable one is an error naming
			// the fix rather than a run that quietly mutates nothing and
			// reports a pass. --base-sha names the commit outright and
			// --base-branch asks where this branch diverged from one; they
			// answer the same question two ways, which is why cobra refuses
			// both at once.
			base, err := resolveMutationBase(ctx, dir, baseBranch, baseSHA)
			if err != nil {
				return err
			}

			selected := own
			var skipped map[string]ui.Row
			var ordered []component.Component
			if onlyAffected {
				// From the base already resolved, never from the flag again:
				// selection answering about a different range than the mutants
				// were generated against would report a component untouched
				// while its own mutants ran, or the reverse.
				res, err := affectedFrom(ctx, dir, file, base)
				if err != nil {
					return err
				}
				selected = intersect(res.Selected, own)
				rep.Add(selectRow(res, len(file.Components)))
				skipped = make(map[string]ui.Row, len(res.Skipped))
				for _, c := range intersect(res.Skipped, own) {
					skipped[c.Name] = ui.Row{Status: ui.StatusUnmeasured,
						Label: mutationLabel(c.Name), Value: "not affected"}
				}
				ordered = own
			}

			// Every changed line in one call, partitioned per component
			// afterwards: they all measure the same range, and asking git
			// once per component is the same answer computed N times.
			changed, err := coverage.ChangedLines(ctx, dir, base)
			if err != nil {
				return err
			}

			// Only for a language whose mutants need a worker directory. A
			// repository of Go components pays no git walk for a copy it
			// never makes.
			var files []string
			if needsWorktree(selected) {
				if files, err = gitdiff.Tracked(ctx, dir); err != nil {
					return err
				}
			}

			opts := mutationOptions{
				root:    dir,
				changed: changed,
				files:   files,
				limit:   limit,
				timeout: timeout,
				memory:  maxMemory,
				stream:  stream,
				noGate:  noGate,
				// A run responsible for part of the declaration emits no
				// summary row, for the reason it emits no coverage(repo):
				// the figure counts over the whole repository, and a shard
				// holding two of four components would publish its own two
				// under a label about all of them.
				summary: len(components) == 0,
			}
			ran := runMutation(ctx, rep, selected, ordered, skipped, cfg, envs, opts)
			recordMutants(ctx, cmd, dir, ran)
			return renderReport(cmd, rep, dir, asJSON, noColor)
		},
	}
	cmd.AddCommand(newMutationMergeCmd())
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&components, "component", nil,
		"component this run is responsible for; repeatable, and every declared component by default")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	cmd.Flags().StringVar(&concurrency, "concurrency", strconv.Itoa(defaultConcurrency),
		`how many suites to run at once, or "max" for no bound`)
	cmd.Flags().BoolVar(&onlyAffected, "affected", false,
		"of the components this run is responsible for, mutate only those the change against the base could have broken")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	// The base a caller states rather than one lydite discovers. A post-merge
	// run is where the difference is load-bearing: on the default branch the
	// merge-base against the default branch is HEAD itself, so a run there has
	// no diff and nothing to mutate.
	cmd.Flags().StringVar(&baseSHA, "base-sha", "",
		"exact commit the change is measured against, as a SHA or a relative ref such as HEAD~1; "+
			"resolved with git rev-parse and never fetched, so no merge-base is computed")
	// Two answers to one question. Picking a winner silently would let a
	// mis-wired workflow mutate a range nobody asked for.
	cmd.MarkFlagsMutuallyExclusive("base-branch", "base-sha")
	// Overrides the budget derived from the component's own baseline. A
	// number nobody measured is what ADR 0027 refuses as a runtime budget;
	// this is the escape for a suite whose own timing is not representative,
	// and it is a flag rather than a key for the reason --concurrency is.
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"how long one mutant's suite may run before it counts as killed; derived from the component's own baseline by default")
	// The same escape as --timeout, for the other half of what bounds a
	// mutant: a suite whose own peak is not representative of what its mutants
	// need. A bound below what the component's baseline itself held leaves the
	// component unmeasured rather than reporting every mutant as killed.
	cmd.Flags().StringVar(&memory, "memory", "",
		"how much memory one mutant's suite may hold before it counts as killed, e.g. 4GiB; derived from the component's own baseline by default")
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	cmd.Flags().BoolVar(&declined, "declined", false,
		"write a report saying this repository declined mutation testing for this run, and do nothing else")
	// The post-merge run, where a survivor is history rather than a verdict:
	// the change has landed, the branch is gone, and the remedy a failing row
	// prints is addressed to a pull request that no longer exists. Only a
	// completed measurement stops voting — a variant that is not runnable, an
	// unresolvable base and every error still fail, which is the family a
	// `continue-on-error` on the step could not tell a survivor from. Spelled
	// as the negation of a default because mutation gates by default, the way
	// `lydite test`'s --no-coverage is. See ADR 0048.
	cmd.Flags().BoolVar(&noGate, "no-gate", false,
		"measure and record every mutant as usual, and let no survivor fail the command")
	return cmd
}

// resolveMutationBase is the commit this run's mutants come from the diff
// against, resolved once for the whole run.
//
// Each flag fails with its own value named. The two resolutions fail for
// different reasons and have different fixes — a branch that could not be
// fetched or merged-base against, and a revision this checkout does not hold —
// so one message covering both would name a cause the caller can act on only
// half the time.
func resolveMutationBase(ctx context.Context, dir, baseBranch, baseSHA string) (string, error) {
	if baseSHA != "" {
		base, err := gitstate.ResolveRevision(ctx, dir, baseSHA)
		if err != nil {
			return "", fmt.Errorf("mutation is scoped to the change against %s %s, and it could not be resolved: %w",
				gitstate.BaseSHAFlag, baseSHA, err)
		}
		return base, nil
	}
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		return "", fmt.Errorf("mutation is scoped to the change against the merge-base, and it could not be resolved: %w"+
			"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
	}
	return base, nil
}

// annotationMarker is the declaration an author writes to say no test could
// kill a mutant, in the form the help and a failing row quote it: the comment
// introducer, the token, and where the reason goes. One statement of it, so the
// command's own words cannot drift from what internal/annotation reads.
var annotationMarker = "// " + annotation.Marker(annotation.Mutation) + "[<reason>]"

// mutationMarker is that token as the help text quotes it.
var mutationMarker = "`" + annotationMarker + "`"

// mutationLabel is how every row about one component is named.
//
// Labelled rather than bare, for the reason a test row is: nothing forbids a
// component called `select`, `schedule` or `mutation`, and a consumer keying
// rows by label would silently lose the gate row to a component sharing its
// name.
func mutationLabel(name string) string { return "mutation(" + name + ")" }

// mutationOptions is what the command decided before anything ran.
type mutationOptions struct {
	root string
	// changed is every line the diff added, repository-wide, keyed by a path
	// relative to the scan root.
	changed map[string][]int
	// files is every path git knows about, scan-root relative: tracked, plus
	// untracked ones git is not ignoring. It is what a worker directory is
	// copied from, and it is read once for the run rather than once per
	// component — every component reads the same listing, and asking git per
	// component is the same answer computed N times.
	files   []string
	limit   int
	timeout time.Duration
	// memory overrides the ceiling derived from each component's own baseline,
	// in bytes, and zero asks for the derivation.
	memory int64
	stream bool
	// noGate is whether a completed component's outcome stops voting on the
	// exit code. True under --no-gate, where the mutants still run, the
	// findings are still emitted and mutants.json is still written. The zero
	// value keeps gating, so a caller that leaves this unset gets today's
	// behaviour rather than a silent, unasked-for --no-gate.
	noGate  bool
	summary bool
}

// componentMutation is one component's outcome, kept alongside its row so the
// summary counts what the rows say.
type componentMutation struct {
	summary  mutation.Summary
	elapsed  time.Duration
	ran      bool
	findings []finding.Finding
}

// recordMutants writes what this run made of its mutants beside its report.
//
// Unconditionally, and never only under a flag: a count that reaches the
// recording step only when somebody remembered one records nothing when they
// forget. A run that mutated no component writes a document naming its tree and
// holding no component, which is what a later fold needs to tell a shard that
// ran nothing from a shard whose job died.
//
// The tree is resolved out from under the run's cancellation, because a run cut
// short still reports the components that finished — the same rule the teardown
// above runs under. Every failure warns and none of them fails the command: the
// mutants ran, their verdict is in the report, and losing the byproduct is not
// a reason to discard it.
func recordMutants(ctx context.Context, cmd *cobra.Command, dir string, ran map[string]componentMutation) {
	tree, err := gitstate.TreeSHA(context.WithoutCancel(ctx), dir, "HEAD")
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not resolve this tree, so the mutant counts were not written: %v\n", err)
		return
	}
	if err := writeMutants(dir, mutantsFrom(tree, ran)); err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write the mutant counts: %v\n", err)
	}
}

// mutationLogKind names this command's per-component log, which openLog writes
// as `<reports>/<component>/mutation.log`. The fold reads that file back out of
// a shard's uploaded directory, so the name is one constant rather than a
// literal at each end.
const (
	mutationLogKind = "mutation"
	mutationLogName = mutationLogKind + ".log"
)

// runMutation plans every selected component, runs its mutants and adds the
// rows in declaration order.
//
// It returns what became of the mutants of every component that ran, keyed by
// component name. A component that did not run is absent from it rather than
// present with zeros, which is the distinction mutants.json exists to carry: a
// zeroed entry for a component nothing mutated reads, permanently, as a suite
// that killed everything.
//
// The components go through the same scheduler `lydite test` uses, under the
// same bound: a component is one item, so its compose stack is started and
// torn down inside that item, and two components publishing one host port are
// serialised there rather than here. What is new is the second bound inside
// each item — its mutants dispatch against slots shared by the whole run, so
// `--concurrency` means suite executions in flight exactly as it does in
// `lydite test`. Two independent bounds would multiply into components times
// mutants, which is the quadratic oversubscription defaultConcurrency is a
// constant rather than NumCPU to avoid.
func runMutation(ctx context.Context, rep *ui.Report, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, opts mutationOptions) map[string]componentMutation {
	plans := planComponents(ctx, opts.root, selected, mutationLogKind, opts.stream)
	for _, p := range plans {
		defer p.log.Close()
	}

	rows := make([]ui.Row, len(plans))
	results := make([]componentMutation, len(plans))
	var items []scheduler.Item
	var index []int
	for i, p := range plans {
		if !p.ready {
			rows[i] = p.row
			continue
		}
		rows[i] = ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  mutationLabel(p.c.Name),
			Value:  "not run",
			Detail: []string{"the run ended before this component started"},
		}
		items = append(items, itemFor(p))
		index = append(index, i)
	}

	slots := mutation.NewSlots(opts.limit)
	outcome := scheduler.Run(ctx, items, opts.limit, func(ctx context.Context, k int) {
		i := index[k]
		rows[i], results[i] = mutateComponent(ctx, plans[i], cfg, envs.For(plans[i].c.Name), slots, opts)
	})

	if ctx.Err() != nil {
		withdrawInterrupted(rows, results, index)
	}

	rep.Add(scheduleRow(ctx, outcome, len(plans), opts.limit))
	addRows(rep, rows, ordered, skipped, mutationLabel)
	// In plan order, so two runs of one declaration hand the same document to
	// whatever anchors these. A component the interrupt above reset carries
	// none: under cancellation a survivor cannot be told from a mutant whose
	// suite was killed, and a claim nobody can stand behind is worse than
	// none.
	for _, r := range results {
		rep.AddFindings(r.findings...)
	}
	if opts.summary {
		rep.Add(mutationSummaryRow(results))
	}
	// Plans and results are indexed in parallel, and `ran` is set only where a
	// component's mutants were generated and executed to a summary — including
	// the run withdrawInterrupted took back, which resets the result and with it
	// that flag.
	var ran map[string]componentMutation
	for i, p := range plans {
		if !results[i].ran {
			continue
		}
		if ran == nil {
			ran = map[string]componentMutation{}
		}
		ran[p.c.Name] = results[i]
	}
	return ran
}

// withdrawInterrupted takes back every failing verdict a cancelled run reached.
//
// The same rule runComponents applies, for the same reason: under cancellation
// lydite cannot tell a suite that failed from one that was killed, and a red
// row blaming a CI job timeout on the repository's tests is the worst available
// answer.
//
// The component's findings go with the row. A survivor is a claim that the
// suite passed with the code changed that way, and a suite that was killed
// established no such thing — so a claim left behind here would be one nobody
// can stand behind, anchored to a line, on a pull request.
func withdrawInterrupted(rows []ui.Row, results []componentMutation, index []int) {
	for _, i := range index {
		if rows[i].Status != ui.StatusFail {
			continue
		}
		rows[i] = ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  rows[i].Label,
			Value:  "not completed",
			Detail: []string{"the run was interrupted before this component finished"},
			Log:    rows[i].Log,
		}
		results[i] = componentMutation{}
	}
}

// addRows adds one row per component in declaration order, interleaving the
// ones selection skipped so a component sits where its author wrote it.
func addRows(rep *ui.Report, rows []ui.Row, ordered []component.Component, skipped map[string]ui.Row, label func(string) string) {
	if len(skipped) == 0 {
		for _, r := range rows {
			rep.Add(r)
		}
		return
	}
	byLabel := make(map[string]ui.Row, len(rows))
	for _, r := range rows {
		byLabel[r.Label] = r
	}
	for _, c := range ordered {
		if r, ok := skipped[c.Name]; ok {
			rep.Add(r)
			continue
		}
		if r, ok := byLabel[label(c.Name)]; ok {
			rep.Add(r)
		}
	}
}

// mutationTarget is everything a component needs before its first suite runs:
// the three invocations of its own declaration, the isolation strategy its
// mutants execute under, and the changed lines they may come from.
type mutationTarget struct {
	lang    runner.Lang
	inv     runner.Invocation
	suite   runner.Invocation
	backend mutation.Backend
	scoped  map[string][]int
	dir     string
}

// prepareMutation answers whether this component can be mutated at all, and
// with what.
//
// Every way it cannot is a row rather than an error, and every one of them is
// settled from the declaration and the diff before anything is prepared,
// started or run. Half of what bounds a mutant is knowable that way, and a
// component the change does not touch has no mutant whatever its coverage says
// — so the baseline suite, the compose stack and the setup commands are all
// pure cost there. On the default branch, where HEAD is its own merge-base,
// that is every component.
func prepareMutation(p componentPlan, cfg config.Config, tc *toolchain.Env, opts mutationOptions) (mutationTarget, ui.Row, bool) {
	c, log := p.c, p.log
	label := mutationLabel(c.Name)
	var t mutationTarget

	if !c.MutationEnabled() {
		// Present, so ADR 0026's completeness rule holds with no exception
		// and the fold needs no second copy of the opt-out rule to know which
		// absences are legitimate. Context and not unmeasured: the amber tag
		// is for a gate that could not run, and spending it on a decision the
		// repository stated deliberately is what teaches a reader to skim
		// past it.
		return t, ui.Row{Status: ui.StatusContext, Label: label, Value: "mutation is off for this component",
			Detail: []string{"`mutation: false` in " + component.FileName}}, false
	}
	t.lang = langOf(c)
	if len(c.Command) > 0 || t.lang == "" {
		return t, unmeasuredRow(label, "the component declares a raw command, so lydite cannot derive the build-only and plain variants a mutant needs"), false
	}
	// All three variants of one declaration, derived together: mutation needs
	// every one of them, and a component that cannot produce one can produce
	// no mutant at all. Built in a loop so the failure is written once rather
	// than three times, which is what a reader has to check three copies of.
	var build runner.Invocation
	for _, want := range []struct {
		variant runner.Variant
		into    *runner.Invocation
	}{
		{runner.Instrumented, &t.inv},
		{runner.BuildOnly, &build},
		{runner.Plain, &t.suite},
	} {
		inv, err := invocation(c, want.variant)
		if err != nil {
			return t, ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}, false
		}
		*want.into = inv
	}

	backend, err := backendFor(t.lang, opts.root, c.Dir, build, t.suite, opts.files,
		func(ctx context.Context, dir string) error {
			// The runner's own preparation, in the worker rather than in the
			// component: a JavaScript workspace copied without its
			// node_modules fails at import, naming the tests rather than the
			// absent dependencies. Once per worker and never once per mutant,
			// which is the bound that makes a tree copy affordable.
			//
			// No root of its own: dir is inside a copy of opts.root, not
			// opts.root itself, so the bound has to come from the copy's own
			// tree rather than from the repository it was copied from.
			row, ok := prepare(ctx, t.suite, dir, "", label, c, cfg, tc, log)
			if !ok {
				return errors.New(strings.Join(row.Detail, "; "))
			}
			return nil
		})
	if err != nil {
		return t, unmeasuredRow(label, err.Error()), false
	}
	t.backend = backend

	t.scoped = scopeToComponent(opts.changed, measurement{Name: c.Name, Dir: c.Dir, Lang: t.lang})
	if len(t.scoped) == 0 {
		return t, unmeasuredRow(label, "this change touches no source this component is written in"), false
	}
	// A runner whose instrumented variant names no report can supply no
	// executed lines, and mutation needs those as much as it needs a passing
	// baseline. Reported rather than attempted: `clearReport` joins the
	// report path onto the component's directory, so an empty one names the
	// directory itself, and asking it to clear that is asking to remove the
	// component.
	if t.inv.CoverageReport == "" {
		return t, unmeasuredRow(label,
			"the runner's instrumented variant names no coverage report, so there are no executed lines to mutate"), false
	}
	t.dir = filepath.Join(opts.root, filepath.FromSlash(c.Dir))
	return t, ui.Row{}, true
}

// mutateComponent runs one component's baseline, generates its mutants and
// reports what became of them.
//
// The baseline is the instrumented variant, which is both halves of what this
// needs: a suite that passes, and the lines coverage says were executed. A
// component whose baseline fails is unmeasured rather than failed — nothing
// can be concluded about tests that were not passing before the mutation, and
// failing here would report one broken suite as two red gates whose second
// names a cause its author clears by fixing the first.
func mutateComponent(ctx context.Context, p componentPlan, cfg config.Config, tc *toolchain.Env, slots *mutation.Slots, opts mutationOptions) (row ui.Row, out componentMutation) {
	c, log := p.c, p.log
	label := mutationLabel(c.Name)
	t, blocked, ok := prepareMutation(p, cfg, tc, opts)
	if !ok {
		return blocked, out
	}
	inv, suite, backend, scoped, dir := t.inv, t.suite, t.backend, t.scoped, t.dir
	lang := t.lang

	if err := clearReport(dir, inv.CoverageReport); err != nil {
		return failure(label, log, err.Error(), "not runnable", ""), out
	}
	if prepared, ok := prepare(ctx, inv, dir, opts.root, label, c, cfg, tc, log); !ok {
		return prepared, out
	}
	stop, started, ok := startServices(ctx, p, label)
	if !ok {
		return started, out
	}
	defer stop()
	defer func() {
		failed, ok := runCommands(context.WithoutCancel(ctx), dir, label, c, tc, "teardown", c.Teardown, log)
		if !ok && teardownFailureReplaces(row.Status) {
			row = failed
		}
	}()
	if failed, ok := runCommands(ctx, dir, label, c, tc, "setup", c.Setup, log); !ok {
		return failed, out
	}

	env := childEnv(tc, c, inv)
	baselineStarted := time.Now()
	// Under the run's slots, because a baseline is a suite execution exactly
	// as a mutant is. Counting only mutants would let three components in
	// their baseline run beside a fourth executing four mutants — seven
	// suites in flight under `--concurrency 4`, which is what one bound
	// exists to prevent.
	var res executil.Result
	if !slots.Run(ctx, func() {
		res = executil.RunOutput(ctx, dir, env, log.out, inv.Name, inv.Args...)
	}) {
		return unmeasuredRow(label, "the run was interrupted before this component's baseline suite ran"), out
	}
	if !res.Ok() {
		return detailed(unmeasuredRow(label,
			"the baseline suite did not pass, so nothing can be concluded about what a mutant would change"),
			log, tail(res.Output)...), out
	}
	baseline := time.Since(baselineStarted)
	// Both halves of what bounds a mutant come off the same run: the elapsed
	// time and the peak the kernel reported for it. The baseline itself runs
	// under no ceiling, because deriving one needs the measurement first.
	maxMemory := memoryBudget(res.MaxRSS, opts.memory)
	if !memoryFits(res.MaxRSS, maxMemory) {
		// Gating nothing rather than reporting a component whose suite kills
		// everything: under a ceiling the baseline alone already fills, every
		// mutant dies of the bound rather than of a test.
		return detailed(unmeasuredRow(label, fmt.Sprintf(
			"the baseline suite held %d byte(s), which leaves no room under the %d byte memory bound its mutants would run at",
			res.MaxRSS, maxMemory)), log), out
	}

	report, err := coverage.Measure(ctx, opts.root, c.Dir, inv.CoverageReport, lang, childEnv(tc, c, runner.Invocation{}))
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	mutants, err := generate(opts.root, c, report.Executed, scoped)
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	if len(mutants) == 0 {
		// A run with nothing to mutate must not report a pass: a green row
		// from a gate that examined nothing is indistinguishable from one
		// that examined everything.
		return detailed(unmeasuredRow(label,
			"no line this change touched is both mutable and reported as executed"), log), out
	}

	timeout, workers := budget(baseline, opts.timeout), workersFor(p, opts.limit)
	// Into the live stream, where the per-mutant lines go, and not only into
	// the document: a run too large to finish is killed by its job timeout and
	// writes no document at all, so a projection the document alone carried is
	// one the reader who needs it never sees. ADR 0027 refuses a runtime
	// budget, and this is not one — nothing here stops a run.
	_, _ = fmt.Fprintln(log.out, costProjection(len(mutants), workers, timeout))

	results, err := mutation.Execute(ctx, backend, mutants, mutation.Options{
		Env:       childEnv(tc, c, suite),
		Timeout:   timeout,
		MaxMemory: maxMemory,
		Workers:   workers,
		Slots:     slots,
		Log:       log.out,
	})
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	var s mutation.Summary
	for _, r := range results {
		s.Add(r)
	}
	out = componentMutation{summary: s, elapsed: time.Since(baselineStarted), ran: true}
	row, findings := mutationRow(label, c.Name, c.Dir, log, s, results, scoped, out.elapsed)
	out.findings = findings
	return completedRow(row, opts.noGate), out
}

// completedRow is what a component whose mutants ran is worth to the exit code.
//
// Under --no-gate it is worth nothing, and the row says so as StatusContext —
// whether or not it had survivors. A component that killed every mutant renders
// the same glyph as one that did not, because a ✓ is the claim that a gate
// examined this component and cleared it, and no gate examined anything. The
// value and the detail are untouched: the numbers and the surviving mutants are
// the whole point of the run.
//
// Only a pass and a survivor are converted. A row that reached here already
// non-voting — a denominator of zero — was never a measurement this flag has
// anything to say about, and every way a component could not run at all is a
// row mutateComponent returned before this.
func completedRow(row ui.Row, noGate bool) ui.Row {
	if !noGate {
		return row
	}
	if row.Status == ui.StatusPass || row.Status == ui.StatusFail {
		row.Status = ui.StatusContext
	}
	return row
}

// teardownFailureReplaces says whether a component's teardown failing takes
// over its row.
//
// It does where the row is the component's own completed measurement: the
// mutants said what they had to say, and a stack the teardown left running is
// then the only thing left to report. StatusContext is that same measurement
// under --no-gate, and a teardown failure is a command that did not run rather
// than a measurement, so the flag does not excuse it.
//
// A row that already names why the component could not be measured keeps that
// reason, and so does a survivor a gating run is failing on: both are upstream
// of the teardown, and the cause a reader acts on is the one they name.
func teardownFailureReplaces(status ui.Status) bool {
	return status == ui.StatusPass || status == ui.StatusContext
}

// backendFor is the isolation strategy one language's mutants are run under.
//
// A language with no backend is unmeasured with the reason said out loud,
// never skipped: a component silently absent from a mutation report reads as
// one whose suite killed everything.
func backendFor(lang runner.Lang, root, dir string, build, suite runner.Invocation, files []string, prep func(context.Context, string) error) (mutation.Backend, error) {
	switch lang {
	case runner.Go:
		// Go needs no worker directory at all: an overlay names the mutated
		// file wherever it is written, so every other path resolves in the
		// component's own tree and nothing is copied.
		return mutation.Go{Dir: filepath.Join(root, filepath.FromSlash(dir)), Build: build, Suite: suite}, nil
	case runner.Rust, runner.TypeScript:
		// A scan root whose files git lists none of has nothing to copy, and
		// a worker holding an empty tree would report every mutant unviable
		// with a compiler error nobody could act on.
		//
		// It asks about the scan root rather than about this component's own
		// subtree, and the weaker question is the one worth asking: a mutant
		// exists only for a file in the change, which is therefore tracked,
		// therefore listed and therefore copied — so a worker whose component
		// directory holds nothing is not reachable from a run that has a
		// mutant to stage.
		if len(files) == 0 {
			return nil, errors.New("git lists no file under the scan root, so there is nothing to copy into a worker directory")
		}
		// The scan root, with the component's commands run at its own
		// directory inside the copy: a component's build routinely reads a
		// file above itself, and a worker holding the component alone makes
		// every one of its mutants unviable.
		return mutation.Tree{Root: root, Component: path.Clean(dir), Files: files, Build: build, Suite: suite, Prepare: prep}, nil
	default:
		return nil, fmt.Errorf("lydite has no mutation backend for %s yet", lang)
	}
}

// workersFor is how many of a component's mutants are staged at once.
//
// One, for a component declaring compose services. ADR 0016 rejects sharing a
// running service between concurrent suites — two suites against one database
// truncate each other's tables — and eight mutants against one component's
// stack is that exactly: it would surface as mutants surviving at random, so
// the score would vary run to run, which is worse than a slow one.
//
// The question is asked of scheduler.Conflicts with two of this component's
// mutants as items rather than of the port list directly, so the predicate
// that decides what may run beside what has one implementation. They carry the
// component's published ports, so they conflict exactly when it publishes one;
// they carry no directory, because a mutant is not a second tree.
func workersFor(p componentPlan, limit int) int {
	pair := []scheduler.Item{{Name: "mutant-a", Ports: p.ports}, {Name: "mutant-b", Ports: p.ports}}
	if len(scheduler.Conflicts(pair)) > 0 {
		return 1
	}
	return limit
}

// budget is how long one mutant's suite may run before it counts as killed.
//
// A multiple of what this run measured, never a number nobody measured: ADR
// 0027 refuses a runtime budget because every way of exceeding an invented one
// is bad, and a timeout that multiplies the component's own observed baseline
// is not that. Without one, TimedOut is an outcome nothing can produce.
//
// The floor is for a suite too fast to measure. A component whose baseline is
// forty milliseconds would otherwise give every mutant a budget shorter than
// the compiler takes to start, and every one of them would be reported as a
// hang.
func budget(baseline, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	// The floor as a clamp: a conditional whose boundary returns what the
	// other arm returns is a branch nothing can be asked about.
	return max(baseline*budgetFactor, minimumBudget)
}

// costProjection is what a run says it is about to cost, before it pays it.
//
// The basis is in the line and not only the number, because every term in it is
// a decision lydite made for this component — how many mutants the change
// yielded, what its own baseline bought each of them, and how many run at once
// — and a reader who can see the derivation can argue with it.
//
// It is a worst case: every mutant running to the whole of its budget, with no
// worker ever idle. A killed mutant costs a fraction of that, so a real run
// lands well under. Stating the ceiling is not capping it — ADR 0027 refuses a
// runtime budget, and nothing here stops a run.
func costProjection(mutants, workers int, timeout time.Duration) string {
	return fmt.Sprintf(costProjectionFormat,
		mutants, timeout.Round(time.Second), staged(mutants, workers),
		projectedCeiling(mutants, workers, timeout).Round(time.Second))
}

// costProjectionFormat is the projection's one spelling, written through
// Sprintf here and read back through Sscanf by the fold, which finds the line
// in a log a shard uploaded without a document. A reader holding its own copy
// of the wording agrees with the writer until either is edited, and the
// disagreement is silent: the fold simply stops finding the line.
const costProjectionFormat = "%d mutant(s), budget %s each, %d worker(s): at most %s"

// costProjectionIn returns the projection a mutation log carries, if it carries
// one.
//
// The whole line, verbatim, because what a reader is shown is what the run
// itself said — a projection restated in the fold's own words would be the
// fold making a claim about a run it never saw.
func costProjectionIn(line string) (string, bool) {
	line = strings.TrimSpace(line)
	var mutants, workers int
	var budget, ceiling string
	n, err := fmt.Sscanf(line, costProjectionFormat, &mutants, &budget, &workers, &ceiling)
	if err != nil || n != 4 {
		return "", false
	}
	return line, true
}

// projectedCeiling is the longest a run of this many mutants can take: as many
// rounds as it takes to stage them all, each round costing a whole budget.
func projectedCeiling(mutants, workers int, timeout time.Duration) time.Duration {
	w := staged(mutants, workers)
	rounds := (mutants + w - 1) / w
	return time.Duration(rounds) * timeout
}

// staged is how many mutants actually run at once, which is the executor's own
// clamp: never fewer than one, and never more than there are mutants to stage.
// A projection over an unclamped count states a parallelism the run does not
// have, which is the direction that understates the cost.
func staged(mutants, workers int) int { return min(max(workers, 1), max(mutants, 1)) }

// memoryBudget is how much memory one mutant's suite may hold before it counts
// as killed.
//
// A multiple of the component's own measured baseline, never a share of the
// machine's memory: a bound that moved with the machine would make a mutant
// killed on a small runner and surviving on a large one, so the verdict would
// stop meaning one thing. The machine is what sets the floor's value instead.
//
// The floor is for a suite whose own peak is small. Four times a forty-megabyte
// baseline is a ceiling a compiler reaches on its own, and every mutant would
// be reported as one that allocated without stopping.
func memoryBudget(baseline, override int64) int64 {
	if override > 0 {
		return override
	}
	// The floor as a clamp, for the reason budget's is: a conditional whose
	// boundary returns what the other arm returns is a branch nothing can be
	// asked about.
	return max(baseline*memoryFactor, minimumMemory)
}

// memoryFactor is how much more than the baseline's own peak a mutant may hold.
//
// Four rather than the timeout's three because memory is the less elastic of
// the two: a suite is routinely slower under a mutation and is rarely four
// times larger. The error that matters is one-sided — a bound too tight kills a
// mutant nothing about the tests killed, and an inflated score is permanent and
// silent where a false survivor is an author's afternoon.
const memoryFactor = 4

// minimumMemory is the floor under that multiple. Against defaultConcurrency's
// four slots it is 8GiB of a 16GB runner, with the agent, the toolchains and
// the page cache in the rest; --concurrency is what bounds the sum.
const minimumMemory = 2 << 30

// memoryHeadroom is how much of the derived bound the baseline itself may hold
// and the run still mean something.
//
// A mutant runs the plain variant of the suite the baseline ran instrumented,
// so a ceiling the baseline alone already fills is one every mutant reaches
// whatever its tests do — the run would report a component whose suite kills
// everything, from a bound nothing about the code justifies. Only an override
// can produce it: the derivation is four times that same peak.
const memoryHeadroom = 2

// memoryFits reports whether a component's baseline leaves room under the bound
// its mutants run at.
func memoryFits(peak, bound int64) bool { return peak*memoryHeadroom <= bound }

// budgetFactor is how much longer than the baseline a mutant may take.
//
// The baseline is the *instrumented* variant, which is the slower of the two —
// Go's -coverpkg=./... recompiles every package per test binary — so three
// times it is generous against the plain variant a mutant actually runs. What
// it has to separate is a suite that is merely slower under a mutation from
// one that is not going to finish.
const budgetFactor = 3

// minimumBudget is the floor under that multiple.
const minimumBudget = 60 * time.Second

// generate produces every mutant for one component: from the lines this change
// touched, intersected with the lines its own coverage reports as executed.
//
// Both bounds are load-bearing and neither is the other. The diff makes the
// cost proportional to the change rather than to the repository, which is what
// makes mutation affordable at all; the coverage intersection removes mutants
// that cannot be killed by construction, and reporting one would only restate
// what patch coverage already said about the same line.
func generate(root string, c component.Component, executed coverage.LineHits, scoped map[string][]int) ([]mutation.Mutant, error) {
	var out []mutation.Mutant
	lang := langOf(c)
	for _, file := range sortedFiles(scoped) {
		ran := executed[file]
		lines := map[int]bool{}
		for _, l := range scoped[file] {
			// Reported *and* executed. A line the report lists with a hit
			// count of zero is covered by no test, so a mutant on it survives
			// by construction and says nothing about the suite.
			if ran[l] > 0 {
				lines[l] = true
			}
		}
		if len(lines) == 0 {
			continue
		}
		// The generator works in component-relative paths, because that is
		// what the executor writes and what a compiler is pointed at; the
		// diff and the coverage report are both scan-root relative.
		rel, err := componentRelative(c.Dir, file)
		if err != nil {
			return nil, err
		}
		// Joined onto the scan root, which is what a component's dir is
		// relative to. Resolving it against this process's working directory
		// instead reads the right file only when lydite happens to be run
		// from the scan root, and silently generates nothing everywhere else.
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Dir), filepath.FromSlash(rel))) // #nosec G304 -- a source file of the component being mutated, named by git's own diff
		if err != nil {
			// A file in the diff that is no longer in the tree — deleted, or
			// renamed — has no source to mutate and is not an error about the
			// declaration.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		mutants, unmatched, err := mutation.Generate(lang, rel, src, lines)
		if err != nil {
			return nil, err
		}
		// Named on stderr, because their author believes they have answered a
		// survivor and nothing they can see says otherwise: the comment is
		// well formed, it carries a reason, and the mutant it was meant for
		// is generated and run anyway.
		for _, u := range unmatched {
			fmt.Fprintf(os.Stderr, "lydite: %s: %s\n", c.Name, u)
		}
		out = append(out, mutants...)
	}
	return out, nil
}

// componentRelative maps a scan-root-relative path onto the component it is
// inside. scopeToComponent has already established that it is.
func componentRelative(dir, file string) (string, error) {
	clean := path.Clean(dir)
	if clean == "." {
		return file, nil
	}
	rel, ok := strings.CutPrefix(file, clean+"/")
	if !ok {
		return "", fmt.Errorf("%s is not inside %s", file, dir) // [lydite:exclude_from_mutation][the one caller abandons the file when the error is non-nil, so no path reads the string beside it and no test can be shown a different one]
	}
	return rel, nil
}

func sortedFiles(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// mutationRow is one component's verdict.
//
// A survivor is the only outcome that fails it. It is a gate rather than a
// referral because the author clears it by writing the assertion that kills
// the mutant, which is work that improves the code — and CONTEXT.md uses this
// exact case to say what a gate is.
//
// A component with a denominator of zero is unmeasured rather than a pass:
// every mutant was unviable or acknowledged, so nothing was established about
// the suite, and a green row from a gate that examined nothing is
// indistinguishable from one that examined everything.
func mutationRow(label, component, dir string, log *componentLog, s mutation.Summary, results []mutation.Result, changed map[string][]int, elapsed time.Duration) (ui.Row, []finding.Finding) {
	killed, total := s.Score()
	if total == 0 {
		return detailed(unmeasuredRow(label, fmt.Sprintf(
			"%d mutant(s), none of which says anything about the suite: %s", s.Total(), aside(s))), log), nil
	}
	// The elapsed time is in the value rather than under the row, because a
	// reader deciding whether this component is worth mutating on every pull
	// request reads it beside the score it bought. It is prose for that reader
	// alone: mutants.json carries the same span as a number, and the fold and
	// the ledger both take it from there rather than from this sentence.
	row := ui.Row{Status: ui.StatusPass, Label: label, Log: log.Rel,
		Value: fmt.Sprintf("%d of %d mutant(s) killed in %s", killed, total, elapsed.Round(time.Second))}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	if n := unboundedNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	survivors := mutation.Survivors(results)
	if len(survivors) == 0 {
		return row, nil
	}
	row.Status = ui.StatusFail
	row.Value = fmt.Sprintf("%d of %d mutant(s) survived in %s", len(survivors), total, elapsed.Round(time.Second))
	findings := mutationFindings(label, component, dir, survivors, changed)
	// Every survivor, not a sample: the author's next action is to write an
	// assertion for each one, and a truncated list makes that a second run to
	// discover the rest.
	row.Detail = nil
	for _, f := range findings {
		row.Detail = append(row.Detail, f.Message)
	}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	if n := unboundedNote(results); n != "" {
		row.Detail = append(row.Detail, n)
	}
	row.Detail = append(row.Detail,
		"write the assertion that fails when the code changes this way, or declare the mutant equivalent with "+
			annotationMarker+" beside it")
	if log.Rel != "" {
		row.Detail = append(row.Detail, "full output: "+log.Rel)
	}
	return row, findings
}

// mutationFindings is the claim a failing mutation row makes, one per
// survivor.
//
// A mutant that was killed is evidence the suite works and no claim about the
// code, so only survivors become findings — the rule every gate here follows,
// that findings are emitted exactly where a claim is made the author must
// clear. An acknowledged mutant is not among them either: a human has already
// read that claim and answered it in the source.
//
// The site is what the mutant did rather than where it did it: the operator,
// the text replaced and the text replacing it. Two identical comparisons in
// one file produce two mutants alike in all of that, which is what the ordinal
// separates.
//
// Paths are made relative to the scan root, because a mutant's own path is
// relative to the component it came from and a claim that names one file from
// two roots is two claims.
func mutationFindings(label, component, dir string, survivors []mutation.Result, changed map[string][]int) []finding.Finding {
	out := make([]finding.Finding, 0, len(survivors))
	for _, r := range survivors {
		m := r.Mutant
		out = append(out, finding.Finding{
			Gate:      "mutation",
			Component: component,
			Row:       label,
			Path:      path.Join(dir, m.Path),
			Line:      m.Line,
			Message:   m.String(),
			Detail: []string{
				"This mutant survived: the suite passed with the code changed this way.",
				"Write the assertion that fails when it is, or declare the mutant equivalent with " +
					annotationMarker + " beside it.",
			},
			Site: string(m.Operator) + "\x1f" + finding.Normalise(m.Original) + "\x1f" + finding.Normalise(m.Mutated),
		})
	}
	finding.Number(out)
	// A mutant exists only on a line the change touched, so this establishes
	// what generation already guaranteed. It is asked anyway, so the rule
	// deciding how a claim reaches a change has one implementation.
	finding.Anchored(out, changed)
	return out
}

// aside names the mutants that are in the run and not in the denominator.
//
// Both are excluded because neither is evidence about the tests — one could not
// be built and the other has been declared unkillable — and both are worth
// seeing: a component whose mutants are mostly unviable is telling its reader
// about the generator, and one whose survivors are mostly acknowledged is
// telling them about the claims a human has been asked to read.
func aside(s mutation.Summary) string {
	var parts []string
	if s.Unviable > 0 {
		parts = append(parts, fmt.Sprintf("%d did not compile", s.Unviable))
	}
	if s.Acknowledged > 0 {
		parts = append(parts, fmt.Sprintf("%d declared equivalent", s.Acknowledged))
	}
	if s.TimedOut > 0 {
		parts = append(parts, fmt.Sprintf("%d timed out", s.TimedOut))
	}
	if s.OutOfMemory > 0 {
		parts = append(parts, fmt.Sprintf("%d ran out of memory", s.OutOfMemory))
	}
	return strings.Join(parts, ", ")
}

// unboundedNote says on the row that the memory bound did not reach the
// mutants it was asked for.
//
// Darwin is where that happens: setrlimit refuses RLIMIT_DATA and RLIMIT_AS
// alike with EINVAL, at any value, so a mutant there runs bounded in time and
// unbounded in memory. Said out loud rather than left out, because a bound
// quietly not applied renders exactly the green of one that held.
func unboundedNote(results []mutation.Result) string {
	if !mutation.Unbounded(results) {
		return ""
	}
	return "memory was not bounded: this platform has no limit to set, so a mutant that allocates without stopping was held to nothing"
}

// parseBytes reads a byte quantity as a flag spells one: a plain number of
// bytes, or one with a binary suffix.
//
// Binary rather than decimal for every suffix, including the bare K, M and G:
// the quantity is compared against a peak the kernel reports in pages, and a
// tool whose GB and GiB were a 7% different ceiling would be one nobody could
// reason about from the row.
func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	digits, shift := s, 0
	for i, suffix := range []string{"K", "M", "G"} {
		for _, spelling := range []string{suffix, suffix + "B", suffix + "iB"} {
			if cut, ok := strings.CutSuffix(s, spelling); ok {
				digits, shift = strings.TrimSpace(cut), 10*(i+1)
			}
		}
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a byte quantity — write bytes, or a size like 4GiB: %w", s, err)
	}
	// A negative ceiling bounds nothing and a suffix that overflowed reads as
	// one, so both are refused where the number is read rather than where a
	// suite would fail under it.
	scaled := n << shift
	if n < 0 || scaled>>shift != n {
		return 0, fmt.Errorf("%q is not a memory bound a suite could run under", s)
	}
	return scaled, nil
}

// mutationSummaryRow counts the run, and gates nothing.
//
// There is no repository-wide figure only a fold can compute: survived == 0
// for every component is survived == 0 for the repository, so a gating row
// here could only restate the conjunction of the rows above it. What it
// carries is the counts and the elapsed time, which is what makes a runtime
// budget a measured decision later rather than an invented number now.
func mutationSummaryRow(results []componentMutation) ui.Row {
	var total mutation.Summary
	var elapsed time.Duration
	ran := 0
	for _, r := range results {
		if !r.ran {
			continue
		}
		ran++
		elapsed += r.elapsed
		total.Killed += r.summary.Killed
		total.TimedOut += r.summary.TimedOut
		total.OutOfMemory += r.summary.OutOfMemory
		total.Survived += r.summary.Survived
		total.Unviable += r.summary.Unviable
		total.Acknowledged += r.summary.Acknowledged
	}
	if ran == 0 {
		return ui.Row{Status: ui.StatusContext, Label: "mutation", Value: "no component was mutated"}
	}
	killed, denom := total.Score()
	row := ui.Row{Status: ui.StatusContext, Label: "mutation",
		Value: fmt.Sprintf("%d of %d mutant(s) killed across %d component(s) in %s",
			killed, denom, ran, elapsed.Round(time.Second))}
	if a := aside(total); a != "" {
		row.Detail = []string{a}
	}
	return row
}

// detailed hangs the tail of what a component printed under its row, and names
// the log holding the rest.
//
// The same three things a failing suite row carries, for the same reason: a
// reader looking at an amber row should not have to scroll past another
// component's container lifecycle to learn why, and the log is what survives
// when the tail is not enough.
func detailed(row ui.Row, log *componentLog, lines ...string) ui.Row {
	row.Detail = append(row.Detail, lines...)
	if log.Rel != "" {
		row.Detail = append(row.Detail, "full output: "+log.Rel)
		row.Log = log.Rel
	}
	return row
}

// needsWorktree reports whether any selected component's mutants are run in a
// copy of its tree rather than through an overlay.
func needsWorktree(selected []component.Component) bool {
	for _, c := range selected {
		switch langOf(c) {
		case runner.Rust, runner.TypeScript:
			return true
		}
	}
	return false
}
