package crap

import (
	"fmt"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/coverage"
)

// write puts a source file under root and returns the hits map covering every
// line of it, so a test that is about complexity does not have to state
// coverage line by line.
func write(t *testing.T, root, rel, src string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

// covering is a hit map giving every line 1..n the same count, which stands in
// for a report that reached the whole file.
func covering(n, count int) map[int]int {
	out := map[int]int{}
	for line := 1; line <= n; line++ {
		out[line] = count
	}
	return out
}

// The formula is the whole of what the gate rests on, and the cubed term is
// what makes it a cliff rather than a slope: the same function is cheap fully
// tested, at the threshold half tested, and five times the threshold untested.
func TestTheIndexIsComplexitySquaredOverUncoveredCubed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		complexity int
		lines      coverage.LineCount
		want       float64
	}{
		{"fully covered scores its own complexity", 12, coverage.LineCount{Covered: 10, Total: 10}, 12},
		{"half covered sits exactly on the threshold", 12, coverage.LineCount{Covered: 5, Total: 10}, 30},
		{"untested costs the square", 12, coverage.LineCount{Covered: 0, Total: 10}, 156},
		{"a straight-line function is cheap untested", 1, coverage.LineCount{Covered: 0, Total: 4}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Index(tc.complexity, tc.lines); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("Index(%d, %v) = %v, want %v", tc.complexity, tc.lines, got, tc.want)
			}
		})
	}
	// The threshold is the complexity at which no amount of testing helps,
	// which is what makes "decompose it" the other way to clear the gate.
	if Index(Threshold+1, coverage.LineCount{Covered: 10, Total: 10}) <= Threshold {
		t.Errorf("a fully covered function above the threshold complexity did not exceed it")
	}
}

// Cyclomatic complexity is one plus a decision point, and the set of decision
// points is the one gocyclo and cyclop already apply — so lydite's number and
// the number a developer gets from either agree.
func TestComplexityCountsTheDecisionPointsGoToolingCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func branches(n int, ch chan int) int {
	if n > 0 && n < 10 || n == 99 {
		n++
	}
	for i := 0; i < n; i++ {
		n += i
	}
	for range []int{1, 2} {
		n++
	}
	switch n {
	case 1:
		n = 2
	case 2, 3:
		n = 4
	default:
		n = 0
	}
	select {
	case v := <-ch:
		n = v
	case w := <-ch:
		n = w
	default:
	}
	return n
}
`)
	rep, err := Measure(root, coverage.LineHits{"a.go": covering(40, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 1 {
		t.Fatalf("scored %d functions, want 1", rep.Scored)
	}
	// 1 base + if + && + || + for + range + two non-default cases + two
	// communicating select clauses = 10. `default` is not a decision in
	// either statement: control reaches it when every other clause is decided
	// against — and there are two of each kind so that miscounting one for
	// the other does not come to the same total.
	if got := functions(t, root, "a.go")[0].Complexity; got != 10 {
		t.Errorf("complexity = %d, want 10", got)
	}
}

// A closure's branches belong to the function that declares it, because the
// coverage half of the score is that function's whole line span and contains
// the closure's lines. Excluding them would score one span's coverage against
// another span's complexity.
func TestAClosuresBranchesCountTowardsTheFunctionThatDeclaresIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func outer(n int) func() int {
	return func() int {
		if n > 0 {
			return 1
		}
		return 0
	}
}
`)
	got := functions(t, root, "a.go")
	if len(got) != 1 {
		t.Fatalf("scored %d functions, want the declaration alone", len(got))
	}
	if got[0].Complexity != 2 {
		t.Errorf("complexity = %d, want 2 — the closure's `if` is inside outer's span", got[0].Complexity)
	}
}

// Only functions above the threshold are carried, worst first: the point of a
// failing row is to name the work, and a list in report order names whatever
// happened to be declared first.
func TestOnlyFunctionsAboveTheThresholdAreCarriedWorstFirst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func simple(n int) int {
	return n + 1
}

func tangled(n int) int {
	if n > 1 {
		n++
	}
	if n > 2 {
		n++
	}
	if n > 3 {
		n++
	}
	if n > 4 {
		n++
	}
	if n > 5 {
		n++
	}
	return n
}

