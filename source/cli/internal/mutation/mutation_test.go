package mutation

import (
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/annotation"
)

// Unviable and acknowledged mutants say nothing about the suite — one could
// not be built, the other has been declared unkillable — so counting either in
// the denominator reports a score the tests did not earn.
func TestTheDenominatorExcludesUnviableAndAcknowledged(t *testing.T) {
	var s Summary
	for _, o := range []Outcome{Killed, Killed, TimedOut, Survived, Unviable, Unviable, Acknowledged} {
		s.Add(Result{Outcome: o})
	}
	if got := s.Total(); got != 7 {
		t.Errorf("Total() = %d, want 7", got)
	}
	if got := s.Denominator(); got != 4 {
		t.Errorf("Denominator() = %d, want 4 (2 killed + 1 timed out + 1 survived)", got)
	}
	killed, total := s.Score()
	if killed != 3 || total != 4 {
		t.Errorf("Score() = %d/%d, want 3/4 — a timeout scores as a kill", killed, total)
	}
}

// The gate is a boolean over survivors, which is what lets it survive a
// changing operator catalogue. Nothing else votes.
func TestOnlyASurvivorFailsTheGate(t *testing.T) {
	for _, o := range []Outcome{Killed, TimedOut, Unviable, Acknowledged} {
		var s Summary
		s.Add(Result{Outcome: o})
		if !s.Passed() {
			t.Errorf("a lone %s outcome failed the gate", o)
		}
	}
	var s Summary
	s.Add(Result{Outcome: Killed})
	s.Add(Result{Outcome: Survived})
	if s.Passed() {
		t.Error("a survivor passed the gate")
	}
}

func TestSurvivorsAreFilteredAndOrderedForAReader(t *testing.T) {
	results := []Result{
		{Outcome: Survived, Mutant: Mutant{Path: "b.go", Line: 1, Column: 1}},
		{Outcome: Killed, Mutant: Mutant{Path: "a.go", Line: 1, Column: 1}},
		{Outcome: Survived, Mutant: Mutant{Path: "a.go", Line: 9, Column: 2}},
		{Outcome: Unviable, Mutant: Mutant{Path: "a.go", Line: 2, Column: 1}},
		{Outcome: Survived, Mutant: Mutant{Path: "a.go", Line: 9, Column: 1}},
		{Outcome: Acknowledged, Mutant: Mutant{Path: "a.go", Line: 3, Column: 1}},
		{Outcome: Survived, Mutant: Mutant{Path: "a.go", Line: 2, Column: 1}},
	}
	got := Survivors(results)
	var lines []string
	for _, r := range got {
		if r.Outcome != Survived {
			t.Fatalf("Survivors returned a %s outcome", r.Outcome)
		}
		lines = append(lines, r.Mutant.Path+":"+strconv.Itoa(r.Mutant.Line)+":"+strconv.Itoa(r.Mutant.Column))
	}
	want := []string{"a.go:2:1", "a.go:9:1", "a.go:9:2", "b.go:1:1"}
	if strings.Join(lines, " ") != strings.Join(want, " ") {
		t.Errorf("Survivors() = %v, want %v", lines, want)
	}
}

// A mutant quotes source, which holds whatever the source holds — a newline
// among it. Unquoted, a removed statement could put text shaped like one of
// lydite's own status rows at the start of a line in a report.
func TestStringQuotesAndClipsTheSourceItCarries(t *testing.T) {
	m := Mutant{
		Path: "a.go", Line: 4, Column: 2, Operator: RemoveStatement,
		Original: "println(\n\ta,\n)", Mutated: "",
	}
	got := m.String()
	if strings.Contains(got, "\n") {
		t.Errorf("String() carried a raw newline into a report line: %q", got)
	}
	if !strings.HasPrefix(got, "a.go:4:2: remove-statement ") {
		t.Errorf("String() = %q, want it to open with the site", got)
	}

	long := Mutant{Path: "a.go", Original: strings.Repeat("x\n", displayLimit)}
	if got := long.String(); strings.Contains(got, "\n") {
		t.Errorf("String() carried a raw newline from a clipped statement: %q", got)
	}
	if n := len(long.String()); n > displayLimit*3 {
		t.Errorf("String() length %d for a long statement, want it clipped", n)
	}
}

