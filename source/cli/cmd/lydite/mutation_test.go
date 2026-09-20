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
	"reflect"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/annotation"
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

// A shard that wrote no mutants.json — an older lydite, or a document that
// would not parse — still has its score read back out of the row it rendered.
// Nothing else holds the wording and the regex together, so a change to either
// is a fold that silently stops counting that shard.
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
		{
			name:   "a mutant stopped at its memory bound counts as killed",
			s:      mutation.Summary{Killed: 2, OutOfMemory: 2, Unviable: 1},
			killed: 4, denom: 4,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			row, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t), c.s, c.results, nil, 42*time.Second)
			decl := component.File{Components: []component.Component{{Name: "app"}}}
			folded := foldedMutationRow([]shardInput{{read: true, doc: ui.Document{Rows: []ui.Row{row}}}}, mutantsDoc{}, decl)

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

// The memory bound multiplies the component's own measured peak, never the
// machine's memory: a bound that moved with the machine would make a mutant
// killed on a small runner and surviving on a large one.
func TestTheMemoryBoundIsAMultipleOfTheMeasuredBaseline(t *testing.T) {
	const big = 4 << 30
	if got := memoryBudget(big, 0); got != big*memoryFactor {
		t.Errorf("memoryBudget(%d) = %d, want %d", big, got, big*memoryFactor)
	}
	// Four times a small peak is a ceiling a compiler reaches on its own, and
	// every mutant would be reported as one that allocated without stopping.
	if got := memoryBudget(40<<20, 0); got != minimumMemory {
		t.Errorf("the bound for a 40MiB baseline = %d, want the %d floor", got, minimumMemory)
	}
	if got := memoryBudget(big, 7<<30); got != 7<<30 {
		t.Errorf("--memory was not honoured: %d", got)
	}
}

// A ceiling the baseline itself already fills is one every mutant reaches
// whatever its tests do, so the component gates nothing rather than reporting
// a suite that kills everything. Only an override can produce it: the
// derivation is four times that same peak.
func TestABaselineThatWouldNotFitUnderTheBoundGatesNothing(t *testing.T) {
	const peak = 1 << 30
	if !memoryFits(peak, memoryBudget(peak, 0)) {
		t.Error("a derived bound left its own baseline no room")
	}
	if memoryFits(peak, memoryBudget(peak, peak)) {
		t.Error("a bound equal to the baseline's own peak was accepted")
	}
	if memoryFits(peak, memoryBudget(peak, peak*memoryHeadroom-1)) {
		t.Errorf("a bound under %d times the baseline's peak was accepted", memoryHeadroom)
	}
	if !memoryFits(peak, memoryBudget(peak, peak*memoryHeadroom)) {
		t.Errorf("a bound of exactly %d times the baseline's peak was refused", memoryHeadroom)
	}
}

// --memory is written the way a size is written. Every suffix is binary,
// including the bare ones: the quantity is compared against a peak the kernel
// reports in pages, and a GB and a GiB that were different ceilings would be a
// row nobody could reason about.
func TestTheMemoryOverrideIsReadAsASize(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"", 0},
		// Zero is no bound at all rather than an impossible one, which is what
		// the flag's own default means.
		{"0", 0},
		{"1024", 1024},
		{"4G", 4 << 30},
		{"4GB", 4 << 30},
		{"4GiB", 4 << 30},
		{"512M", 512 << 20},
		{"512MiB", 512 << 20},
		{"8K", 8 << 10},
		{" 2GiB ", 2 << 30},
	} {
		got, err := parseBytes(c.in)
		if err != nil {
			t.Errorf("parseBytes(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	// A negative ceiling bounds nothing and a suffix that overflowed reads as
	// one, so both are refused where the number is read.
	for _, in := range []string{"lots", "4TiB", "-1", "-4GiB", "9223372036854775807GiB", "4 GiB extra"} {
		got, err := parseBytes(in)
		if err == nil {
			t.Errorf("parseBytes(%q) = %d, want an error naming the fix", in, got)
		}
		// Nothing beside the error, because zero is what asks for the
		// derivation: a quantity nobody could read must not leave a ceiling
		// nobody chose.
		if got != 0 {
			t.Errorf("parseBytes(%q) = %d beside its error, want no bound at all", in, got)
		}
	}
}

// A bound the platform had none to set is on the row, never silent: a mutant
// that could allocate without stopping was held to nothing, and reporting that
// as the green of a bound that held is the failure the amber tag exists for.
func TestARowSaysWhenMemoryWasNotBounded(t *testing.T) {
	unbounded := []mutation.Result{{
		Mutant: mutation.Mutant{Path: "a.go", Line: 3, Column: 4, Operator: mutation.NegateConditional},
		// A mutant that survived, so the note is proven on the failing row as
		// well as on the passing one.
		Outcome: mutation.Survived, MemoryUnbounded: true,
	}}
	failing, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Killed: 1, Survived: 1}, unbounded, nil, time.Second)
	if !detailSaying(failing, "memory was not bounded") {
		t.Errorf("a failing row from an unbounded run says %v", failing.Detail)
	}

	held := []mutation.Result{{Outcome: mutation.Killed}}
	passing, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Killed: 1}, held, nil, time.Second)
	if detailSaying(passing, "memory was not bounded") {
		t.Errorf("a row whose bound held says it did not: %v", passing.Detail)
	}
	unheld := []mutation.Result{{Outcome: mutation.Killed, MemoryUnbounded: true}}
	passing, _ = mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Killed: 1}, unheld, nil, time.Second)
	if !detailSaying(passing, "memory was not bounded") {
		t.Errorf("a passing row from an unbounded run says %v", passing.Detail)
	}
}

