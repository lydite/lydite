package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/ui"
)

func testLog(t *testing.T) *componentLog {
	t.Helper()
	log := openLog(t.TempDir(), "app", "mutation.log", false, 3)
	t.Cleanup(log.Close)
	return log
}

// The fold reads each component's score back out of the row a run rendered,
// because a report's rows carry prose and mutation writes no second document
// beside them. Nothing else holds the two together, so a wording change here
// is a fold that silently stops counting.
func TestTheFoldReadsBackTheScoreARunRendered(t *testing.T) {
	for _, c := range []struct {
		name          string
		s             mutation.Summary
		results       []mutation.Result
		killed, denom int
	}{
		{name: "every mutant killed", s: mutation.Summary{Killed: 7}, killed: 7, denom: 7},
		{
			name:    "one survivor",
			s:       mutation.Summary{Killed: 4, Survived: 1},
			results: []mutation.Result{{Mutant: mutation.Mutant{Path: "a.go", Line: 3}, Outcome: mutation.Survived}},
			killed:  4, denom: 5,
		},
		{
			name:   "a timeout counts as killed",
			s:      mutation.Summary{Killed: 2, TimedOut: 1, Unviable: 3, Acknowledged: 1},
			killed: 3, denom: 3,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := mutationRow(mutationLabel("app"), testLog(t), c.s, c.results, 42*time.Second)
			decl := component.File{Components: []component.Component{{Name: "app"}}}
			folded := foldedMutationRow([]shardInput{{read: true, doc: ui.Document{Rows: []ui.Row{row}}}}, decl)

			want := formatScore(c.killed, c.denom, 1, 42*time.Second)
			if folded.Value != want {
				t.Errorf("the fold read %q out of %q, want %q", folded.Value, row.Value, want)
			}
		})
	}
}

func formatScore(killed, denom, components int, elapsed time.Duration) string {
	return fmt.Sprintf("%d of %d mutant(s) killed across %d component(s) in %s", killed, denom, components, elapsed)
}

// A component declaring compose services runs its mutants one at a time: eight
// suites against one database truncate each other's tables, which surfaces as
// mutants surviving at random and a score that varies run to run.
func TestAComponentPublishingAPortMutatesSerially(t *testing.T) {
	if got := workersFor(componentPlan{ports: []int{5432}}, 8); got != 1 {
		t.Errorf("%d workers for a component publishing a port, want 1", got)
	}
	if got := workersFor(componentPlan{}, 8); got != 8 {
		t.Errorf("%d workers for a component publishing none, want the run's own bound", got)
	}
	// The predicate is the scheduler's own, so a component that publishes
	// nothing holds nothing: a second implementation here would answer
	// differently the day one of them learned about a port syntax.
	if len(scheduler.Conflicts([]scheduler.Item{{Name: "a"}, {Name: "b"}})) != 0 {
		t.Error("two items holding nothing were reported in conflict")
	}
}

// The budget multiplies something this run measured, which is what separates
// it from the invented runtime cap ADR 0027 refuses. Without one, TimedOut is
// an outcome nothing can produce.
func TestTheBudgetIsAMultipleOfTheMeasuredBaseline(t *testing.T) {
	long := 5 * time.Minute
	if got := budget(long, 0); got != long*budgetFactor {
		t.Errorf("budget(%s) = %s, want %s", long, got, long*budgetFactor)
	}
	// A suite too fast to measure would otherwise give every mutant a budget
	// shorter than the compiler takes to start, and every one of them would
	// be reported as a hang.
	if got := budget(40*time.Millisecond, 0); got != minimumBudget {
		t.Errorf("budget for a 40ms baseline = %s, want the %s floor", got, minimumBudget)
	}
	if got := budget(long, 7*time.Second); got != 7*time.Second {
		t.Errorf("--timeout was not honoured: %s", got)
	}
}

// A component's dir is relative to the scan root and so is git's diff; the
// generator works in component-relative paths, because that is what a compiler
// is pointed at.
func TestAChangedPathIsMappedOntoTheComponentItIsInside(t *testing.T) {
	for _, c := range []struct{ dir, file, want string }{
		{".", "cmd/main.go", "cmd/main.go"},
		{"source/cli", "source/cli/internal/a/a.go", "internal/a/a.go"},
		{"./source/cli", "source/cli/a.go", "a.go"},
	} {
		got, err := componentRelative(c.dir, c.file)
		if err != nil {
			t.Fatalf("%s in %s: %v", c.file, c.dir, err)
		}
		if got != c.want {
			t.Errorf("%s in %s = %q, want %q", c.file, c.dir, got, c.want)
		}
	}
	if _, err := componentRelative("web", "source/cli/a.go"); err == nil {
		t.Error("a path outside the component was mapped into it")
	}
}

