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
	var asJSON, noColor, stream, onlyAffected bool
	var concurrency, baseBranch string
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

Mutants come only from lines in the change against the merge-base, and only
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

			// The same interrupt handling `lydite test` installs, and for the
			// same reason: this command starts a component's compose stack
			// and removes it in a deferred teardown, so a signal that skipped
			// those defers would leave one stack per started component
			// holding the ports the next run has to bind.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				stop() //lydite:equivalent its only effect is to restore the default disposition for the second interrupt, and a test that observed that would be a test signalling the test binary to death
			}()

			limit, err := resolveConcurrency(concurrency)
			if err != nil {
				return err
			}
			if timeout < 0 {
				return fmt.Errorf("--timeout must not be negative, got %s", timeout)
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

			// Mutation is diff-scoped always, so the merge-base is not a flag
			// this command can do without: an unresolvable one is an error
			// naming the fix rather than a run that quietly mutates nothing
			// and reports a pass.
			base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
			if err != nil {
				return fmt.Errorf("mutation is scoped to the change against the merge-base, and it could not be resolved: %w"+
					"\n       a shallow checkout is the usual cause — fetch with depth 0", err)
			}

			selected := own
			var skipped map[string]ui.Row
			var ordered []component.Component
			if onlyAffected {
				res, err := selectAffected(ctx, dir, file, baseBranch)
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
				stream:  stream,
				// A run responsible for part of the declaration emits no
				// summary row, for the reason it emits no coverage(repo):
				// the figure counts over the whole repository, and a shard
				// holding two of four components would publish its own two
				// under a label about all of them.
				summary: len(components) == 0,
			}
			runMutation(ctx, rep, selected, ordered, skipped, cfg, envs, opts)
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
		"of the components this run is responsible for, mutate only those the change against the merge-base could have broken")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	// Overrides the budget derived from the component's own baseline. A
	// number nobody measured is what ADR 0027 refuses as a runtime budget;
	// this is the escape for a suite whose own timing is not representative,
	// and it is a flag rather than a key for the reason --concurrency is.
	cmd.Flags().DurationVar(&timeout, "timeout", 0,
		"how long one mutant's suite may run before it counts as killed; derived from the component's own baseline by default")
	cmd.Flags().BoolVar(&stream, "stream", false, "mirror each component's output to stderr as it runs, as well as to its log")
	return cmd
}

// annotationMarker is the equivalence declaration an author writes. One
// statement of the token, so the command's own help cannot drift from what
// internal/annotation reads.
const annotationMarker = annotation.Marker

// mutationMarker is that token as the help text quotes it.
const mutationMarker = "`" + annotationMarker + "`"

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
	stream  bool
	summary bool
}

// componentMutation is one component's outcome, kept alongside its row so the
// summary counts what the rows say.
type componentMutation struct {
	summary mutation.Summary
	elapsed time.Duration
	ran     bool
}