func worse(n int) int {
	if n > 1 {
		n++
	}
	if n > 2 {
		n++
	}
	if n > 3 {
		n++
	}
	if n > 4 {
		n++
	}
	if n > 5 {
		n++
	}
	if n > 6 {
		n++
	}
	return n
}
`)
	// Nothing covered at all, so every function scores comp² + comp.
	rep, err := Measure(root, coverage.LineHits{"a.go": covering(50, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 3 {
		t.Fatalf("scored %d functions, want 3", rep.Scored)
	}
	// simple is complexity 1, so 2 — under the threshold however untested.
	if rep.Above() != 2 {
		t.Fatalf("above = %d, want the two tangled ones; got %v", rep.Above(), rep.Over)
	}
	if rep.Over[0].Name != "worse" || rep.Over[1].Name != "tangled" {
		t.Errorf("over = %v, want worse first", []string{rep.Over[0].Name, rep.Over[1].Name})
	}
	if rep.Worst != rep.Over[0].Value {
		t.Errorf("worst = %v, want the highest scored value %v", rep.Worst, rep.Over[0].Value)
	}
	// The identity is the file and the line, because a bare name identifies
	// nothing in a repository with more than one package.
	if rep.Over[0].File != "a.go" || rep.Over[0].Line == 0 {
		t.Errorf("over[0] = %+v, want it to name where it is", rep.Over[0])
	}
}

// A method carries its receiver, so two methods of one package are told apart
// in a row that has room for a name and not a signature.
func TestAMethodCarriesItsReceiver(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

type T struct{}

func (t *T) Do() int { return 1 }

func (t T) Value() int { return 2 }
`)
	var names []string
	for _, f := range functions(t, root, "a.go") {
		names = append(names, f.Name)
	}
	want := []string{"(*T).Do", "(T).Value"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// A function no line of which the report knows about is not scored, and is
// certainly not scored as 0% covered. A 0/0 that reads as 0% is what
// coverage.LineCount.Measured exists to keep out of every other figure, and an
// empty function scored at 0% would be counted as tested by nobody.
func TestAFunctionTheReportKnowsNoLineOfIsNotScored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func empty() {}

func real(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}
`)
	// The report knows only the lines `real` occupies.
	hits := map[int]int{5: 1, 6: 1, 7: 1, 8: 1, 9: 1}
	rep, err := Measure(root, coverage.LineHits{"a.go": hits})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 1 {
		t.Errorf("scored %d, want only the function the report covers", rep.Scored)
	}
	if !rep.Measured() {
		t.Errorf("a report that scored a function reads as unmeasured")
	}
	// And a component whose report reaches nothing is unmeasured rather than
	// clean: the two read identically in a count of zero.
	empty, err := Measure(root, coverage.LineHits{"a.go": {}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Measured() || empty.Above() != 0 {
		t.Errorf("a report covering no line = %+v, want unmeasured", empty)
	}
}

// The hit map bounds what is scored, which is what makes the generated-file
// exclusion #16 asks for the one internal/coverage already applies rather than
// a second copy of it here. A file on disk that the profile says nothing about
// is a file no test could reach.
func TestOnlyWhatTheProfileDescribesIsScored(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "kept.go", "package a\n\nfunc kept() int { return 1 }\n")
	write(t, root, "gen.go", "// Code generated by hand. DO NOT EDIT.\n\npackage a\n\nfunc gen() int { return 1 }\n")
	rep, err := Measure(root, coverage.LineHits{"kept.go": covering(4, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 1 {
		t.Errorf("scored %d functions, want only the one the profile describes", rep.Scored)
	}
}

// A file the profile names and lydite cannot read is an error naming it, never
// a file quietly skipped. It compiled to produce the profile being read, so a
// failure now says something is wrong with the tree — and a report short one
// file is a count the gate would compare against a baseline taken over all of
// them.
func TestAFileThatCannotBeReadIsAnErrorNamingIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if _, err := Measure(root, coverage.LineHits{"gone.go": covering(3, 1)}); err == nil ||
		!strings.Contains(err.Error(), "gone.go") {
		t.Errorf("err = %v, want one naming the file", err)
	}
	write(t, root, "broken.go", "package a\n\nfunc (\n")
	if _, err := Measure(root, coverage.LineHits{"broken.go": covering(3, 1)}); err == nil ||
		!strings.Contains(err.Error(), "broken.go") {
		t.Errorf("err = %v, want one naming the file", err)
	}
}

// functions scores one file and returns every function in it, above the
// threshold or not, for a test whose subject is the walk rather than the gate.
func functions(t *testing.T, root, file string) []Function {
	t.Helper()
	out, _, _, err := scoreFile(token.NewFileSet(), root, file, covering(400, 1))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The threshold is exclusive: a function sitting exactly on it is not counted.
// 30 is where a complexity-12 function lands at half coverage, which is the
// worked example the definition is usually explained with, so the boundary is
// reachable rather than theoretical.
func TestAFunctionExactlyOnTheThresholdIsNotAboveIt(t *testing.T) {
	t.Parallel()
	if got := Index(12, coverage.LineCount{Covered: 5, Total: 10}); got != Threshold {
		t.Fatalf("the fixture scores %v, not the threshold — the boundary is not being tested", got)
	}
	root := t.TempDir()
	// Complexity 12 — one base plus eleven `if`s — with half its reported
	// lines covered.
	var body strings.Builder
	body.WriteString("package a\n\nfunc onTheLine(n int) int {\n")
	for i := range 11 {
		fmt.Fprintf(&body, "\tif n > %d {\n\t\tn++\n\t}\n", i)
	}
	body.WriteString("\treturn n\n}\n")
	write(t, root, "a.go", body.String())

	hits := map[int]int{}
	for line := 1; line <= 40; line++ {
		hits[line] = line % 2
	}
	rep, err := Measure(root, coverage.LineHits{"a.go": hits})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 1 {
		t.Fatalf("scored %d functions, want 1", rep.Scored)
	}
	// Exactly on the line by construction, and so not above it.
	if got := rep.Over; len(got) != 0 {
		t.Errorf("over = %v, want nothing: a score equal to the threshold is not above it", got)
	}
}

// A fully covered function scores its own complexity, which is the whole of
// what makes the threshold also a complexity ceiling. It is the assertion that
// says the coverage half of the formula is read at all — every other test here
// scores functions the report covers none of, where the covered count could be
// anything.
func TestCoverageIsReadAndAFullyCoveredFunctionScoresItsComplexity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func two(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}
`)
	covered := functions(t, root, "a.go")
	if len(covered) != 1 {
		t.Fatalf("scored %d functions, want 1", len(covered))
	}
	if covered[0].Lines.Percent() != 100 {
		t.Fatalf("coverage = %v%%, want a fully covered function", covered[0].Lines.Percent())
	}
	if covered[0].Value != 2 {
		t.Errorf("value = %v, want the complexity 2 — a fully covered function costs nothing more", covered[0].Value)
	}
	// The same function with nothing covered costs the square, which is what
	// says the two are being told apart rather than both read as uncovered.
	uncovered, _, _, err := scoreFile(token.NewFileSet(), root, "a.go", covering(400, 0))
	if err != nil {
		t.Fatal(err)
	}
	if uncovered[0].Value != 6 {
		t.Errorf("value = %v, want 6 — complexity 2 with nothing covered", uncovered[0].Value)
	}
}