// A line the coverage report lists with no hits is covered by no test, so a
// mutant on it survives by construction and would restate what patch coverage
// already said about the same line.
func TestOnlyAnExecutedLineIsMutated(t *testing.T) {
	root := t.TempDir()
	src := "package a\n\nfunc Less(x, y int) bool {\n\tif x < y {\n\t\treturn true\n\t}\n\treturn false\n}\n"
	write(t, root, "a.go", src)
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test"}

	// Line 4 holds the comparison; the report says it never ran.
	hits := coverage.LineHits{"a.go": {4: 0, 5: 1}}
	mutants, err := generate(root, c, hits, map[string][]int{"a.go": {4, 5}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutants {
		if m.Line == 4 {
			t.Errorf("an uncovered line was mutated: %s", m)
		}
	}
	// Line 5 did run, and it holds a `return true` the catalogue rewrites, so
	// the assertion above is not passing merely because nothing was generated.
	if len(mutants) == 0 {
		t.Fatal("no mutant was generated from the covered line either")
	}
}

// A file the diff names and the tree no longer holds — deleted, or renamed —
// has no source to mutate, and that is not an error about the declaration.
func TestAChangedFileThatIsGoneIsSkipped(t *testing.T) {
	root := t.TempDir()
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test"}
	mutants, err := generate(root, c, coverage.LineHits{"gone.go": {3: 1}}, map[string][]int{"gone.go": {3}})
	if err != nil {
		t.Fatalf("a deleted file failed the run: %v", err)
	}
	if len(mutants) != 0 {
		t.Errorf("%d mutant(s) from a file that is not there", len(mutants))
	}
}

// Present, so ADR 0026's completeness rule holds with no exception and the
// fold needs no second copy of the opt-out rule. Context and not unmeasured:
// the amber tag is for a gate that could not run, and spending it on a
// decision the repository stated teaches a reader to skim past it.
func TestAComponentThatOptedOutStillTakesARowAndRunsNothing(t *testing.T) {
	off := false
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test", Mutation: &off}
	plan := componentPlan{c: c, log: testLog(t)}
	row, out := mutateComponent(t.Context(), plan, config.Config{}, nil, nil, mutationOptions{root: t.TempDir()})

	if row.Status != ui.StatusContext {
		t.Errorf("status is %q, want %q", row.Status, ui.StatusContext)
	}
	if row.Label != mutationLabel("app") {
		t.Errorf("label is %q", row.Label)
	}
	if out.ran {
		t.Error("an opted-out component was run")
	}
}

// Go needs no worker directory: an overlay names the mutated file wherever it
// is written. Rust and TypeScript have no such instruction, so their mutants
// run in a copy of the component's tree — and a component git lists no file
// under has nothing to copy, which is said out loud rather than reported as a
// component whose suite killed everything.
func TestOnlyALanguageWithNoOverlayNeedsAWorkerDirectory(t *testing.T) {
	goComponent := component.Component{Name: "cli", Dir: ".", Runner: "go-test"}
	web := component.Component{Name: "web", Dir: ".", Runner: "vitest"}

	if needsWorktree([]component.Component{goComponent}) {
		t.Error("a Go component asked for a worker directory")
	}
	if !needsWorktree([]component.Component{goComponent, web}) {
		t.Error("a TypeScript component did not ask for a worker directory")
	}

	none := func(context.Context, string) error { return nil }
	backend, err := backendFor(runner.Go, t.TempDir(), runner.Invocation{}, runner.Invocation{}, nil, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Go); !ok {
		t.Errorf("Go got %T, want the overlay backend", backend)
	}
	backend, err = backendFor(runner.TypeScript, t.TempDir(), runner.Invocation{}, runner.Invocation{}, []string{"src/a.ts"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Tree); !ok {
		t.Errorf("TypeScript got %T, want the worker-directory backend", backend)
	}
	if _, err := backendFor(runner.Rust, t.TempDir(), runner.Invocation{}, runner.Invocation{}, nil, none); err == nil {
		t.Error("a component git lists no file under was given a worker directory to copy nothing into")
	}
}

// Half of what bounds a mutant is knowable from the diff alone, so a component
// the change does not touch pays for no baseline, no compose stack and no
// setup command. On the default branch that is every component.
func TestAnUntouchedComponentRunsNothing(t *testing.T) {
	c := component.Component{Name: "app", Dir: "cli", Runner: "go-test"}
	plan := componentPlan{c: c, log: testLog(t)}
	row, out := mutateComponent(t.Context(), plan, config.Config{}, nil, nil,
		mutationOptions{root: t.TempDir(), changed: map[string][]int{"web/app.ts": {1}}})

	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("status is %q, want %q", row.Status, ui.StatusUnmeasured)
	}
	if out.ran {
		t.Error("an untouched component was run")
	}
}

// mutationShard writes one shard's mutation report directory, the way a matrix
// job leaves it behind and the fold reads it back.
func mutationShard(t *testing.T, rows ...ui.Row) string {
	t.Helper()
	dir := t.TempDir()
	rep := ui.NewReport("mutation")
	for _, r := range rows {
		rep.Add(r)
	}
	f, err := os.Create(filepath.Join(dir, documentName("mutation"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runMutationMerge(t *testing.T, root string, reports ...string) (ui.Document, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	args := []string{"mutation", "merge", "--dir", root, "--no-color", "--json"}
	for _, r := range reports {
		args = append(args, "--reports", r)
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	var doc ui.Document
	if out.Len() > 0 {
		if decodeErr := json.Unmarshal(out.Bytes(), &doc); decodeErr != nil {
			t.Fatalf("the fold emitted no document: %v\n%s", decodeErr, out.String())
		}
	}
	return doc, err
}

func rowNamed(doc ui.Document, label string) (ui.Row, bool) {
	for _, r := range doc.Rows {
		if r.Label == label {
			return r, true
		}
	}
	return ui.Row{}, false
}

// A declared component with no row in any shard is a shard whose job died. It
// fails rather than reporting unmeasured, because an unmeasured row does not
// vote and would publish a passing verdict over a repository half of which was
// never mutated.
func TestAComponentNoShardMutatedFailsTheFold(t *testing.T) {
	root := mergeRepo(t)
	only := mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "4 of 4 mutant(s) killed in 12s"})

	doc, err := runMutationMerge(t, root, only)
	if err == nil {
		t.Error("the fold passed over a component no shard reported")
	}
	if doc.Verdict != ui.VerdictFail {
		t.Errorf("verdict is %q, want fail", doc.Verdict)
	}
	shards, ok := rowNamed(doc, "shards")
	if !ok {
		t.Fatal("the fold emitted no shards row")
	}
	if !strings.Contains(strings.Join(shards.Detail, " "), "b has no row") {
		t.Errorf("the shards row does not name the missing component: %v", shards.Detail)
	}
}

// Two shards reporting one component is two jobs running the same work, and a
// consumer keying rows by label picks one of two answers.
func TestAComponentTwoShardsMutatedFailsTheFold(t *testing.T) {
	root := mergeRepo(t)
	killed := ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "4 of 4 mutant(s) killed in 12s"}
	other := ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "1 of 1 mutant(s) killed in 2s"}

	doc, err := runMutationMerge(t, root, mutationShard(t, killed, other), mutationShard(t, killed))
	if err == nil {
		t.Error("the fold passed over a component two shards reported")
	}
	shards, _ := rowNamed(doc, "shards")
	if !strings.Contains(strings.Join(shards.Detail, " "), "a has a row in 2 shards") {
		t.Errorf("the shards row does not name the duplicate: %v", shards.Detail)
	}
}

// The summary sums the scores the shards rendered and gates nothing: survived
// == 0 for every component is survived == 0 for the repository, so a gating row
// could only restate the conjunction of the rows above it.
func TestTheFoldSumsTheShardsScoresAndGatesNothing(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "7 of 8 mutant(s) killed in 1m30s"}),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"}))
	if err != nil {
		t.Fatalf("a complete fold failed: %v", err)
	}
	summary, ok := rowNamed(doc, "mutation")
	if !ok {
		t.Fatal("the fold emitted no summary row")
	}
	if summary.Status != ui.StatusContext {
		t.Errorf("the summary is %q; it gates nothing", summary.Status)
	}
	want := "10 of 11 mutant(s) killed across 2 component(s) in 2m0s"
	if summary.Value != want {
		t.Errorf("summary = %q, want %q", summary.Value, want)
	}
	if doc.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q, want pass", doc.Verdict)
	}
}

