package mutation

import (
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
		if !strings.Contains(string(m.Source), "n <= 10") {
			t.Fatalf("mutated source lacks `n <= 10`:\n%s", m.Source)
		}
		return
	}
	t.Fatal("no conditional-boundary mutant")
}

// A mutant that differs from its original anywhere but the site is a mutant
// whose compile error cannot be attributed, and two mutants of one file would
// differ from each other for reasons that are not the mutation. Splicing at
// byte offsets is what holds this; printing a rewritten tree would not.
func TestAMutantDiffersInExactlyOnePlace(t *testing.T) {
	const src = `package p

func F(a, b int) int {
	if a < b {
		a++
	}
	return a + b
}
`
	for _, m := range generate(t, src) {
		lo, hi := commonAffixes(src, string(m.Source))
		if lo+hi > len(src) {
			t.Errorf("%s: mutant overlaps itself, not a single contiguous edit", m)
			continue
		}
		removed := src[lo : len(src)-hi]
		if !strings.Contains(m.Original, strings.TrimSpace(removed)) && strings.TrimSpace(removed) != "" {
			t.Errorf("%s: edited %q, which its Original %q does not cover", m, removed, m.Original)
		}
	}
}

// commonAffixes returns the length of the shared prefix and shared suffix of
// two strings, which bracket the one region that differs.
func commonAffixes(a, b string) (prefix, suffix int) {
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	return prefix, suffix
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
		if _, err := parser.ParseFile(token.NewFileSet(), "x.go", m.Source, parser.SkipObjectResolution); err != nil {
			t.Errorf("%s: mutant does not parse: %v\n%s", m, err, m.Source)
		}
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

func TestReturnSubstitutionKeepsTheLiteralKind(t *testing.T) {
	const src = `package p

func B() bool   { return true }
func N() int    { return 7 }
func S() string { return "hello" }
`
	got := map[string]string{}
	for _, m := range generate(t, src) {
		if m.Operator == ReplaceReturn {
			got[m.Original] = m.Mutated
		}
	}
	for original, want := range map[string]string{"true": "false", "7": "0", `"hello"`: `""`} {
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

func TestMutantsComeOnlyFromTheGivenLines(t *testing.T) {
	const src = `package p

func F(a, b int) bool {
	if a < b {
		return true
	}
	return a > b
}
`
	// Line 4 alone: the `a < b` test, and nothing else in the function.
	got, err := GenerateGo("x.go", []byte(src), map[int]bool{4: true})
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no mutants from the one line that has them")
	}
	for _, m := range got {
		if m.Line != 4 {
			t.Errorf("%s: generated outside the requested line set", m)
		}
	}
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

func TestAnnotationAcknowledgesTheMutantsOnItsLine(t *testing.T) {
	const src = `package p

func F(a, b int) bool {
	return a < b //lydite:equivalent b is always a+1 here
}
`
	got := generate(t, src)
	if len(got) == 0 {
		t.Fatal("annotation suppressed generation; it must acknowledge, not skip")
	}
	for _, m := range got {
		if !m.Acknowledged() {
			t.Errorf("%s: not acknowledged by the annotation on its line", m)
		}
		if m.Reason != "b is always a+1 here" {
			t.Errorf("%s: reason = %q", m, m.Reason)
		}
	}
}

func TestAnnotationAboveTheStatementCoversIt(t *testing.T) {
	const src = `package p

func F(a, b int) bool {
	//lydite:equivalent the caller guarantees a != b
	return a < b
}
`
	for _, m := range generate(t, src) {
		if !m.Acknowledged() {
			t.Errorf("%s: an annotation on the preceding line must cover it", m)
		}
	}
}

func TestAnnotationWithoutAReasonIsAnError(t *testing.T) {
	const src = `package p

func F(a, b int) bool {
	return a < b //lydite:equivalent
}
`
	_, err := GenerateGo("x.go", []byte(src), allLines(6))
	var want ErrNoReason
	if err == nil {
		t.Fatal("a bare annotation was accepted; it must be an error, not silently ignored")
	}
	if !asErrNoReason(err, &want) {
		t.Fatalf("err = %v, want ErrNoReason", err)
	}
	if want.Line != 4 {
		t.Errorf("ErrNoReason.Line = %d, want 4", want.Line)
	}
}

func asErrNoReason(err error, out *ErrNoReason) bool {
	e, ok := err.(ErrNoReason)
	if ok {
		*out = e
	}
	return ok
}
