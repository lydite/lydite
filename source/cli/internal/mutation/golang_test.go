package mutation

import (
	"errors"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// allLines is the unrestricted line set, for tests about what an operator
// produces rather than about where mutants are allowed.
func allLines(n int) map[int]bool {
	out := map[int]bool{}
	for i := 1; i <= n; i++ {
		out[i] = true
	}
	return out
}

func generate(t *testing.T, src string) []Mutant {
	t.Helper()
	m, err := GenerateGo("x.go", []byte(src), allLines(strings.Count(src, "\n")+1))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	return m
}

// apply builds a mutant's source, failing the test if the mutant does not
// describe the file it came from.
func apply(t *testing.T, m Mutant, src string) string {
	t.Helper()
	out, err := m.Apply([]byte(src))
	if err != nil {
		t.Fatalf("%s: Apply: %v", m, err)
	}
	return string(out)
}

func operators(mutants []Mutant) map[Operator]int {
	out := map[Operator]int{}
	for _, m := range mutants {
		out[m.Operator]++
	}
	return out
}

const boundarySrc = `package p

func Over(n int) bool {
	return n < 10
}
`

func TestRelationalOperatorYieldsBoundaryAndNegation(t *testing.T) {
	got := operators(generate(t, boundarySrc))
	if got[ConditionalBoundary] != 1 {
		t.Errorf("conditional-boundary mutants = %d, want 1", got[ConditionalBoundary])
	}
	if got[NegateConditional] != 1 {
		t.Errorf("negate-conditional mutants = %d, want 1", got[NegateConditional])
	}
}

func TestBoundaryShiftsTheOperatorAndNothingElse(t *testing.T) {
	for _, m := range generate(t, boundarySrc) {
		if m.Operator != ConditionalBoundary {
			continue
		}
		if m.Original != "<" || m.Mutated != "<=" {
			t.Fatalf("boundary rewrote %q to %q, want < to <=", m.Original, m.Mutated)
		}
		if !strings.Contains(apply(t, m, boundarySrc), "n <= 10") {
			t.Fatalf("mutated source lacks `n <= 10`:\n%s", apply(t, m, boundarySrc))
		}
		return
	}
	t.Fatal("no conditional-boundary mutant")
}

// Every mutant must describe exactly the edit it claims: the range it names
// holds its Original, and applying it produces that range replaced by its
// Mutated and nothing else touched.
//
// The assertion is on the recorded range rather than on a diff of the two
// sources, because a diff cannot locate a pure insertion unambiguously — the
// `<` to `<=` mutants are one — and an assertion that silently skips them
// leaves the operator producing the most mutants entirely unchecked.
func TestAMutantDescribesExactlyTheEditItMakes(t *testing.T) {
	const src = `package p

func F(a, b int) int {
	if a < b {
		a++
	}
	return a + b
}
`
	mutants := generate(t, src)
	if len(mutants) == 0 {
		t.Fatal("no mutants")
	}
	for _, m := range mutants {
		if m.Offset < 0 || m.Offset+m.Length > len(src) {
			t.Errorf("%s: range [%d,%d) is outside the file", m, m.Offset, m.Offset+m.Length)
			continue
		}
		if got := src[m.Offset : m.Offset+m.Length]; got != m.Original {
			t.Errorf("%s: range holds %q, but Original is %q", m, got, m.Original)
			continue
		}
		want := src[:m.Offset] + m.Mutated + src[m.Offset+m.Length:]
		if got := apply(t, m, src); got != want {
			t.Errorf("%s: applying it edited more than its own range:\n got %q\nwant %q", m, got, want)
		}
		// An operator swap replaces the operator and nothing around it. A
		// range measured wrongly still applies cleanly, so this is what
		// catches one.
		switch m.Operator {
		case ConditionalBoundary, NegateConditional, ArithmeticOperator:
			if !isOperatorText(m.Original) {
				t.Errorf("%s: replaced %q, which is not an operator — the range is wrong", m, m.Original)
			}
			if !isOperatorText(m.Mutated) {
				t.Errorf("%s: substituted %q, which is not an operator", m, m.Mutated)
			}
		}
	}
}

func isOperatorText(s string) bool {
	for _, op := range []string{"<", "<=", ">", ">=", "==", "!=", "+", "-", "*", "/", "%"} {
		if s == op {
			return true
		}
	}
	return false
}

// Apply refuses source the mutant does not describe. Splicing anyway would
// replace whatever now sits at that offset, producing a mutant nobody
// generated whose outcome is then reported against this operator.
func TestApplyRefusesSourceItDoesNotDescribe(t *testing.T) {
	m := Mutant{Path: "x.go", Offset: 0, Length: 3, Original: "abc", Mutated: "xyz"}
	if _, err := m.Apply([]byte("abcdef")); err != nil {
		t.Fatalf("Apply on matching source: %v", err)
	}
	var stale ErrStaleMutant
	_, err := m.Apply([]byte("ZZZdef"))
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want ErrStaleMutant", err)
	}
	if stale.Got != "ZZZ" || stale.Want != "abc" {
		t.Errorf("ErrStaleMutant reported %q vs %q, want ZZZ vs abc", stale.Got, stale.Want)
	}
	if _, err := m.Apply([]byte("ab")); !errors.As(err, &stale) {
		t.Errorf("a range past the end of the source: err = %v, want ErrStaleMutant", err)
	}
	// Mutant is an ordinary struct, so a caller can build one directly. The
	// guard exists for that caller, and a negative field must not reach the
	// slice expression.
	for _, bad := range []Mutant{
		{Path: "x.go", Offset: -1, Length: 1, Original: "a"},
		{Path: "x.go", Offset: 0, Length: -1, Original: "a"},
		{Path: "x.go", Offset: 1<<62 + 1, Length: 1<<62 + 1, Original: "a"},
	} {
		if _, err := bad.Apply([]byte("abcdef")); !errors.As(err, &stale) {
			t.Errorf("Apply with offset %d length %d: err = %v, want ErrStaleMutant", bad.Offset, bad.Length, err)
		}
	}
}