// A survivor in a shard is what fails the run, and the fold carries that row
// rather than laundering it into a summary.
func TestASurvivorInAShardFailsTheFold(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		mutationShard(t, ui.Row{Status: ui.StatusFail, Label: mutationLabel("a"), Value: "1 of 8 mutant(s) survived in 1m0s",
			Detail: []string{"a.go:3:2: negate-conditional (\"<\" -> \">=\")"}}),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"}))
	if err == nil {
		t.Error("a survivor did not fail the fold")
	}
	row, ok := rowNamed(doc, mutationLabel("a"))
	if !ok || row.Status != ui.StatusFail {
		t.Fatalf("the survivor's row did not survive the fold: %+v", row)
	}
	if len(row.Detail) == 0 {
		t.Error("the fold dropped the survivor's detail, which is the only thing an author can act on")
	}
	// A survivor still counts in the summary: 8 - 1 killed of 8, plus b's 3.
	summary, _ := rowNamed(doc, "mutation")
	if !strings.HasPrefix(summary.Value, "10 of 11 mutant(s) killed") {
		t.Errorf("summary = %q, want the survivor counted in the denominator", summary.Value)
	}
}

// A row the fold has no rule for is carried through when the shards agree
// about it, because a row merge cannot arbitrate must not be silently reduced
// to one shard's copy.
func TestARowTheFoldHasNoRuleForIsCarried(t *testing.T) {
	root := mergeRepo(t)
	odd := ui.Row{Status: ui.StatusContext, Label: "toolchain", Value: "go 1.26.6"}
	doc, err := runMutationMerge(t, root,
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "1 of 1 mutant(s) killed in 1s"}, odd),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "1 of 1 mutant(s) killed in 1s"}, odd))
	if err != nil {
		t.Fatalf("a complete fold failed: %v", err)
	}
	carried := 0
	for _, r := range doc.Rows {
		if r.Label == "toolchain" {
			carried++
		}
	}
	if carried != 1 {
		t.Errorf("a row the shards agreed on appears %d times, want once", carried)
	}
	// And a disagreement is shown rather than arbitrated.
	doc, _ = runMutationMerge(t, root,
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "1 of 1 mutant(s) killed in 1s"}, odd),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "1 of 1 mutant(s) killed in 1s"},
			ui.Row{Status: ui.StatusContext, Label: "toolchain", Value: "go 1.25.0"}))
	carried = 0
	for _, r := range doc.Rows {
		if r.Label == "toolchain" {
			carried++
		}
	}
	if carried != 2 {
		t.Errorf("the shards disagreed and the fold kept %d row(s), want both", carried)
	}
}

// Completeness is a question about the declaration, and an empty one answers
// every question with yes.
func TestAFoldOverNoComponentIsRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	if _, err := runMutationMerge(t, root, mutationShard(t)); err == nil {
		t.Error("a fold over an empty declaration was allowed")
	}
}

func TestTheFoldNeedsAtLeastOneReportDirectory(t *testing.T) {
	if _, err := runMutationMerge(t, mergeRepo(t)); err == nil {
		t.Error("the fold ran with no --reports")
	}
}

