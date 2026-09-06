package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/mutation"
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

// A component whose language lydite parses no source for is unmeasured with
// the reason said out loud. Absent, it would read as one whose suite killed
// everything.
func TestAComponentWithNoBackendIsUnmeasuredAndSaysWhy(t *testing.T) {
	c := component.Component{Name: "web", Dir: ".", Runner: "vitest"}
	root := t.TempDir()
	write(t, root, "a.ts", "export const a = 1\n")
	plan := componentPlan{c: c, log: testLog(t)}
	row, _ := mutateComponent(t.Context(), plan, config.Config{}, nil, nil,
		mutationOptions{root: root, changed: map[string][]int{"a.ts": {1}}})

	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("status is %q, want %q", row.Status, ui.StatusUnmeasured)
	}
	if !strings.Contains(row.Value, "typescript") {
		t.Errorf("the row does not name the language: %q", row.Value)
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
