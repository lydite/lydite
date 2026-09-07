package crap

import (
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
	// 1 base + if + && + || + for + range + two non-default cases + one
	// communicating select clause = 9. `default` is not a decision in either
	// statement: control reaches it when every other clause is decided
	// against.
	if got := functions(t, root, "a.go")[0].Complexity; got != 9 {
		t.Errorf("complexity = %d, want 9", got)
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
	out, err := scoreFile(token.NewFileSet(), root, file, covering(400, 1))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