// An operator swap must never produce source the compiler cannot read: that
// would report every such mutant unviable and quietly remove the operator
// from the catalogue while the run still went green.
func TestOperatorSwapsStillParse(t *testing.T) {
	const src = `package p

func F(a, b int) int {
	if a < b && a != 0 {
		return a * b
	}
	return a + b
}
`
	for _, m := range generate(t, src) {
		if m.Operator == RemoveStatement {
			continue
		}
		if _, err := parser.ParseFile(token.NewFileSet(), "x.go", apply(t, m, src), parser.SkipObjectResolution); err != nil {
			t.Errorf("%s: mutant does not parse: %v", m, err)
		}
	}
}

// A raw string literal's Value has its carriage returns stripped, so a range
// measured from that text is short by one byte per line and leaves a dangling
// backtick. Ranges come from the node, which is what keeps this file parsing.
func TestACarriageReturnInARawStringDoesNotShortenTheRange(t *testing.T) {
	src := "package p\r\n\r\nfunc F() string {\r\n\treturn `a\r\nb`\r\n}\r\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(10))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != ReplaceReturn {
			continue
		}
		seen = true
		if _, err := parser.ParseFile(token.NewFileSet(), "x.go", apply(t, m, src), parser.SkipObjectResolution); err != nil {
			t.Errorf("%s: mutant does not parse: %v", m, err)
		}
	}
	if !seen {
		t.Fatal("no replace-return mutant for the raw string")
	}
}

func TestRemoveStatementTakesCallsAndIncrementsOnly(t *testing.T) {
	const src = `package p

func F(n int) int {
	sum := 0
	println(n)
	sum++
	return sum
}
`
	var removed []string
	for _, m := range generate(t, src) {
		if m.Operator == RemoveStatement {
			removed = append(removed, strings.TrimSpace(m.Original))
		}
	}
	want := map[string]bool{"println(n)": true, "sum++": true}
	if len(removed) != len(want) {
		t.Fatalf("removed %v, want exactly %v", removed, want)
	}
	for _, r := range removed {
		if !want[r] {
			t.Errorf("removed %q, which takes an identifier's only definition with it", r)
		}
	}
}