// Two functions scoring the same are ordered by where they are, so a report is
// the same report whichever order the walk reached them in. Without it the
// detail lines under a failing row name a different function each run, and a
// reader chases a moving target.
func TestEqualScoresAreOrderedByWhereTheyAre(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	same := "package a\n\nfunc f(n int) int {\n\tif n > 1 {\n\t\tn++\n\t}\n\tif n > 2 {\n\t\tn++\n\t}\n\tif n > 3 {\n\t\tn++\n\t}\n\tif n > 4 {\n\t\tn++\n\t}\n\tif n > 5 {\n\t\tn++\n\t}\n\treturn n\n}\n"
	hits := coverage.LineHits{}
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		write(t, root, name, same)
		hits[name] = covering(30, 0)
	}
	for range 30 {
		rep, err := Measure(root, hits)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Over) != 5 {
			t.Fatalf("over = %v, want one per file", rep.Over)
		}
		var files []string
		for _, f := range rep.Over {
			files = append(files, f.File)
		}
		if got := strings.Join(files, ","); got != "a.go,b.go,c.go,d.go,e.go" {
			t.Fatalf("order = %s, want the identical scores ordered by where they are", got)
		}
	}
}

// A tree with more than one unreadable file names the same one every run. The
// walk is over a map, so without an ordering the failure a reader is handed
// changes between runs of the same command over the same tree.
func TestTheFirstFailureIsTheSameFailureEveryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hits := coverage.LineHits{}
	for _, name := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		write(t, root, name, "package a\n\nfunc (\n")
		hits[name] = covering(3, 1)
	}
	for range 30 {
		_, err := Measure(root, hits)
		if err == nil {
			t.Fatal("no error over a tree of unparseable files")
		}
		if !strings.Contains(err.Error(), "a.go") {
			t.Fatalf("err = %v, want the first file by name every time", err)
		}
	}
}

