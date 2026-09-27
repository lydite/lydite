package run

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flaky"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// FlakyLabel is how every row about one component's new tests is named.
//
// Labelled the way TestLabel is, and for the same reason: a component called
// `flaky` must not be able to take this gate's row, and a consumer keying rows
// by label would silently lose one of the two.
func FlakyLabel(name string) string { return "flaky(" + name + ")" }

// NoSuiteFlakyRow is the flaky row of a component that declares no suite: there
// is no first run for a second one to disagree with.
func NoSuiteFlakyRow(name string) ui.Row {
	return unexaminedRow(FlakyLabel(name), NoSuiteReason)
}

// FlakyGate is the `--gate-flaky` run: what makes a test new, and what each
// component's two runs established about the tests it introduced.
//
// The merge-base and the paths the change touched are resolved once for the
// run, because they are facts about the change rather than about any
// component. Each component's verdict is filled in from the goroutine running
// that component — the rerun has to happen inside the component's own run,
// before its services come down — and every row is rendered afterwards, in
// declaration order.
type FlakyGate struct {
	// requested is the flag. A gate nobody asked for still takes a row per
	// component, as context: a section that quietly disappears is
	// indistinguishable from a concern that passed.
	requested bool
	// base is the revision a test is new against, and changed the paths this
	// change touched, relative to the scan root. An empty pair is a tree with
	// no change against its base, where nothing is new.
	base    string
	changed []string
	// why names what stopped the gate before any component ran. It is one
	// unmeasured row per component rather than an error, the way --affected's
	// unresolvable merge-base is not: this gate narrows nothing and skips no
	// suite, so a run that cannot examine it still ran everything it was asked
	// to.
	why string

	mu    sync.Mutex
	rows  map[string]ui.Row
	found []finding.Finding
}

// NewFlakyGate resolves what the gate needs before any component starts.
func NewFlakyGate(ctx context.Context, dir, baseBranch string, requested bool) *FlakyGate {
	g := &FlakyGate{requested: requested, rows: map[string]ui.Row{}}
	if !requested {
		return g
	}
	base, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
	if err != nil {
		g.why = "the merge-base could not be resolved, so no test can be called new: " + err.Error()
		return g
	}
	head, err := gitstate.HeadSHA(ctx, dir)
	if err != nil {
		g.why = "HEAD could not be resolved: " + err.Error()
		return g
	}
	// A tree that is its own merge-base has no change to introduce a test, so
	// every component reports no new tests. It is left holding no paths, which
	// says exactly that and asks git for no diff at all.
	if head == base {
		return g
	}
	touched, err := gitdiff.Changed(ctx, dir, base)
	if err != nil {
		g.why = "the change against " + shortSHA(base) + " could not be read: " + err.Error()
		return g
	}
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		g.why = "the scan root could not be located inside the repository: " + err.Error()
		return g
	}
	g.base = base
	for _, p := range touched.All {
		// The diff is repository-relative and a new test is decided over paths
		// relative to the scan root. A path outside that root declares no test
		// this run could rerun.
		if rel, inside := gitdiff.Rel(prefix, p); inside {
			g.changed = append(g.changed, rel)
		}
	}
	return g
}

// shortSHA is how a revision is named in a sentence a reader sees.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// gates reports whether this component's suite has to write a JUnit report for
// the gate to read its first outcomes from.
func (g *FlakyGate) gates(c component.Component) bool {
	return g != nil && g.requested && len(c.Command) == 0 && gatedRunner(c.Runner)
}

// gatedRunner is the runners the gate can examine: the four that write a
// JUnit report lydite installs nothing into the repository to obtain, and
// whose new tests a parser can enumerate statically.
//
// jest is outside it deliberately rather than by omission (ADR 0041): it ships
// no JUnit reporter, and installing jest-junit into a workspace lydite is
// about to gate is a scanner changing what the repository resolves to.
func gatedRunner(name runner.Name) bool {
	switch name {
	case runner.GoTest, runner.CargoNextest, runner.CargoLLVMCovNextest, runner.Vitest:
		return true
	}
	return false
}

// run reruns one component's new tests and records what the two runs
// established, from the goroutine running that component.
func (g *FlakyGate) run(ctx context.Context, root, dir string, c component.Component, inv runner.Invocation, tc *toolchain.Env, out Output) {
	if g == nil || !g.requested {
		return
	}
	row, found := g.examine(ctx, root, dir, c, inv, tc, out)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rows[c.Name] = row
	g.found = append(g.found, found...)
}