// mutationRepo declares one component and returns a root a `lydite mutation`
// run can be pointed at.
func mutationRepo(t *testing.T, decl string) string {
	t.Helper()
	return gitRepoWithOrigin(t, map[string]string{
		".lydite/components.yml": decl,
		"app/.keep":              "",
		"web/.keep":              "",
	})
}

func runMutationCmd(t *testing.T, args ...string) (ui.Document, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"mutation", "--no-color", "--json"}, args...))
	err := cmd.Execute()
	var doc ui.Document
	if out.Len() > 0 {
		_ = json.Unmarshal(out.Bytes(), &doc)
	}
	return doc, errOut.String(), err
}

// A repository that declares no component is reported rather than silently
// passing: a run over nothing must not read like a run that mutated
// everything.
func TestARepositoryDeclaringNoComponentIsReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	doc, _, err := runMutationCmd(t, "--dir", root)
	if err != nil {
		t.Fatalf("the run failed rather than reporting: %v", err)
	}
	row, ok := rowNamed(doc, "mutation")
	if !ok || row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want an unmeasured mutation row", row)
	}
	if doc.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q; nothing was declared, which is not a failure", doc.Verdict)
	}
}

// A typo in a flag must not pay for a git walk first, and must not discard a
// report the run had already computed.
func TestAFlagIsRefusedBeforeAnyWorkHappens(t *testing.T) {
	root := mutationRepo(t, "components:\n  - name: app\n    dir: app\n    runner: go-test\n")
	// The message and not merely that something failed. A flag the command
	// never registered is refused by cobra as unknown, which is also an
	// error — so a test asserting only that one occurred passes over a run
	// that lost the flag entirely.
	for _, c := range []struct {
		name string
		args []string
		says string
	}{
		{"a concurrency that is not a number", []string{"--dir", root, "--concurrency", "lots"}, `--concurrency`},
		{"a concurrency below one", []string{"--dir", root, "--concurrency", "0"}, "at least 1"},
		{"a negative timeout", []string{"--dir", root, "--timeout", "-5s"}, "--timeout must not be negative"},
		{"a component that is not declared", []string{"--dir", root, "--component", "nope"}, "nope"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := runMutationCmd(t, c.args...)
			if err == nil {
				t.Fatal("the flag was accepted")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the run failed with %q, want it to say %q", err, c.says)
			}
		})
	}
}