// runMutation plans every selected component, runs its mutants and adds the
// rows in declaration order.
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
func runMutation(ctx context.Context, rep *ui.Report, selected, ordered []component.Component, skipped map[string]ui.Row, cfg config.Config, envs toolchain.Envs, opts mutationOptions) {
	plans := planComponents(ctx, opts.root, selected, "mutation", opts.stream)
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

	// The same rule runComponents applies, for the same reason: under
	// cancellation lydite cannot tell a suite that failed from one that was
	// killed, and a red row blaming a CI job timeout on the repository's
	// tests is the worst available answer.
	if ctx.Err() != nil {
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

	rep.Add(scheduleRow(ctx, outcome, len(plans), opts.limit))
	addRows(rep, rows, ordered, skipped, mutationLabel)
	if opts.summary {
		rep.Add(mutationSummaryRow(results))
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

	if !c.MutationEnabled() {
		// Present, so ADR 0026's completeness rule holds with no exception
		// and the fold needs no second copy of the opt-out rule to know which
		// absences are legitimate. Context and not unmeasured: the amber tag
		// is for a gate that could not run, and spending it on a decision the
		// repository stated deliberately is what teaches a reader to skim
		// past it.
		return ui.Row{Status: ui.StatusContext, Label: label, Value: "mutation is off for this component",
			Detail: []string{"`mutation: false` in " + component.FileName}}, out
	}
	lang := langOf(c)
	if len(c.Command) > 0 || lang == "" {
		return unmeasuredRow(label, "the component declares a raw command, so lydite cannot derive the build-only and plain variants a mutant needs"), out
	}
	inv, err := invocation(c, runner.Instrumented)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}, out
	}
	build, err := invocation(c, runner.BuildOnly)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}, out
	}
	suite, err := invocation(c, runner.Plain)
	if err != nil {
		return ui.Row{Status: ui.StatusFail, Label: label, Value: "not runnable", Detail: []string{err.Error()}}, out
	}
	backend, err := backendFor(lang, opts.root, c.Dir, build, suite, opts.files,
		func(ctx context.Context, dir string) error {
			// The runner's own preparation, in the worker rather than in the
			// component: a JavaScript workspace copied without its
			// node_modules fails at import, naming the tests rather than the
			// absent dependencies. Once per worker and never once per mutant,
			// which is the bound that makes a tree copy affordable.
			row, ok := prepare(ctx, suite, dir, label, c, cfg, tc, log)
			if !ok {
				return errors.New(strings.Join(row.Detail, "; "))
			}
			return nil
		})
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	// Before anything is prepared, started or run. Half of what bounds a
	// mutant is knowable from the diff alone, and a component the change
	// does not touch has no mutant whatever its coverage says — so the
	// baseline suite, the compose stack and the setup commands are all pure
	// cost there. On the default branch, where HEAD is its own merge-base,
	// that is every component.
	scoped := scopeToComponent(opts.changed, measurement{Name: c.Name, Dir: c.Dir, Lang: lang})
	if len(scoped) == 0 {
		return unmeasuredRow(label, "this change touches no source this component is written in"), out
	}

	// A runner whose instrumented variant names no report can supply no
	// executed lines, and mutation needs those as much as it needs a passing
	// baseline. Reported rather than attempted: `clearReport` joins the
	// report path onto the component's directory, so an empty one names the
	// directory itself, and asking it to clear that is asking to remove the
	// component.
	if inv.CoverageReport == "" {
		return unmeasuredRow(label,
			"the runner's instrumented variant names no coverage report, so there are no executed lines to mutate"), out
	}

	dir := filepath.Join(opts.root, filepath.FromSlash(c.Dir))
	if err := clearReport(dir, inv.CoverageReport); err != nil {
		return failure(label, log, err.Error(), "not runnable", ""), out
	}
	if prepared, ok := prepare(ctx, inv, dir, label, c, cfg, tc, log); !ok {
		return prepared, out
	}
	stop, started, ok := startServices(ctx, p, label)
	if !ok {
		return started, out
	}
	defer stop()
	defer func() {
		failed, ok := runCommands(context.WithoutCancel(ctx), dir, label, c, tc, "teardown", c.Teardown, log)
		if !ok && row.Status == ui.StatusPass {
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

	report, err := coverage.Measure(ctx, opts.root, c.Dir, inv.CoverageReport, lang, childEnv(tc, c, runner.Invocation{}))
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	mutants, err := generate(opts.root, c, report.Hits, scoped)
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

	results, err := mutation.Execute(ctx, backend, mutants, mutation.Options{
		Env:     childEnv(tc, c, suite),
		Timeout: budget(baseline, opts.timeout),
		Workers: workersFor(p, opts.limit),
		Slots:   slots,
		Log:     log.out,
	})
	if err != nil {
		return unmeasuredRow(label, err.Error()), out
	}
	var s mutation.Summary
	for _, r := range results {
		s.Add(r)
	}
	out = componentMutation{summary: s, elapsed: time.Since(baselineStarted), ran: true}
	return mutationRow(label, log, s, results, out.elapsed), out
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
func generate(root string, c component.Component, hits coverage.LineHits, scoped map[string][]int) ([]mutation.Mutant, error) {
	var out []mutation.Mutant
	lang := langOf(c)
	for _, file := range sortedFiles(scoped) {
		executed := hits[file]
		lines := map[int]bool{}
		for _, l := range scoped[file] {
			// Reported *and* executed. A line the report lists with a hit
			// count of zero is covered by no test, so a mutant on it survives
			// by construction and says nothing about the suite.
			if executed[l] > 0 {
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
		return "", fmt.Errorf("%s is not inside %s", file, dir) //lydite:equivalent the one caller abandons the file when the error is non-nil, so no path reads the string beside it and no test can be shown a different one
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
func mutationRow(label string, log *componentLog, s mutation.Summary, results []mutation.Result, elapsed time.Duration) ui.Row {
	killed, total := s.Score()
	if total == 0 {
		return detailed(unmeasuredRow(label, fmt.Sprintf(
			"%d mutant(s), none of which says anything about the suite: %s", s.Total(), aside(s))), log)
	}
	// The elapsed time is in the value rather than under the row, because it
	// is what a later runtime budget would be a multiple of and the fold has
	// no other channel to read it from — a report's rows carry rendered prose,
	// and mutation writes no measurements document beside them.
	row := ui.Row{Status: ui.StatusPass, Label: label, Log: log.Rel,
		Value: fmt.Sprintf("%d of %d mutant(s) killed in %s", killed, total, elapsed.Round(time.Second))}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	survivors := mutation.Survivors(results)
	if len(survivors) == 0 {
		return row
	}
	row.Status = ui.StatusFail
	row.Value = fmt.Sprintf("%d of %d mutant(s) survived in %s", len(survivors), total, elapsed.Round(time.Second))
	// Every survivor, not a sample: the author's next action is to write an
	// assertion for each one, and a truncated list makes that a second run to
	// discover the rest.
	row.Detail = nil
	for _, r := range survivors {
		row.Detail = append(row.Detail, r.Mutant.String())
	}
	if a := aside(s); a != "" {
		row.Detail = append(row.Detail, a)
	}
	row.Detail = append(row.Detail,
		"write the assertion that fails when the code changes this way, or declare the mutant equivalent with "+
			annotationMarker+" <reason> beside it")
	if log.Rel != "" {
		row.Detail = append(row.Detail, "full output: "+log.Rel)
	}
	return row
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
	return strings.Join(parts, ", ")
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