// examine is one component's verdict: which of its tests the change
// introduced, what a second run of them says, and the finding each
// disagreement makes.
func (g *FlakyGate) examine(ctx context.Context, root, dir string, c component.Component, inv runner.Invocation, tc *toolchain.Env, out Output) (ui.Row, []finding.Finding) {
	label := FlakyLabel(c.Name)
	// What the component is comes before what the run could resolve: a Rust
	// component is outside this slice whatever the checkout looks like, and a
	// row blaming the merge-base would send its author after the wrong thing.
	switch {
	case len(c.Command) > 0:
		// A raw command opts out of the derived variants, so there is no
		// invocation to filter down to a set of test names and no report to
		// read a first outcome from.
		return unexaminedRow(label, "the component declares a raw command, which opts out of the derived variants"), nil
	case !gatedRunner(c.Runner):
		// Named rather than skipped: a repository that asked for the gate and
		// got silence in one of its languages must be able to see that it did.
		return unexaminedRow(label, flakyGap(c)), nil
	case g.why != "":
		return unexaminedRow(label, g.why), nil
	}
	identity, build := flakyRerunner(c)
	tests, err := flaky.NewTests(ctx, root, g.base, measure.LangOf(c), path.Clean(c.Dir), componentPaths(c.Dir, g.changed))
	switch {
	case errors.Is(err, flaky.ErrNoMergeBase):
		return unexaminedRow(label, err.Error()), nil
	case err != nil:
		return unexaminedRow(label, "the tests this change introduces could not be read: "+err.Error()), nil
	case len(tests) == 0:
		return ui.Row{Status: ui.StatusPass, Label: label, Value: "no new tests"}, nil
	case inv.JUnitReport == "":
		return unexaminedRow(label, "the suite wrote no test report, so no first outcome could be read"), nil
	}
	// Run 1's report is read in the same key space the rerun's will be. Two
	// runs compared across two key spaces agree about nothing.
	run1, err := identity.ReadOutcomes(filepath.Join(dir, filepath.FromSlash(inv.JUnitReport)))
	if err != nil {
		return unexaminedRow(label, "the suite's own report could not be read: "+err.Error()), nil
	}
	results, err := flaky.Rerun(ctx, tests, flaky.Options{
		Root: root,
		Dir:  c.Dir,
		Args: c.Args,
		// The rerun goes through the same pinned wrapper run 1 did, so the
		// environment that found it is the environment that finds it again.
		Env:      ChildEnv(tc, c, inv),
		Run1:     run1,
		Identity: identity,
		Build:    build,
		Log:      out.W,
	})
	if err != nil {
		return unexaminedRow(label, "the rerun did not finish: "+err.Error()), nil
	}
	return flakyRow(label, c, results)
}

// flakyRerunner is how one component's language addresses a test and how its
// scope's new tests become a second invocation.
//
// The build closure is what keeps internal/flaky ignorant of any runner's
// argv: the three runners filter on entirely different things — a relative
// package pattern, an OR-ed exact-name expression, a file list and a title
// regexp — and an interface per call would say the same thing in more words.
func flakyRerunner(c component.Component) (flaky.Identity, func(string, []flaky.Test) (runner.Invocation, bool)) {
	switch c.Runner {
	case runner.CargoNextest, runner.CargoLLVMCovNextest:
		// The rerun is plain cargo-nextest either way: instrumentation is a
		// runner substitution rather than a flag (ADR 0016), so a component
		// whose ordinary variant already runs through cargo-llvm-cov reruns
		// its new tests exactly as an uninstrumented one does.
		return flaky.ByClassAndName, func(_ string, tests []flaky.Test) (runner.Invocation, bool) {
			return runner.RustRerun(c.Args, flakyNames(tests))
		}
	case runner.Vitest:
		return flaky.ByClassAndName, func(_ string, tests []flaky.Test) (runner.Invocation, bool) {
			return runner.VitestRerun(c.Args, flakyFiles(tests), flakyNames(tests))
		}
	default:
		return flaky.ByName, func(scope string, tests []flaky.Test) (runner.Invocation, bool) {
			pattern, err := relPackage(c.Dir, scope)
			if err != nil {
				return runner.Invocation{}, false
			}
			return runner.GoRerun(c.Args, pattern, flakyNames(tests))
		}
	}
}