func detailSaying(row ui.Row, want string) bool {
	for _, d := range row.Detail {
		if strings.Contains(d, want) {
			return true
		}
	}
	return false
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

// A component the change touches no source of is refused before anything is
// prepared, started or run. Half of what bounds a mutant is knowable from the
// diff alone, so the baseline suite, the compose stack and the setup commands
// are pure cost there — and on the default branch, where HEAD is its own
// merge-base, that is every component.
//
// Asserted on the refusal itself and not only on the row beside it: a decision
// that reported the row and carried on would run the whole component anyway,
// which is the cost this exists to avoid, while every assertion about the row
// still passed.
func TestAComponentTheChangeDoesNotTouchIsRefusedBeforeAnythingRuns(t *testing.T) {
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test"}
	plan := componentPlan{c: c, log: testLog(t)}
	// No changed lines at all, which is what a component outside the diff has.
	_, row, ok := prepareMutation(plan, config.Config{}, nil, mutationOptions{root: t.TempDir()})
	if ok {
		t.Fatal("a component the change touches no source of was prepared to run")
	}
	if row.Status != ui.StatusUnmeasured {
		t.Errorf("status is %q, want unmeasured — nothing was mutated", row.Status)
	}
	if !strings.Contains(row.Value, "touches no source") {
		t.Errorf("value = %q, want it to say why", row.Value)
	}
}

// Go needs no worker directory: an overlay names the mutated file wherever it
// is written. Rust and TypeScript have no such instruction, so their mutants
// run in a copy of the repository — and one git lists no file in has nothing
// to copy, which is said out loud rather than reported as a component whose
// suite killed everything.
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
	backend, err := backendFor(runner.Go, t.TempDir(), "cli", runner.Invocation{}, runner.Invocation{}, nil, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Go); !ok {
		t.Errorf("Go got %T, want the overlay backend", backend)
	}
	backend, err = backendFor(runner.TypeScript, t.TempDir(), "web", runner.Invocation{}, runner.Invocation{}, []string{"web/src/a.ts"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Tree); !ok {
		t.Errorf("TypeScript got %T, want the worker-directory backend", backend)
	}
	if _, err := backendFor(runner.Rust, t.TempDir(), "rust", runner.Invocation{}, runner.Invocation{}, nil, none); err == nil {
		t.Error("a scan root git lists no file under was given a worker directory to copy nothing into")
	}

	// A component rooted at the scan root is where every path join in the
	// backend collapses, so it is the case least likely to be noticed and the
	// one a `.`-rooted repository always takes.
	backend, err = backendFor(runner.TypeScript, t.TempDir(), ".", runner.Invocation{}, runner.Invocation{}, []string{"src/a.ts"}, none)
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := backend.(mutation.Tree)
	if !ok {
		t.Fatalf("a component at the scan root got %T, want the worker-directory backend", backend)
	}
	if tree.Component != "." {
		t.Errorf("a component declared at %q became %q", ".", tree.Component)
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

// countsShard writes one shard's report directory with the counts document
// beside the rendered report, the way a matrix job leaves it behind.
func countsShard(t *testing.T, counts mutantsDoc, rows ...ui.Row) string {
	t.Helper()
	dir := mutationShard(t, rows...)
	data, err := json.Marshal(counts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutantsName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The fold sums what the shards measured rather than what they said, and a
// shard that ran nothing at all folds in: its document holds no component, so
// it contributes nothing to the score and its own rows still take their place.
func TestTheFoldSumsTheShardsCountsIncludingAShardThatRanNothing(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		countsShard(t,
			mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 7, Survived: 1, Unviable: 2}}},
			ui.Row{Status: ui.StatusFail, Label: mutationLabel("a"), Value: "1 of 8 mutant(s) survived in 1m30s"}),
		countsShard(t,
			mutantsDoc{Tree: "abc"},
			ui.Row{Status: ui.StatusContext, Label: mutationLabel("b"), Value: "mutation is off for this component"}))
	if err == nil {
		t.Error("a survivor did not fail the fold")
	}
	summary, ok := rowNamed(doc, "mutation")
	if !ok {
		t.Fatal("the fold emitted no summary row")
	}
	// Seven killed of the eight that say something about the suite. The two
	// that did not compile are outside the denominator, and the shard that ran
	// nothing adds neither a component nor a mutant.
	want := "7 of 8 mutant(s) killed across 1 component(s) in 1m30s"
	if summary.Value != want {
		t.Errorf("summary = %q, want %q", summary.Value, want)
	}
	// A shard that measured nothing is a shard of this run all the same: it was
	// responsible for a component that opted out, and refusing its document
	// would make an opt-out anywhere in the matrix a fold that cannot complete.
	shards, _ := rowNamed(doc, "shards")
	if shards.Status != ui.StatusPass {
		t.Errorf("the shards row is %+v, want the shard that ran nothing folded in", shards)
	}
}

// A component whose mutants said nothing about the suite still ran, and the
// fold counts it because its shard's counts hold it — the rendered row is
// `unmeasured` and carries no score at all. It is the count an unsharded run
// reports for the same declaration, which is what a fold reading numbers rather
// than sentences is for.
func TestTheFoldCountsAComponentWhoseMutantsSaidNothing(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		countsShard(t,
			mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 7, Survived: 1}}},
			ui.Row{Status: ui.StatusFail, Label: mutationLabel("a"), Value: "1 of 8 mutant(s) survived in 1m30s"}),
		countsShard(t,
			mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"b": {Unviable: 3}}},
			ui.Row{Status: ui.StatusUnmeasured, Label: mutationLabel("b"),
				Value: "3 mutant(s), none of which says anything about the suite: 3 did not compile"}))
	if err == nil {
		t.Error("a survivor did not fail the fold")
	}
	summary, _ := rowNamed(doc, "mutation")
	want := "7 of 8 mutant(s) killed across 2 component(s) in 1m30s"
	if summary.Value != want {
		t.Errorf("summary = %q, want %q", summary.Value, want)
	}
}