// A component that opted out still takes a row, so ADR 0026's completeness
// rule holds with no exception — and nothing about it is executed, which is
// what lets this run without a toolchain at all.
func TestAnOptedOutComponentIsReportedByARealRun(t *testing.T) {
	root := mutationRepo(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    mutation: false\n")
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err != nil {
		t.Fatalf("an opted-out component failed the run: %v", err)
	}
	row, ok := rowNamed(doc, mutationLabel("app"))
	if !ok {
		t.Fatal("the opted-out component took no row, so a fold could not tell it from a shard that died")
	}
	if row.Status != ui.StatusContext {
		t.Errorf("row is %q, want context — the amber tag is for a gate that could not run", row.Status)
	}
	if _, ok := rowNamed(doc, "mutation"); !ok {
		t.Error("an unnarrowed run emitted no summary row")
	}
}

// A run responsible for part of the declaration emits no summary row, for the
// reason it emits no coverage(repo): the figure counts over the whole
// repository, and a shard would publish its own share under a label about all
// of it.
func TestANarrowedRunEmitsNoSummaryRow(t *testing.T) {
	root := mutationRepo(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    mutation: false\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n    mutation: false\n")
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--component", "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rowNamed(doc, "mutation"); ok {
		t.Error("a narrowed run emitted a summary row about the whole repository")
	}
	if _, ok := rowNamed(doc, mutationLabel("web")); ok {
		t.Error("the run reported a component it was not responsible for")
	}
}

// The summary counts only components that ran, and says how many did — a
// figure that did not say how much of the run it covers is
// indistinguishable from one that covered everything.
func TestTheSummaryCountsOnlyWhatRan(t *testing.T) {
	results := []componentMutation{
		{summary: mutation.Summary{Killed: 3, Survived: 1, Unviable: 2}, elapsed: 30 * time.Second, ran: true},
		{summary: mutation.Summary{Killed: 5, TimedOut: 1, Acknowledged: 1}, elapsed: 90 * time.Second, ran: true},
		{summary: mutation.Summary{Killed: 99}, elapsed: time.Hour, ran: false},
	}
	row := mutationSummaryRow(results)
	if row.Status != ui.StatusContext {
		t.Errorf("the summary is %q; it gates nothing", row.Status)
	}
	want := "9 of 10 mutant(s) killed across 2 component(s) in 2m0s"
	if row.Value != want {
		t.Errorf("summary = %q, want %q", row.Value, want)
	}
	// The mutants that say nothing about the suite are named rather than
	// folded into the score.
	if len(row.Detail) == 0 || !strings.Contains(row.Detail[0], "2 did not compile") ||
		!strings.Contains(row.Detail[0], "1 declared equivalent") ||
		!strings.Contains(row.Detail[0], "1 timed out") {
		t.Errorf("the aside does not name what is outside the denominator: %v", row.Detail)
	}
	if got := mutationSummaryRow(nil); got.Value != "no component was mutated" {
		t.Errorf("a run that mutated nothing says %q", got.Value)
	}
}

// Rows are in declaration order, never completion order: two runs over one
// declaration must produce the same document, and a component selection
// skipped sits where its author wrote it rather than ahead of every component
// that ran.
func TestRowsInterleaveIntoDeclarationOrder(t *testing.T) {
	declared := []component.Component{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	ran := []ui.Row{
		{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "ran"},
		{Status: ui.StatusPass, Label: mutationLabel("c"), Value: "ran"},
	}
	skipped := map[string]ui.Row{"b": {Status: ui.StatusUnmeasured, Label: mutationLabel("b"), Value: "not affected"}}

	rep := ui.NewReport("mutation")
	addRows(rep, ran, declared, skipped, mutationLabel)
	doc := documentOf(t, rep)

	var order []string
	for _, r := range doc.Rows {
		order = append(order, r.Label)
	}
	want := []string{mutationLabel("a"), mutationLabel("b"), mutationLabel("c")}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("rows are %v, want %v", order, want)
	}

	// With nothing skipped there is nothing to interleave, and the rows are
	// already in the order the scheduler's slots were filled in.
	rep = ui.NewReport("mutation")
	addRows(rep, ran, nil, nil, mutationLabel)
	if got := len(documentOf(t, rep).Rows); got != len(ran) {
		t.Errorf("%d row(s) with nothing skipped, want %d", got, len(ran))
	}
}

func documentOf(t *testing.T, rep *ui.Report) ui.Document {
	t.Helper()
	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var doc ui.Document
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// gitRepoWithOrigin builds a repository whose `origin` is a bare clone of
// itself, which is the smallest thing `gitstate.BaseSHA` can resolve a
// merge-base against.
//
// Mutation is diff-scoped always, so a run needs one: unlike `lydite test`,
// there is no mode that skips it. A file:// origin is what makes that
// resolvable in a test without a network.
func gitRepoWithOrigin(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		write(t, root, rel, body)
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=lydite", "GIT_AUTHOR_EMAIL=lydite@example.com",
			"GIT_COMMITTER_NAME=lydite", "GIT_COMMITTER_EMAIL=lydite@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	run(root, "init", "--quiet", "--initial-branch=main")
	run(root, "add", "-A")
	run(root, "commit", "--quiet", "-m", "seed")
	run(root, "init", "--quiet", "--bare", origin)
	run(root, "remote", "add", "origin", "file://"+origin)
	run(root, "push", "--quiet", "origin", "main")
	return root
}

// goModuleRepo is a component whose suite really runs: a Go module, a
// committed base, and a change on top of it. `go test` is what this process is
// already running under, so this needs nothing the suite does not already
// have.
func goModuleRepo(t *testing.T, added, addedTest string) string {
	t.Helper()
	return goModuleRepoWith(t, goModuleDecl, "", nil, added, addedTest)
}

// goModuleDecl is the one component the fixture declares unless a test needs
// another.
const goModuleDecl = "components:\n  - name: app\n    dir: .\n    runner: go-test\n    args: [\"./...\"]\n"

// goModuleRepoWith is goModuleRepo over a declaration of the test's choosing,
// with the module under dir — empty for the scan root — and extra files
// committed alongside it.
func goModuleRepoWith(t *testing.T, decl, dir string, extra map[string]string, added, addedTest string) string {
	t.Helper()
	at := func(rel string) string {
		if dir == "" {
			return rel
		}
		return dir + "/" + rel
	}
	// The base already holds well-tested code, so the mutants this run
	// generates come from the change alone. A fixture whose whole file is new
	// measures the base as well, which is the diff scope not being applied.
	files := map[string]string{
		".lydite/components.yml":  decl,
		at("go.mod"):              "module fixture\n\ngo 1.26.4\n",
		at("paths/paths.go"):      baseSource,
		at("paths/paths_test.go"): baseSuite,
	}
	for name, body := range extra {
		files[name] = body
	}
	root := gitRepoWithOrigin(t, files)
	write(t, root, at("paths/paths.go"), baseSource+added)
	write(t, root, at("paths/paths_test.go"), baseSuite+addedTest)
	for _, args := range [][]string{{"checkout", "--quiet", "-b", "change"}, {"add", "-A"},
		{"commit", "--quiet", "-m", "the change under test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=lydite", "GIT_AUTHOR_EMAIL=lydite@example.com",
			"GIT_COMMITTER_NAME=lydite", "GIT_COMMITTER_EMAIL=lydite@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// baseSource and baseSuite are what the merge-base already holds: code with
// assertions, so nothing in it is a survivor and nothing in it is in the diff.
const baseSource = `package paths

import "strings"

func Depth(target string) int {
	if len(target) == 0 {
		return 0
	}
	return strings.Count(target, "/") + 1
}
`

const baseSuite = `package paths

import "testing"

func TestDepth(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
	}{{"", 0}, {"a", 1}, {"a/b", 2}} {
		if got := Depth(c.in); got != c.want {
			t.Errorf("Depth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
`

// deeper is the change under test: one comparison, on a line the suite below
// executes.
const deeper = "\nfunc Deeper(a, b string) bool { return Depth(a) > Depth(b) }\n"

// The whole run, against a suite that really executes: the instrumented
// baseline, the coverage report read back, the mutants generated from the
// change intersected with what ran, and each one built and executed.
//
// The engine merged once with no caller, which is how a defect survives a
// clean build, a clean lint and a passing suite. This is the caller.
func TestASuiteThatAssertsKillsItsMutants(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestDeeper(t *testing.T) {
	if !Deeper("a/b", "a") {
		t.Error("a/b is deeper than a")
	}
	if Deeper("a", "a") {
		t.Error("a is not deeper than itself")
	}
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err != nil {
		t.Fatalf("a suite that kills every mutant failed the gate: %v\n%+v", err, doc.Rows)
	}
	row, ok := rowNamed(doc, mutationLabel("app"))
	if !ok {
		t.Fatalf("no row for the component: %+v", doc.Rows)
	}
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v, want a pass", row)
	}
	if !strings.Contains(row.Value, "mutant(s) killed") {
		t.Errorf("value = %q, want a kill count", row.Value)
	}
	if row.Log == "" {
		t.Error("the row names no log, so nothing a reader could open records what ran")
	}
}

// A test that calls a function and asserts nothing scores full marks on every
// line it touches. This is the whole reason the command exists, and it is the
// only outcome that fails the gate.
func TestASuiteThatAssertsNothingLeavesASurvivor(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestDeeperIsCalledAndNothingIsAsserted(t *testing.T) {
	Deeper("a/b", "a")
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err == nil {
		t.Fatalf("a survivor did not fail the run: %+v", doc.Rows)
	}
	row, _ := rowNamed(doc, mutationLabel("app"))
	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v, want a failure", row)
	}
	if !strings.Contains(row.Value, "survived") {
		t.Errorf("value = %q, want it to say what survived", row.Value)
	}
	// The survivor is named where the author is looking, with the next step
	// beside it — a row saying only that something survived is one they
	// cannot act on.
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "paths/paths.go:") {
		t.Errorf("the survivor is not located: %v", row.Detail)
	}
	if !strings.Contains(detail, annotationMarker) {
		t.Errorf("the row does not say how to answer a survivor: %v", row.Detail)
	}
}

// An author who believes a survivor is unkillable declares it beside the
// mutant. The declared mutant is generated, counted and never run — and
// because it is a suppression, internal/referral refers the change.
func TestADeclaredMutantIsCountedAndNeverRun(t *testing.T) {
	declared := "\nfunc Deeper(a, b string) bool {\n\treturn Depth(a) > Depth(b) " +
		annotationMarker + " the two forms agree for every input this is called with\n}\n"
	root := goModuleRepo(t, declared, `
func TestDeeperIsCalledAndNothingIsAsserted(t *testing.T) {
	Deeper("a/b", "a")
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err != nil {
		t.Fatalf("a declared mutant failed the gate: %v\n%+v", err, doc.Rows)
	}
	row, _ := rowNamed(doc, mutationLabel("app"))
	// Every mutant on that line is acknowledged, so nothing is left to say
	// anything about the suite — unmeasured, never a pass.
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured: an acknowledged mutant measures nothing", row)
	}
	if !strings.Contains(row.Value, "declared equivalent") {
		t.Errorf("value = %q, want it to name the declaration", row.Value)
	}
}

// Nothing can be concluded about tests that were not passing before the
// mutation. Failing would report one broken suite as two red gates, the second
// naming a cause its author clears by fixing the first.
func TestAComponentWhoseBaselineFailsIsUnmeasured(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestSomethingUnrelatedIsBroken(t *testing.T) {
	Deeper("a/b", "a")
	t.Fatal("this suite was already red")
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err != nil {
		t.Fatalf("a red baseline failed the mutation gate as well: %v", err)
	}
	row, _ := rowNamed(doc, mutationLabel("app"))
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured", row)
	}
	if !strings.Contains(row.Value, "baseline suite did not pass") {
		t.Errorf("value = %q, want it to name the cause", row.Value)
	}
	// The tail of what the suite printed is under the row, and the log holds
	// the rest — a reader should not have to reproduce the run to see why.
	if len(row.Detail) == 0 {
		t.Error("the row carries nothing of what the suite printed")
	}
}

// A report path joins onto the component's directory, so an empty one names
// the directory itself — and clearing it is asking to remove the component.
// Both the caller and the floor refuse it, because the caller is the one that
// can say something useful and the floor is what stops a caller that forgets.
func TestAnEmptyReportPathIsRefusedRatherThanCleared(t *testing.T) {
	dir := t.TempDir()
	if err := clearReport(dir, ""); err == nil {
		t.Error("clearing an empty report path was allowed")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the component's directory was removed: %v", err)
	}
	// An empty directory is the case a bare os.Remove would actually succeed
	// on, so it is the one worth asserting.
	empty := filepath.Join(t.TempDir(), "component")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := clearReport(empty, ""); err == nil {
		t.Error("clearing an empty report path was allowed for an empty directory")
	}
	if _, err := os.Stat(empty); err != nil {
		t.Errorf("an empty component directory was removed: %v", err)
	}
}

// Every mutant in the run and outside the denominator is named, and one that
// is not there is not mentioned. An aside reading "0 did not compile" on every
// clean component is what teaches a reader to skim the line that exists to be
// noticed.
func TestTheAsideNamesOnlyTheMutantsThatAreThere(t *testing.T) {
	if got := aside(mutation.Summary{Killed: 9, Survived: 1}); got != "" {
		t.Errorf("the aside of a run with nothing outside its denominator is %q, want nothing", got)
	}
	for _, c := range []struct {
		name string
		s    mutation.Summary
		want string
	}{
		{"unviable", mutation.Summary{Killed: 1, Unviable: 2}, "2 did not compile"},
		{"acknowledged", mutation.Summary{Killed: 1, Acknowledged: 1}, "1 declared equivalent"},
		{"timed out", mutation.Summary{Killed: 1, TimedOut: 3}, "3 timed out"},
		{"all three", mutation.Summary{Unviable: 1, Acknowledged: 2, TimedOut: 3},
			"1 did not compile, 2 declared equivalent, 3 timed out"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := aside(c.s); got != c.want {
				t.Errorf("aside = %q, want %q", got, c.want)
			}
		})
	}
}

// What hangs under a row: the mutants outside the denominator when there are
// any, and the log when there is one. Both are conditional, and a row that
// carried an empty line or a path to nothing would say it either way.
func TestARowCarriesAnAsideAndALogOnlyWhenItHasThem(t *testing.T) {
	survivor := []mutation.Result{{
		Mutant:  mutation.Mutant{Path: "a.go", Line: 3, Column: 4, Operator: mutation.NegateConditional},
		Outcome: mutation.Survived,
	}}
	logged, unlogged := testLog(t), &componentLog{}
	if logged.Rel == "" {
		t.Fatal("the fixture log names no path, so this proves nothing")
	}

	// A passing row with nothing outside its denominator says nothing more.
	row := mutationRow(mutationLabel("app"), logged, mutation.Summary{Killed: 3}, nil, time.Second)
	if len(row.Detail) != 0 {
		t.Errorf("a clean passing row carries %v", row.Detail)
	}
	// ...and with something outside it, exactly that.
	row = mutationRow(mutationLabel("app"), logged, mutation.Summary{Killed: 3, Unviable: 2}, nil, time.Second)
	if len(row.Detail) != 1 || row.Detail[0] != "2 did not compile" {
		t.Errorf("a passing row's detail is %v, want the aside alone", row.Detail)
	}

	// A failing row names every survivor, then the aside if there is one,
	// then what to do, then the log.
	row = mutationRow(mutationLabel("app"), logged, mutation.Summary{Killed: 1, Survived: 1}, survivor, time.Second)
	if joined := strings.Join(row.Detail, "\n"); strings.Contains(joined, "did not compile") ||
		strings.Contains(joined, "declared equivalent") {
		t.Errorf("a failing row with nothing outside its denominator carries an aside: %v", row.Detail)
	}
	if !hasDetail(row, "full output: "+logged.Rel) {
		t.Errorf("a failing row does not name its log: %v", row.Detail)
	}
	row = mutationRow(mutationLabel("app"), logged, mutation.Summary{Killed: 1, Survived: 1, Unviable: 2}, survivor, time.Second)
	if !hasDetail(row, "2 did not compile") {
		t.Errorf("a failing row does not name what is outside its denominator: %v", row.Detail)
	}

	// A run whose log could not be opened names no log rather than a path to
	// nothing, on a failing row and on an unmeasured one alike.
	row = mutationRow(mutationLabel("app"), unlogged, mutation.Summary{Killed: 1, Survived: 1}, survivor, time.Second)
	for _, d := range row.Detail {
		if strings.HasPrefix(d, "full output:") {
			t.Errorf("a row with no log carries %q", d)
		}
	}
	if row.Log != "" {
		t.Errorf("a row with no log names %q", row.Log)
	}
	unmeasured := mutationRow(mutationLabel("app"), logged, mutation.Summary{Unviable: 2}, nil, time.Second)
	if unmeasured.Status != ui.StatusUnmeasured {
		t.Fatalf("a run with an empty denominator is %q, want unmeasured", unmeasured.Status)
	}
	if !hasDetail(unmeasured, "full output: "+logged.Rel) || unmeasured.Log != logged.Rel {
		t.Errorf("an unmeasured row does not name its log: %+v", unmeasured)
	}
	if got := mutationRow(mutationLabel("app"), unlogged, mutation.Summary{Unviable: 2}, nil, time.Second); got.Log != "" {
		t.Errorf("an unmeasured row with no log names %q", got.Log)
	}
}

func hasDetail(row ui.Row, want string) bool {
	for _, d := range row.Detail {
		if d == want {
			return true
		}
	}
	return false
}

// Under --json stdout carries the document, so a tool's live output must go to
// stderr: findings interleaved into the document make it unparseable, and
// anything automated reads the document and never the terminal.
//
// executil's stream target is a package global the command sets and nothing
// reads back, so this asks it to write and looks at where the writing landed.
func TestUnderJSONAToolsLiveOutputGoesToStderr(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	const probe = "lydite-stream-probe"
	for _, c := range []struct {
		name string
		args []string
	}{
		{"mutation", []string{"mutation", "--json", "--dir", root}},
		{"mutation merge", []string{"mutation", "merge", "--json", "--dir", root, "--reports", mutationShard(t)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			executil.StreamTo(os.Stdout)
			t.Cleanup(func() { executil.StreamTo(os.Stdout) })
			got := capturedStderr(t, func() {
				cmd := newRootCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs(c.args)
				_ = cmd.Execute()
				executil.Run(t.Context(), root, "sh", "-c", "echo "+probe)
			})
			if !strings.Contains(got, probe) {
				t.Errorf("a tool's live output went to stdout, which is where the document is; stderr held:\n%s", got)
			}
		})
	}
}

// capturedStderr runs during with the process's own stderr replaced, which is
// the only way to see what a command wrote there rather than to the writer
// cobra was given. One test at a time: os.Stderr is process-wide.
func capturedStderr(t *testing.T, during func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan string, 1)
	go func() {
		out, _ := io.ReadAll(r)
		read <- string(out)
	}()
	defer func() { _ = r.Close() }()
	saved := os.Stderr
	os.Stderr = w
	// Restored however during ends. A t.Fatal inside it exits this goroutine,
	// and a process left writing its diagnostics into a closed pipe takes
	// every test after this one with it.
	func() {
		defer func() {
			os.Stderr = saved
			_ = w.Close()
		}()
		during()
	}()
	return <-read
}

// Each declared component takes exactly one row in the fold. The labels the
// fold produces itself are how it tells a row it replaced from one it has
// never seen, so a component label it stopped recognising is added twice —
// once by the fold and once as a row nobody handled — which is what a consumer
// keying rows by label cannot survive.
func TestEachComponentTakesOneRowInTheMutationFold(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "1 of 1 mutant(s) killed in 1s"}),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "1 of 1 mutant(s) killed in 1s"}))
	if err != nil {
		t.Fatalf("a complete fold failed: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		n := 0
		for _, r := range doc.Rows {
			if r.Label == mutationLabel(name) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s takes %d rows in the fold, want one", mutationLabel(name), n)
		}
	}
}

// killsItsMutants is the suite for the change under test, written so nothing
// survives: a run that ends green is what lets a teardown failure be the only
// thing that could fail the component.
const killsItsMutants = `
func TestDeeper(t *testing.T) {
	if !Deeper("a/b", "a") {
		t.Error("a/b is deeper than a")
	}
	if Deeper("a", "a") {
		t.Error("a is not deeper than itself")
	}
}
`

// Of the components this run is responsible for, --affected mutates only
// those the change could have broken. A deselected one is reported rather
// than dropped, so every declared component takes exactly one row whether it
// ran or not.
func TestOnlyAnAffectedComponentIsMutated(t *testing.T) {
	root := goModuleRepoWith(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n",
		"app",
		map[string]string{"web/app.ts": "export const version = 1\n"},
		deeper, killsItsMutants)

	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--affected")
	if err != nil {
		t.Fatalf("an affected run failed: %v\n%+v", err, doc.Rows)
	}
	web, ok := rowNamed(doc, mutationLabel("web"))
	if !ok {
		t.Fatalf("the deselected component took no row: %+v", doc.Rows)
	}
	if web.Status != ui.StatusUnmeasured || web.Value != "not affected" {
		t.Errorf("web = %+v, want unmeasured/not affected", web)
	}
	app, ok := rowNamed(doc, mutationLabel("app"))
	if !ok || app.Status != ui.StatusPass {
		t.Fatalf("app = %+v, want the component the change touched to have run", app)
	}
	sel, ok := rowNamed(doc, "select")
	if !ok {
		t.Fatal("an affected run emitted no select row, so nothing says how much of the declaration it covered")
	}
	if !strings.Contains(sel.Value, "1 of 2 affected") {
		t.Errorf("select = %q, want 1 of 2 affected", sel.Value)
	}
}

// A teardown that failed has left state the next run inherits, so it fails a
// component that otherwise passed — and never masks a failure that already
// happened, because the earlier one is what its author has to act on.
func TestAFailingTeardownFailsAPassingComponentAndMasksNothing(t *testing.T) {
	decl := goModuleDecl + "    teardown: [\"echo the teardown could not run >&2; exit 1\"]\n"

	green := goModuleRepoWith(t, decl, "", nil, deeper, killsItsMutants)
	doc, _, err := runMutationCmd(t, "--dir", green, "--base-branch", "main")
	if err == nil {
		t.Fatalf("a teardown that failed left a passing component: %+v", doc.Rows)
	}
	row, _ := rowNamed(doc, mutationLabel("app"))
	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v, want the teardown failure", row)
	}
	if !strings.Contains(row.Value, "teardown") {
		t.Errorf("value = %q, want it to name the teardown", row.Value)
	}

	// The same teardown over a component that was already not measurable
	// keeps the earlier answer.
	red := goModuleRepoWith(t, decl, "", nil, deeper, `
func TestSomethingUnrelatedIsBroken(t *testing.T) {
	Deeper("a/b", "a")
	t.Fatal("this suite was already red")
}
`)
	doc, _, _ = runMutationCmd(t, "--dir", red, "--base-branch", "main")
	row, _ = rowNamed(doc, mutationLabel("app"))
	if !strings.Contains(row.Value, "baseline suite did not pass") {
		t.Errorf("value = %q, want the failure the author has to act on first", row.Value)
	}
}

// --stream mirrors each component's output to stderr as well as to its log.
// It is for the case a captured file cannot serve — a suite that hangs prints
// nothing until it is killed — so the mirror has to be the process's own
// stderr rather than a writer a caller passed in.
func TestStreamMirrorsAComponentsOutputToStderr(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestSomethingUnrelatedIsBroken(t *testing.T) {
	Deeper("a/b", "a")
	t.Fatal("this suite was already red")
}
`)
	streamed := capturedStderr(t, func() {
		if _, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--stream"); err != nil {
			t.Errorf("a streamed run failed: %v", err)
		}
	})
	if !strings.Contains(streamed, "app |") {
		t.Errorf("nothing was mirrored under the component's name:\n%s", streamed)
	}
	if !strings.Contains(streamed, "this suite was already red") {
		t.Errorf("the mirror carries none of what the suite printed:\n%s", streamed)
	}

	quiet := capturedStderr(t, func() {
		if _, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main"); err != nil {
			t.Errorf("an unstreamed run failed: %v", err)
		}
	})
	if strings.Contains(quiet, "app |") {
		t.Errorf("a run that was not asked to stream mirrored anyway:\n%s", quiet)
	}
}