// A declared function is not scored, and the count of them rides on the report:
// a repository can annotate its way to nothing above the threshold, and that
// number is what makes it visible when one does.
func TestADeclaredFunctionIsNotScoredAndIsCounted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func tangled(n int) int {
	if n > 1 {
		n++
	}
	if n > 2 {
		n++
	}
	if n > 3 {
		n++
	}
	if n > 4 {
		n++
	}
	if n > 5 {
		n++
	}
	return n
}

// declared provisions something.
//
// [lydite:exclude_from_crap][the proving ground exercises this end to end; a
// unit test here would run the machine's own toolchain]
func declared(n int) int {
	if n > 1 {
		n++
	}
	if n > 2 {
		n++
	}
	if n > 3 {
		n++
	}
	if n > 4 {
		n++
	}
	if n > 5 {
		n++
	}
	if n > 6 {
		n++
	}
	return n
}
`)
	rep, err := Measure(root, coverage.LineHits{"a.go": covering(60, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Excluded != 1 {
		t.Errorf("excluded = %d, want the declared function counted", rep.Excluded)
	}
	if rep.Scored != 1 || rep.Above() != 1 {
		t.Fatalf("report = %+v, want only the undeclared function scored", rep)
	}
	if rep.Over[0].Name != "tangled" {
		t.Errorf("over = %v, want the declared one left out", rep.Over[0].Name)
	}
	// And out of the worst value too: a score that is not evidence about the
	// tests is not evidence about the repository's worst function either.
	if rep.Worst != rep.Over[0].Value {
		t.Errorf("worst = %v, want the scored function's %v", rep.Worst, rep.Over[0].Value)
	}
}

// A declaration that documents no function is carried back so the run can name
// it. Its author believes they have answered a score, and nothing they can see
// says otherwise.
func TestADeclarationCoveringNoFunctionIsCarriedBack(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

func f(n int) int {
	// [lydite:exclude_from_crap][written inside the body, where it does nothing]
	return n
}
`)
	rep, err := Measure(root, coverage.LineHits{"a.go": covering(10, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unused) != 1 || !strings.Contains(rep.Unused[0], "a.go:4") {
		t.Errorf("unused = %v, want the declaration located", rep.Unused)
	}
	if rep.Excluded != 0 {
		t.Errorf("excluded = %d, want nothing excluded by a declaration that documents no function", rep.Excluded)
	}
}

// A function excluded from coverage is excluded from the score and counted
// there too. Its lines are already gone from the hit map, so it would score
// nothing measurable and drop out in silence — uncounted, which is the one
// thing the excluded count exists to prevent.
func TestAFunctionExcludedFromCoverageIsCountedHereToo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "a.go", `package a

// provision fetches a toolchain.
//
// [lydite:exclude_from_coverage][the proving ground exercises this end to end]
func provision(n int) int {
	if n > 1 {
		n++
	}
	return n
}

func scored(n int) int {
	return n
}
`)
	// The hit map is what internal/coverage would hand over: the declared
	// function's lines are already gone from it.
	hits := covering(20, 1)
	for line := 5; line <= 11; line++ {
		delete(hits, line)
	}
	rep, err := Measure(root, coverage.LineHits{"a.go": hits})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Excluded != 1 {
		t.Errorf("excluded = %d, want the coverage-declared function counted", rep.Excluded)
	}
	if rep.Scored != 1 {
		t.Errorf("scored = %d, want only the undeclared function", rep.Scored)
	}
}