// flakyNames is the distinct names a scope's new tests carry, in order.
//
// Distinct because one name recorded under two classnames is two tests and one
// filter term: `test(=shared_name)` selects the test in every binary declaring
// it, and naming it twice would only make the expression longer.
func flakyNames(tests []flaky.Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t.Name)
	}
	return out
}

// flakyFiles is the distinct files a scope's new tests were reported in, named
// the way the directory the rerun runs in names them.
//
// Taken from the classname, which for vitest is the file's own path relative
// to the component — the same shape a positional argument is resolved in,
// since the rerun runs in the component's directory. A test's declaration site
// is not the same question: a title declared in a helper the test file imports
// is reported under the file vitest ran, and it is that file the rerun has to
// name.
func flakyFiles(tests []flaky.Test) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tests {
		if t.Classname == "" || seen[t.Classname] {
			continue
		}
		seen[t.Classname] = true
		out = append(out, t.Classname)
	}
	return out
}

// relPackage turns a package directory relative to the scan root into the
// pattern `go test` takes in the component's own directory.
//
// A pattern and not an import path, because deriving one would need `go list`
// over a module the gate has not otherwise had to load, and a relative pattern
// names the same package for a component whose module path lydite never reads.
// It is spelled with a leading "./" for the reason cmd/go requires one: a bare
// `pkg` is a path in the module cache, and only `./pkg` is a directory here.
func relPackage(dir, pkg string) (string, error) {
	rel, err := filepath.Rel(filepath.FromSlash(dir), filepath.FromSlash(pkg))
	if err != nil {
		return "", fmt.Errorf("locating %s inside the component at %s: %w", pkg, dir, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "../") {
		return rel, nil
	}
	return "./" + path.Clean(rel), nil
}

// Report is one row per component this run was responsible for, in
// declaration order, and every finding the disagreements made.
//
// A component whose suite never ran still takes a row. Its new tests were not
// examined, and a gate that could not run never renders as one that passed.
func (g *FlakyGate) Report(own []component.Component) ([]ui.Row, []finding.Finding) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rows := make([]ui.Row, 0, len(own))
	for _, c := range own {
		switch row, ok := g.rows[c.Name]; {
		case DeclaresNoSuite(c):
			// Whether or not the gate was asked for: the row names the
			// declaration, which no flag changes.
			rows = append(rows, NoSuiteFlakyRow(c.Name))
		case ok:
			rows = append(rows, row)
		case !g.requested:
			rows = append(rows, ui.Row{Status: ui.StatusContext, Label: FlakyLabel(c.Name),
				Value: "not gated — --gate-flaky reruns the tests a change introduces"})
		default:
			rows = append(rows, unexaminedRow(FlakyLabel(c.Name), "the component's suite did not run, so there was nothing to rerun"))
		}
	}
	return rows, append([]finding.Finding(nil), g.found...)
}

// flakyRow is what one component's results establish, and the finding each
// disagreement makes.
//
// The strongest verdict owns the row and the weaker one is still said: a run
// holding both a disagreement and a test nothing could examine fails, with the
// unexamined count in the detail.
func flakyRow(label string, c component.Component, results []flaky.Result) (ui.Row, []finding.Finding) {
	var disagreed, unexamined, skipped int
	var detail []string
	var found []finding.Finding
	for _, r := range results {
		switch r.Verdict {
		case flaky.Disagreed:
			disagreed++
			detail = append(detail,
				fmt.Sprintf("%s: run 1 %s, run 2 %s", flakyName(r.Test), outcomeOf(r.Run1), outcomeOf(r.Run2)),
				"rerun in "+c.Dir+": "+r.Command)
			found = append(found, flakyFinding(label, c, r))
		case flaky.Unmeasured:
			unexamined++
			detail = append(detail, flakyName(r.Test)+" was not examined: "+r.Why)
		case flaky.Skipped:
			// Both runs skipped it, which agrees and examined nothing. It
			// counts toward "could not be measured" alongside Unmeasured,
			// never toward a pass: a component whose new tests all call
			// t.Skip() has run nothing twice, and rendering that as "2 runs
			// each" is exactly the gate-that-could-not-run-as-a-pass failure
			// this gate exists to refuse.
			skipped++
			detail = append(detail, flakyName(r.Test)+" was skipped in both runs")
		case flaky.Agreed:
		}
	}
	// No finding.Number pass: Site is the scope, the classname where the
	// language has one, and the test's name — which the report's own keys
	// already guarantee distinct, so two disagreements from one component can
	// never share a (Path, Site) pair for an ordinal to disambiguate. Ordinal
	// stays its zero value.
	unmeasured := unexamined + skipped
	row := ui.Row{Label: label, Detail: detail}
	switch {
	case disagreed > 0:
		row.Status = ui.StatusFail
		row.Value = fmt.Sprintf("%d of %d new test(s) disagreed between two runs", disagreed, len(results))
	case unmeasured == len(results):
		row.Status = ui.StatusUnmeasured
		row.Value = fmt.Sprintf("not examined — none of the %d new test(s) could be measured", len(results))
	case unmeasured > 0:
		// Some agreed and none disagreed, but a test this run could not run
		// twice — whether because nothing recorded it or because it skipped
		// both times — is not one it can call agreeing either: a pass here
		// would count a test that was never actually rerun among the ones
		// that were, and a gate that examined part of the change must not
		// render as one that examined all of it.
		row.Status = ui.StatusUnmeasured
		row.Value = fmt.Sprintf("%d of %d new test(s) could not be measured", unmeasured, len(results))
	default:
		row.Status = ui.StatusPass
		row.Value = fmt.Sprintf("%d new test(s), 2 runs each", len(results))
	}
	return row, found
}