// A shard whose counts are missing still contributed a verdict, so its score
// comes back out of its rendered row rather than silently out of the total. A
// version skew in a matrix is what produces that, and dropping the shard would
// under-report the run while every row still read green.
func TestAShardWithNoCountsIsReadBackFromItsRows(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		countsShard(t,
			mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 7, Survived: 1}}},
			ui.Row{Status: ui.StatusFail, Label: mutationLabel("a"), Value: "1 of 8 mutant(s) survived in 1m30s"}),
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"}))
	if err == nil {
		t.Error("a survivor did not fail the fold")
	}
	summary, _ := rowNamed(doc, "mutation")
	want := "10 of 11 mutant(s) killed across 2 component(s) in 2m0s"
	if summary.Value != want {
		t.Errorf("summary = %q, want the shard with no counts read back from its row", summary.Value)
	}
}

// Counts that are there and will not parse are neither a shard that wrote none
// nor one that measured nothing, and are named on that shard's row: read as
// absent, the fold would answer from the prose while the row still read `pass`.
func TestAShardWhoseCountsWillNotParseFailsItsRow(t *testing.T) {
	root := mergeRepo(t)
	broken := mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "4 of 4 mutant(s) killed in 10s"})
	if err := os.WriteFile(filepath.Join(broken, mutantsName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := runMutationMerge(t, root, broken,
		mutationShard(t, ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"}))
	if err == nil {
		t.Error("a shard whose counts would not parse passed the fold")
	}
	row, ok := rowNamed(doc, "read("+broken+")")
	if !ok || row.Status != ui.StatusFail {
		t.Fatalf("the shard's row is %+v, want a failure naming what could not be read", row)
	}
	// The score is still reported, from the prose: the shard rendered a
	// verdict whatever became of its counts.
	summary, _ := rowNamed(doc, "mutation")
	if !strings.HasPrefix(summary.Value, "7 of 7 mutant(s) killed") {
		t.Errorf("summary = %q, want the unreadable shard read back from its row", summary.Value)
	}
}

// Shards that mutated different trees are not parts of one run, and the row
// that says so is `shards` — the one row about whether these documents fold
// into a run at all.
func TestShardsThatMutatedDifferentTreesDoNotFold(t *testing.T) {
	root := mergeRepo(t)
	doc, err := runMutationMerge(t, root,
		countsShard(t, mutantsDoc{Tree: "aaaaaaaaaaaa", Components: map[string]mutantCounts{"a": {Killed: 4}}},
			ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "4 of 4 mutant(s) killed in 10s"}),
		countsShard(t, mutantsDoc{Tree: "bbbbbbbbbbbb", Components: map[string]mutantCounts{"b": {Killed: 3}}},
			ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"}))
	if err == nil {
		t.Error("shards of two trees folded into one run")
	}
	row, _ := rowNamed(doc, "shards")
	if row.Status != ui.StatusFail || !strings.Contains(strings.Join(row.Detail, "\n"), "different trees") {
		t.Errorf("the shards row is %+v, want it to name the disagreement", row)
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

// `--declined` writes the one-row document ADR 0044 describes and returns
// before any component is loaded: a declaration `component.Load` would refuse
// is left untouched, and the run still succeeds.
func TestADeclinedRunWritesOneRowAndTouchesNoComponent(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n  - name: app\n    dir: nowhere\n    runner: go-test\n")
	doc, _, err := runMutationCmd(t, "--dir", root, "--declined")
	if err != nil {
		t.Fatalf("a declined run failed: %v", err)
	}
	if len(doc.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one", doc.Rows)
	}
	row := doc.Rows[0]
	if row.Status != ui.StatusDeclined || row.Label != "mutation" || row.Value != "declined for this run" {
		t.Errorf("row = %+v, want the declined row ADR 0044 describes", row)
	}
	if doc.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q; a declined run is not a failure", doc.Verdict)
	}

	// The document a real run would have written is where publish looks for
	// it, holding the same one row.
	written, err := readDocument(documentPath(reportsDir(root), "mutation"))
	if err != nil {
		t.Fatalf("no document was written to the report directory: %v", err)
	}
	if len(written.Rows) != 1 || written.Rows[0].Status != ui.StatusDeclined {
		t.Errorf("written document rows = %+v, want the one declined row", written.Rows)
	}
}

// The document `lydite mutation --declined` writes renders as a declined
// section once `lydite publish` reads it back — the round-trip ADR 0044
// exists to guarantee: a repository that declined the concern sees that
// stated, not a silently absent section.
func TestADeclinedMutationDocumentRendersAsADeclinedSection(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	if _, _, err := runMutationCmd(t, "--dir", root, "--declined"); err != nil {
		t.Fatalf("a declined run failed: %v", err)
	}
	comment := buildComment([]string{reportsDir(root)}, "")
	if len(comment.Sections) != 1 {
		t.Fatalf("sections = %+v, want exactly the mutation section", comment.Sections)
	}
	if got := comment.Sections[0].Status; got != ui.StatusDeclined {
		t.Errorf("section status = %q, want declined", got)
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
		{"a memory bound that is not a size", []string{"--dir", root, "--memory", "lots"}, "--memory"},
		{"a negative memory bound", []string{"--dir", root, "--memory", "-1GiB"}, "--memory"},
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
		{summary: mutation.Summary{Killed: 5, TimedOut: 1, OutOfMemory: 1, Acknowledged: 1}, elapsed: 90 * time.Second, ran: true},
		{summary: mutation.Summary{Killed: 99}, elapsed: time.Hour, ran: false},
	}
	row := mutationSummaryRow(results)
	if row.Status != ui.StatusContext {
		t.Errorf("the summary is %q; it gates nothing", row.Status)
	}
	want := "10 of 11 mutant(s) killed across 2 component(s) in 2m0s"
	if row.Value != want {
		t.Errorf("summary = %q, want %q", row.Value, want)
	}
	// The mutants that say nothing about the suite are named rather than
	// folded into the score.
	if len(row.Detail) == 0 || !strings.Contains(row.Detail[0], "2 did not compile") ||
		!strings.Contains(row.Detail[0], "1 declared equivalent") ||
		!strings.Contains(row.Detail[0], "1 timed out") ||
		!strings.Contains(row.Detail[0], "1 ran out of memory") {
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
	declared := "\nfunc Deeper(a, b string) bool {\n\treturn Depth(a) > Depth(b) // " +
		annotation.Marker(annotation.Mutation) +
		"[the two forms agree for every input this is called with]\n}\n"
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
		{"out of memory", mutation.Summary{Killed: 1, OutOfMemory: 2}, "2 ran out of memory"},
		{"all four", mutation.Summary{Unviable: 1, Acknowledged: 2, TimedOut: 3, OutOfMemory: 4},
			"1 did not compile, 2 declared equivalent, 3 timed out, 4 ran out of memory"},
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
	row, _ := mutationRow(mutationLabel("app"), "app", "app", logged, mutation.Summary{Killed: 3}, nil, nil, time.Second)
	if len(row.Detail) != 0 {
		t.Errorf("a clean passing row carries %v", row.Detail)
	}
	// ...and with something outside it, exactly that.
	row, _ = mutationRow(mutationLabel("app"), "app", "app", logged, mutation.Summary{Killed: 3, Unviable: 2}, nil, nil, time.Second)
	if len(row.Detail) != 1 || row.Detail[0] != "2 did not compile" {
		t.Errorf("a passing row's detail is %v, want the aside alone", row.Detail)
	}

	// A failing row names every survivor, then the aside if there is one,
	// then what to do, then the log.
	row, _ = mutationRow(mutationLabel("app"), "app", "app", logged, mutation.Summary{Killed: 1, Survived: 1}, survivor, nil, time.Second)
	if joined := strings.Join(row.Detail, "\n"); strings.Contains(joined, "did not compile") ||
		strings.Contains(joined, "declared equivalent") {
		t.Errorf("a failing row with nothing outside its denominator carries an aside: %v", row.Detail)
	}
	if !hasDetail(row, "full output: "+logged.Rel) {
		t.Errorf("a failing row does not name its log: %v", row.Detail)
	}
	row, _ = mutationRow(mutationLabel("app"), "app", "app", logged, mutation.Summary{Killed: 1, Survived: 1, Unviable: 2}, survivor, nil, time.Second)
	if !hasDetail(row, "2 did not compile") {
		t.Errorf("a failing row does not name what is outside its denominator: %v", row.Detail)
	}

	// A run whose log could not be opened names no log rather than a path to
	// nothing, on a failing row and on an unmeasured one alike.
	row, _ = mutationRow(mutationLabel("app"), "app", "app", unlogged, mutation.Summary{Killed: 1, Survived: 1}, survivor, nil, time.Second)
	for _, d := range row.Detail {
		if strings.HasPrefix(d, "full output:") {
			t.Errorf("a row with no log carries %q", d)
		}
	}
	if row.Log != "" {
		t.Errorf("a row with no log names %q", row.Log)
	}
	unmeasured, _ := mutationRow(mutationLabel("app"), "app", "app", logged, mutation.Summary{Unviable: 2}, nil, nil, time.Second)
	if unmeasured.Status != ui.StatusUnmeasured {
		t.Fatalf("a run with an empty denominator is %q, want unmeasured", unmeasured.Status)
	}
	if !hasDetail(unmeasured, "full output: "+logged.Rel) || unmeasured.Log != logged.Rel {
		t.Errorf("an unmeasured row does not name its log: %+v", unmeasured)
	}
	if got, _ := mutationRow(mutationLabel("app"), "app", "app", unlogged, mutation.Summary{Unviable: 2}, nil, nil, time.Second); got.Log != "" {
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

// --base-sha is the commit it names and nothing else: no fetch and no
// merge-base, which is what lets a run on the default branch have a range at
// all. The fixture is two commits deep so the two flags name different
// commits, and every spelling git resolves names the same one.
func TestAnExplicitBaseResolvesTheCommitItNames(t *testing.T) {
	root := twoChangesRepo(t)
	ctx := context.Background()
	want := revParse(t, root, "HEAD~1")
	mergeBase, err := resolveMutationBase(ctx, root, "main", "")
	if err != nil {
		t.Fatalf("the merge-base against main did not resolve: %v", err)
	}
	if mergeBase == want {
		t.Fatal("the fixture's merge-base is its own HEAD~1, so nothing here distinguishes the two bases")
	}
	for _, c := range []struct {
		name     string
		revision string
	}{
		{"the full SHA", want},
		{"an abbreviated SHA", want[:8]},
		{"a relative ref", "HEAD~1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveMutationBase(ctx, root, "", c.revision)
			if err != nil {
				t.Fatalf("%s did not resolve: %v", c.revision, err)
			}
			if got != want {
				t.Errorf("--base-sha %s resolved %s, want %s", c.revision, got, want)
			}
		})
	}
}

// The mutants themselves, and not only the commit behind them: pointed at a
// base explicitly, a run produces exactly what the merge-base run over the
// same range produces — over a repository whose merge-base is further back,
// and so would carry an earlier change's mutants too.
func TestAnExplicitBaseProducesTheMutantsTheMergeBaseWould(t *testing.T) {
	score := func(t *testing.T, root string, args ...string) string {
		t.Helper()
		doc, _, err := runMutationCmd(t, append([]string{"--dir", root}, args...)...)
		if err != nil {
			t.Fatalf("the run failed: %v\n%+v", err, doc.Rows)
		}
		row, ok := rowNamed(doc, mutationLabel("app"))
		if !ok {
			t.Fatalf("no row for the component: %+v", doc.Rows)
		}
		// The counts alone. The elapsed time is in the same value and
		// differs between two runs of the same range.
		counts, _, _ := strings.Cut(row.Value, " in ")
		return counts
	}
	// The second change alone, as a branch whose merge-base holds the first.
	want := score(t, goModuleRepo(t, shallower, killsShallower), "--base-branch", "main")
	if !strings.Contains(want, "mutant(s) killed") {
		t.Fatalf("the fixture mutated nothing, so the two runs agree about nothing: %q", want)
	}
	got := score(t, twoChangesRepo(t), "--base-sha", "HEAD~1")
	if got != want {
		t.Errorf("--base-sha HEAD~1 mutated %q, want the equivalent merge-base run's %q", got, want)
	}
}

// twoChangesRepo is a branch carrying two changes over main, so the merge-base
// with main is a commit behind HEAD~1 and the two bases name different ranges.
func twoChangesRepo(t *testing.T) string {
	t.Helper()
	root := goModuleRepoWith(t, goModuleDecl, "", nil, deeper, killsItsMutants)
	write(t, root, "paths/paths.go", baseSource+deeper+shallower)
	write(t, root, "paths/paths_test.go", baseSuite+killsItsMutants+killsShallower)
	commitAll(t, root, "the second change")
	return root
}

// shallower is a second change, on its own commit: one comparison, on a line
// the suite below executes.
const shallower = "\nfunc Shallower(a, b string) bool { return Depth(a) < Depth(b) }\n"

const killsShallower = `
func TestShallower(t *testing.T) {
	if !Shallower("a", "a/b") {
		t.Error("a is shallower than a/b")
	}
	if Shallower("a", "a") {
		t.Error("a is not shallower than itself")
	}
}
`

// revParse is what the fixture's own git says a revision is, which is what
// resolution is checked against rather than against itself.
func revParse(t *testing.T, root, revision string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", revision)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", revision, err)
	}
	return strings.TrimSpace(string(out))
}

// --affected answers about the range the mutants came from. Selection
// resolving a base of its own would report a component untouched while its own
// mutants were being run, or the reverse — so the fixture is built where the
// two bases disagree: the last commit touches web alone, and the merge-base
// with main is behind a commit that touched app.
func TestAnExplicitBaseSelectsTheComponentsItsMutantsCameFrom(t *testing.T) {
	root := goModuleRepoWith(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: web\n    dir: web\n    runner: go-test\n    args: [\"./...\"]\n",
		"app",
		map[string]string{
			"web/go.mod": "module fixture/web\n\ngo 1.26.4\n",
			"web/web.go": "package web\n\nfunc Version() int { return 1 }\n",
		},
		deeper, killsItsMutants)
	write(t, root, "web/web.go", "package web\n\nfunc Version() int { return 2 }\n")
	commitAll(t, root, "a change to web alone")

	doc, _, err := runMutationCmd(t, "--dir", root, "--base-sha", "HEAD~1", "--affected")
	if err != nil {
		t.Fatalf("an affected run against an explicit base failed: %v\n%+v", err, doc.Rows)
	}
	app, ok := rowNamed(doc, mutationLabel("app"))
	if !ok {
		t.Fatalf("the deselected component took no row: %+v", doc.Rows)
	}
	// The component the explicit base's range does not touch, and the one
	// whose mutants that same range produced none of.
	if app.Status != ui.StatusUnmeasured || app.Value != "not affected" {
		t.Errorf("app = %+v, want unmeasured/not affected — the range this run mutated does not touch it", app)
	}
	if _, ok := rowNamed(doc, mutationLabel("web")); !ok {
		t.Fatalf("the selected component took no row: %+v", doc.Rows)
	}
	sel, ok := rowNamed(doc, "select")
	if !ok {
		t.Fatal("an affected run emitted no select row")
	}
	if !strings.Contains(sel.Value, "1 of 2 affected") {
		t.Errorf("select = %q, want 1 of 2 affected", sel.Value)
	}
}

// commitAll commits everything in the fixture as one commit.
func commitAll(t *testing.T, root, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=lydite", "GIT_AUTHOR_EMAIL=lydite@example.com",
			"GIT_COMMITTER_NAME=lydite", "GIT_COMMITTER_EMAIL=lydite@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// Two answers to one question. Refused rather than resolved by precedence: a
// mis-wired workflow supplying both would otherwise mutate a range nobody
// asked for, silently.
func TestABaseBranchAndABaseSHATogetherAreRefused(t *testing.T) {
	root := mutationRepo(t, "components:\n  - name: app\n    dir: app\n    runner: go-test\n")
	// Both of them resolvable in this fixture, so what is under test is the
	// refusal rather than one of the two failing on its own.
	_, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--base-sha", "HEAD")
	if err == nil {
		t.Fatal("both bases were accepted, so one of them silently decided the range")
	}
	for _, want := range []string{"base-branch", "base-sha"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the run failed with %q, want it to name %s", err, want)
		}
	}
}

// An unresolvable explicit base is an error naming the revision and the fix,
// exactly as an unresolvable merge-base is — never a run that mutates nothing
// and reports a pass.
func TestAnUnresolvableExplicitBaseIsAnErrorNamingTheFix(t *testing.T) {
	root := mutationRepo(t, "components:\n  - name: app\n    dir: app\n    runner: go-test\n")
	_, _, err := runMutationCmd(t, "--dir", root, "--base-sha", "HEAD~99")
	if err == nil {
		t.Fatal("a base no commit answers to was accepted")
	}
	for _, want := range []string{"--base-sha", "HEAD~99", "depth 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the run failed with %q, want it to say %q", err, want)
		}
	}
}

// A ceiling the component's own baseline would not fit under gates nothing and
// says why. Under one, every mutant dies of the bound rather than of a test,
// and a row reporting that as a suite that killed everything is a score
// nothing earned.
func TestABaselineThatWouldNotFitUnderTheMemoryBoundIsUnmeasured(t *testing.T) {
	root := goModuleRepo(t, deeper, killsItsMutants)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--memory", "1024")
	if err != nil {
		t.Fatalf("a bound nothing could run under failed the run rather than reporting it: %v", err)
	}
	row, ok := rowNamed(doc, mutationLabel("app"))
	if !ok || row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want an unmeasured row naming the bound", row)
	}
	if !strings.Contains(row.Value, "leaves no room") {
		t.Errorf("value = %q, want the baseline's own peak against the bound", row.Value)
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

// After the merge a survivor is history rather than a verdict: the branch is
// gone and the remedy the row prints belongs to a pull request that no longer
// exists. Under --no-gate the component is measured and recorded in full, and
// the run stays green.
func TestASurvivorUnderNoGateIsRecordedAndDoesNotVote(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestDeeperIsCalledAndNothingIsAsserted(t *testing.T) {
	Deeper("a/b", "a")
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--no-gate")
	if err != nil {
		t.Fatalf("a survivor failed a run that gates nothing: %v\n%+v", err, doc.Rows)
	}
	if doc.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q, want a pass: nothing here was gated", doc.Verdict)
	}
	row, ok := rowNamed(doc, mutationLabel("app"))
	if !ok {
		t.Fatalf("no row for the component: %+v", doc.Rows)
	}
	if row.Status != ui.StatusContext {
		t.Fatalf("row = %+v, want %q", row, ui.StatusContext)
	}
	// Measured and recorded is the whole point of the run, so the numbers and
	// the survivor are on the row a reader acts on.
	if !strings.Contains(row.Value, "survived") {
		t.Errorf("value = %q, want it to say what survived", row.Value)
	}
	if detail := strings.Join(row.Detail, "\n"); !strings.Contains(detail, "paths/paths.go:") {
		t.Errorf("the survivor is not located: %v", row.Detail)
	}
	// The finding anchors to a line in the commit that was measured, which on
	// the default branch is a commit that exists and does not move. A count
	// with no locations is what the ledger would otherwise inherit.
	if len(doc.Findings) == 0 {
		t.Error("a recorded survivor produced no finding")
	}
	for _, f := range doc.Findings {
		if f.Gate != "mutation" || f.Line == 0 {
			t.Errorf("finding = %+v, want a located mutation claim", f)
		}
	}
}

// StatusContext and never StatusPass. Nothing was gated, so a component that
// killed every mutant would otherwise render the ✓ of a gate that examined it
// and cleared it — indistinguishable from the run that did gate.
func TestACleanRunUnderNoGateRendersContextAndNeverPass(t *testing.T) {
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
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--no-gate")
	if err != nil {
		t.Fatalf("a suite that kills every mutant failed: %v\n%+v", err, doc.Rows)
	}
	row, ok := rowNamed(doc, mutationLabel("app"))
	if !ok {
		t.Fatalf("no row for the component: %+v", doc.Rows)
	}
	if row.Status == ui.StatusPass {
		t.Fatalf("a component nothing gated rendered a pass: %+v", row)
	}
	if row.Status != ui.StatusContext {
		t.Fatalf("row = %+v, want %q", row, ui.StatusContext)
	}
	if !strings.Contains(row.Value, "mutant(s) killed") {
		t.Errorf("value = %q, want the kill count", row.Value)
	}
}

// Nothing can be concluded about tests that were not passing before the
// mutation, whichever way the run votes: the flag decides what a completed
// measurement is worth, and this is not one.
func TestABaselineThatDidNotPassIsUnmeasuredUnderNoGateToo(t *testing.T) {
	root := goModuleRepo(t, deeper, `
func TestSomethingUnrelatedIsBroken(t *testing.T) {
	Deeper("a/b", "a")
	t.Fatal("this suite was already red")
}
`)
	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--no-gate")
	if err != nil {
		t.Fatalf("a red baseline failed the run: %v", err)
	}
	row, _ := rowNamed(doc, mutationLabel("app"))
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured — a red baseline measured nothing", row)
	}
	if !strings.Contains(row.Value, "baseline suite did not pass") {
		t.Errorf("value = %q, want it to name the cause", row.Value)
	}
}

// A component that could not run is not a measurement, so the flag that stops
// a survivor voting leaves it failing. Swallowing this is what a
// `continue-on-error` on the workflow step would have done, and it is the
// family --no-gate exists to keep apart from a survivor.
func TestAComponentThatCouldNotRunFailsUnderNoGateToo(t *testing.T) {
	root := t.TempDir()
	c := component.Component{Name: "app", Dir: ".", Runner: runner.GoTest}
	inv, err := invocation(c, runner.Instrumented)
	if err != nil {
		t.Fatal(err)
	}
	// A coverage report the run cannot clear: what it read back afterwards
	// would be the last run's, so the component never starts.
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(inv.CoverageReport), "held"), 0o750); err != nil {
		t.Fatal(err)
	}
	plan := componentPlan{c: c, log: testLog(t)}
	row, out := mutateComponent(t.Context(), plan, config.Config{}, nil, nil,
		mutationOptions{root: root, changed: map[string][]int{"paths/paths.go": {4}}})
	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v, want a failure", row)
	}
	if out.ran {
		t.Error("a component that never started is recorded as having run")
	}
}

// The flag the post-merge workflow passes, spelled as the negation of a
// default: mutation gates unless a caller says otherwise, and a run that
// forgot the flag gates exactly as it does today.
func TestMutationGatesUnlessNoGateIsPassed(t *testing.T) {
	flag := newMutationCmd().Flags().Lookup("no-gate")
	if flag == nil {
		t.Fatal("mutation declares no --no-gate, so the post-merge run has no way to record without gating")
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-gate defaults to %q, want a run that gates unless asked otherwise", flag.DefValue)
	}
}

// The default is byte-identical: a gating run's row is the one mutationRow
// built, status and value and detail alike.
func TestAGatingRunKeepsTheRowItMeasured(t *testing.T) {
	survivor := []mutation.Result{{
		Mutant:  mutation.Mutant{Path: "a.go", Line: 3, Column: 4, Operator: mutation.NegateConditional},
		Outcome: mutation.Survived,
	}}
	failing, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Killed: 4, Survived: 1}, survivor, nil, 12*time.Second)
	if got := completedRow(failing, false); !reflect.DeepEqual(got, failing) {
		t.Errorf("a gating run rendered %+v, want the row it measured, %+v", got, failing)
	}
	if completedRow(failing, false).Status != ui.StatusFail {
		t.Error("a survivor stopped failing a run that gates")
	}
	passing, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Killed: 4}, nil, nil, 12*time.Second)
	if got := completedRow(passing, false); !reflect.DeepEqual(got, passing) {
		t.Errorf("a gating run rendered %+v, want the row it measured, %+v", got, passing)
	}
	// A denominator of zero says nothing about the suite either way: --no-gate
	// has nothing to add to a row that was never voting.
	empty, _ := mutationRow(mutationLabel("app"), "app", "app", testLog(t),
		mutation.Summary{Unviable: 2}, nil, nil, 12*time.Second)
	for _, noGate := range []bool{true, false} {
		if got := completedRow(empty, noGate); got.Status != ui.StatusUnmeasured {
			t.Errorf("an empty denominator is %q under no-gate=%v, want unmeasured", got.Status, noGate)
		}
	}
}

// A teardown that failed left state behind for the next run to inherit, and
// --no-gate does not excuse it: the flag is about what the mutants said, and a
// teardown is a command that did not run. It still never masks a reason the
// component could not be measured, nor a survivor a gating run is failing on.
func TestATeardownFailureTakesOverAMeasurementAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		status ui.Status
		want   bool
	}{
		{ui.StatusPass, true},
		{ui.StatusContext, true},
		{ui.StatusFail, false},
		{ui.StatusUnmeasured, false},
	} {
		if got := teardownFailureReplaces(c.status); got != c.want {
			t.Errorf("a teardown failure over a %q row = %v, want %v", c.status, got, c.want)
		}
	}
}