// A substitution decides on the literal's value, not its spelling. `0.` and
// `0e0` are zero written differently, and replacing either with `0.0` is a
// mutant nothing can kill — reported as a survivor, failing the gate on
// correct code.
func TestReturnSubstitutionChangesTheValueNotTheSpelling(t *testing.T) {
	const src = `package p

func A() float64 { return 0. }
func B() float64 { return 0e0 }
func C() string  { return ` + "``" + ` }
func D() int     { return 0 }
func E() string  { return "hello" }
func G() bool    { return true }
`
	got := map[string]string{}
	for _, m := range generate(t, src) {
		if m.Operator == ReplaceReturn {
			got[m.Original] = m.Mutated
		}
	}
	for original, unwanted := range map[string]string{"0.": "0.0", "0e0": "0.0", "``": `""`} {
		if got[original] == unwanted {
			t.Errorf("return %s mutated to %q, which is the same value written differently", original, unwanted)
		}
		if got[original] == "" {
			t.Errorf("return %s produced no mutant", original)
		}
	}
	for original, want := range map[string]string{"0": "1", `"hello"`: `""`, "true": "false"} {
		if got[original] != want {
			t.Errorf("return %s mutated to %q, want %q", original, got[original], want)
		}
	}
}

// A returned identifier or call has no substitute certain to compile, and a
// mutant that reliably fails to build costs a compilation and teaches nothing.
func TestReturnSubstitutionSkipsWhatItCannotType(t *testing.T) {
	const src = `package p

func F(x int) int { return x }
func G() error    { return nil }
`
	for _, m := range generate(t, src) {
		if m.Operator == ReplaceReturn {
			t.Errorf("%s: substituted a value it cannot know the type of", m)
		}
	}
}

// The restriction covers every line a mutant edits, not only the line it is
// reported at. Asserting on the reported line instead would restate the filter
// the generator already applied and could not fail.
func TestNoMutantEditsALineOutsideTheGivenSet(t *testing.T) {
	const src = `package p

func F(a, b int) bool {
	println(
		a,
	)
	if a < b {
		return true
	}
	return a > b
}
`
	// Line 4 opens a call spanning lines 4 to 6; line 7 holds a whole test.
	for _, lines := range []map[int]bool{{4: true}, {7: true}, {4: true, 7: true}} {
		got, err := GenerateGo("x.go", []byte(src), lines)
		if err != nil {
			t.Fatalf("GenerateGo: %v", err)
		}
		for _, m := range got {
			mutated := apply(t, m, src)
			for _, line := range diffLines(src, mutated) {
				if !lines[line] {
					t.Errorf("%s: edited line %d, outside the requested set %v", m, line, keys(lines))
				}
			}
		}
	}
}

// diffLines returns the 1-indexed lines of a that b does not reproduce
// identically, comparing position by position from both ends so an inserted
// or deleted line does not report every line after it.
func diffLines(a, b string) []int {
	as, bs := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []int
	head := 0
	for head < len(as) && head < len(bs) && as[head] == bs[head] {
		head++
	}
	tail := 0
	for tail < len(as)-head && tail < len(bs)-head && as[len(as)-1-tail] == bs[len(bs)-1-tail] {
		tail++
	}
	for i := head; i < len(as)-tail; i++ {
		out = append(out, i+1)
	}
	return out
}

func keys(m map[int]bool) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestGeneratedAndTestFilesAreNotMutated(t *testing.T) {
	const generated = `// Code generated by stringer. DO NOT EDIT.

package p

func F(a, b int) bool { return a < b }
`
	if got := generate(t, generated); len(got) != 0 {
		t.Errorf("generated file produced %d mutants, want 0", len(got))
	}

	const test = `package p

func F(a, b int) bool { return a < b }
`
	got, err := GenerateGo("x_test.go", []byte(test), allLines(10))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("_test.go produced %d mutants, want 0", len(got))
	}
}

// A mutant's path is joined to a worker directory and written there, so the
// invariant is established where the value is produced rather than left to
// every consumer to remember.
func TestGenerateRefusesAPathOutsideTheComponent(t *testing.T) {
	const src = "package p\n\nfunc F(a, b int) bool { return a < b }\n"
	for _, path := range []string{"/etc/passwd", "../outside.go", "", ".", "..", "a/../../b.go", "a/../.."} {
		var escapes ErrPathEscapes
		_, err := GenerateGo(path, []byte(src), allLines(5))
		if !errors.As(err, &escapes) {
			t.Errorf("GenerateGo(%q) err = %v, want ErrPathEscapes", path, err)
		}
	}
	if _, err := GenerateGo("pkg/a.go", []byte(src), allLines(5)); err != nil {
		t.Errorf("GenerateGo on an ordinary relative path: %v", err)
	}
}