// flakyFinding is the claim one disagreement makes, on the line the test is
// declared at.
//
// The site is the test's identity — the scope, the classname where a name
// alone is not one, and the name — so reformatting the file above the
// declaration does not re-identify the claim. The ordinal is zero because that
// triple is unique within a component: a name is unique in a Go package by the
// compiler's own rule, and elsewhere the classname is what tells two
// same-named tests apart.
func flakyFinding(label string, c component.Component, r flaky.Result) finding.Finding {
	return finding.Finding{
		Gate:      "flaky",
		Component: c.Name,
		Row:       label,
		Path:      r.Test.Path,
		Line:      r.Test.Line,
		Message:   flakyName(r.Test) + " disagreed between two runs",
		Detail: []string{
			"run 1: " + outcomeOf(r.Run1) + ", run 2: " + outcomeOf(r.Run2),
			"The rerun ran it alone, in " + c.Dir + ": " + r.Command,
			"Two runs disagreed. That is not a claim the test is random — a test that only passes because another test ran first disagrees here too, and is non-deterministic in the sense that matters.",
		},
		Site: r.Test.Scope + " " + flakyName(r.Test),
	}
}

// flakyName is how a row and a finding call one test.
//
// The classname comes first where there is one, because it is what tells two
// same-named tests in one component apart — `nextestprobe::a shared_name` and
// `nextestprobe::b shared_name` are two tests, and a row naming both
// `shared_name` says one thing twice. A declaration no parser could name is
// called by the only thing that identifies it: where it sits.
func flakyName(t flaky.Test) string {
	switch {
	case t.Unreadable:
		return fmt.Sprintf("the test declared at %s:%d", t.Path, t.Line)
	case t.Classname != "":
		return t.Classname + " " + t.Name
	}
	return t.Name
}

// outcomeOf names an outcome a report may not have recorded at all.
func outcomeOf(o *junit.Outcome) string {
	if o == nil {
		return "absent"
	}
	return o.String()
}

// flakyGap says why a component's runner is one the gate cannot examine.
//
// jest has a reason of its own rather than the general one, because it is a
// decision and not unshipped scope: it ships no JUnit reporter, and the only
// implementation is the one ADR 0029 refuses — installing jest-junit into the
// workspace lydite is about to gate.
func flakyGap(c component.Component) string {
	if c.Runner == runner.Jest {
		return "jest has no JUnit output lydite will install"
	}
	if measure.LangOf(c) != "" {
		return "the flaky gate has no second run for a " + string(c.Runner) + " suite"
	}
	return "this component declares no runner lydite knows"
}

// componentPaths is the changed paths that lie inside one component, which is
// the whole of what its own gate may look at.
//
// A component's row is about the tests it introduced, and flaky.NewTests reads
// whatever paths it is given: handed the whole diff, every component would
// rerun every other component's new tests and report them under its own name.
func componentPaths(dir string, changed []string) []string {
	dir = path.Clean(dir)
	if dir == "." {
		return changed
	}
	var out []string
	for _, p := range changed {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	return out
}

// unexaminedRow is a gate that examined nothing, with the cause beside it.
func unexaminedRow(label, why string) ui.Row {
	return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "not examined — " + why}
}