func TestErrorsSayWhatIsWrongAndWhere(t *testing.T) {
	if got := (ErrPathEscapes{Path: "../x.go"}).Error(); !strings.Contains(got, "../x.go") {
		t.Errorf("ErrPathEscapes.Error() = %q, want the path", got)
	}
	if got := (ErrStaleMutant{Path: "a.go", Offset: 2, Length: 3, Want: "abc", Got: "xyz"}).Error(); !strings.Contains(got, `"xyz"`) || !strings.Contains(got, `"abc"`) {
		t.Errorf("ErrStaleMutant.Error() = %q, want both texts", got)
	}
}

// A declaration is honoured for the mutants on its own line. Acknowledged
// rather than suppressed, so the count still reports what was claimed.
func TestADeclarationAcknowledgesTheMutantsOnItsLine(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\treturn a < b " + annotation.Token + " b is always a+1 here\n}\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(6))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no mutants; a declaration acknowledges rather than stopping generation")
	}
	for _, m := range got {
		if !m.Acknowledged() {
			t.Errorf("%s: not acknowledged by the declaration on its line", m)
		}
		if m.Reason != "b is always a+1 here" {
			t.Errorf("%s: reason = %q", m, m.Reason)
		}
	}
}

// A declaration already in the tree must not reach code a later change adds
// beneath it. The mutant would be excluded and never run, while the change
// itself adds no line holding the token — so nothing refers it and it merges
// unread.
func TestADeclarationDoesNotReachTheLineBelow(t *testing.T) {
	src := "package p\n\nfunc F(n int) bool {\n\t" + annotation.Token + " bound is arbitrary\n\treturn n < 10\n}\n"
	got, err := GenerateGo("a.go", []byte(src), map[int]bool{5: true})
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("fixture produced no mutant on the changed line")
	}
	for _, m := range got {
		if m.Acknowledged() {
			t.Errorf("%s: acknowledged by a declaration the change never touched, reason %q", m, m.Reason)
		}
	}
}

// The parser decides what a comment is, so none of these declares anything:
// a token inside a string, inside a block comment, after an apostrophe in
// prose, or after a string holding a line continuation.
func TestOnlyARealLineCommentDeclares(t *testing.T) {
	cases := map[string]string{
		"double-quoted string":             "\tprintln(\"" + annotation.Token + " not a claim\")\n",
		"raw string":                       "\tprintln(`" + annotation.Token + " not a claim`)\n",
		"multi-line raw string":            "\tprintln(`one\n" + annotation.Token + " not a claim\nthree`)\n",
		"block comment":                    "\t/* " + annotation.Token + " not a claim */\n",
		"block comment with an apostrophe": "\t/* don't */\n",
	}
	for name, middle := range cases {
		src := "package p\n\nfunc F(a, b int) bool {\n" + middle + "\treturn a < b\n}\n"
		got, err := GenerateGo("x.go", []byte(src), allLines(12))
		if err != nil {
			t.Fatalf("%s: GenerateGo: %v", name, err)
		}
		var seen bool
		for _, m := range got {
			if m.Operator == ConditionalBoundary {
				seen = true
			}
			if m.Acknowledged() {
				t.Errorf("%s: %s acknowledged, reason %q", name, m, m.Reason)
			}
		}
		if !seen {
			t.Errorf("%s: fixture produced no comparison mutant, so it asserts nothing", name)
		}
	}
}

// A string holding a line continuation must not shift which line a later
// declaration is attributed to.
func TestALineContinuationDoesNotShiftADeclaration(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\ts := \"ab\" +\n\t\t\"cd\"\n\t_ = s\n\treturn a < b " + annotation.Token + " claimed\n}\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(10))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != ConditionalBoundary {
			continue
		}
		seen = true
		if m.Line != 7 {
			t.Errorf("%s: reported at line %d, want 7", m, m.Line)
		}
		if !m.Acknowledged() {
			t.Errorf("%s: the declaration on its own line did not reach it", m)
		}
	}
	if !seen {
		t.Fatal("no comparison mutant")
	}
}
