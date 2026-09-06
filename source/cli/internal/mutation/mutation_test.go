package mutation

import (
	"errors"
	"strings"
	"testing"
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
		lines = append(lines, r.Mutant.Path+":"+itoa(r.Mutant.Line)+":"+itoa(r.Mutant.Column))
	}
	want := []string{"a.go:2:1", "a.go:9:1", "a.go:9:2", "b.go:1:1"}
	if strings.Join(lines, " ") != strings.Join(want, " ") {
		t.Errorf("Survivors() = %v, want %v", lines, want)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
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
	if got := (ErrNoReason{Path: "a.go", Line: 7}).Error(); !strings.Contains(got, "a.go:7") || !strings.Contains(got, AnnotationToken) {
		t.Errorf("ErrNoReason.Error() = %q, want the site and the token", got)
	}
	if got := (ErrPathEscapes{Path: "../x.go"}).Error(); !strings.Contains(got, "../x.go") {
		t.Errorf("ErrPathEscapes.Error() = %q, want the path", got)
	}
	if got := (ErrStaleMutant{Path: "a.go", Offset: 2, Length: 3, Want: "abc", Got: "xyz"}).Error(); !strings.Contains(got, `"xyz"`) || !strings.Contains(got, `"abc"`) {
		t.Errorf("ErrStaleMutant.Error() = %q, want both texts", got)
	}
}

// A declaration on its own line is written above the statement it is about,
// so it covers the line below. A trailing one is a claim about the code to its
// left and says nothing about the next statement — carrying it down would
// acknowledge mutants nobody declared, and an acknowledged mutant is never run
// and never counted.
func TestATrailingDeclarationDoesNotReachTheNextLine(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\tprintln(a) " + AnnotationToken + " printing is not observable\n\treturn a < b\n}\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var sawLine4, sawLine5 bool
	for _, m := range got {
		switch m.Line {
		case 4:
			sawLine4 = true
			if !m.Acknowledged() {
				t.Errorf("%s: the trailing declaration must cover its own line", m)
			}
		case 5:
			sawLine5 = true
			if m.Acknowledged() {
				t.Errorf("%s: acknowledged by a claim written about the line above", m)
			}
		}
	}
	if !sawLine4 || !sawLine5 {
		t.Fatalf("fixture produced no mutants on both lines (line 4: %v, line 5: %v)", sawLine4, sawLine5)
	}
}

func TestADeclarationOnItsOwnLineCoversTheStatementBelow(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\t" + AnnotationToken + " the caller guarantees a != b\n\treturn a < b\n}\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no mutants; a declaration acknowledges rather than suppresses generation")
	}
	for _, m := range got {
		if !m.Acknowledged() {
			t.Errorf("%s: a declaration on the preceding line must cover it", m)
		}
		if m.Reason != "the caller guarantees a != b" {
			t.Errorf("%s: reason = %q", m, m.Reason)
		}
	}
}

// Source that merely quotes the annotation has declared nothing. Reading one
// out of a string literal acknowledges mutants silently, which is the one
// direction this must never fail in.
func TestATokenInsideALiteralDeclaresNothing(t *testing.T) {
	cases := map[string]string{
		"double-quoted": "\tprintln(\"" + AnnotationToken + " not a claim\")\n",
		"raw string":    "\tprintln(`" + AnnotationToken + " not a claim`)\n",
	}
	for name, line := range cases {
		src := "package p\n\nfunc F(a, b int) bool {\n" + line + "\treturn a < b\n}\n"
		got, err := GenerateGo("x.go", []byte(src), allLines(8))
		if err != nil {
			t.Fatalf("%s: GenerateGo: %v", name, err)
		}
		var onLiteralLine, below bool
		for _, m := range got {
			switch m.Line {
			case 4:
				onLiteralLine = true
			case 5:
				below = true
			}
			if m.Acknowledged() {
				t.Errorf("%s: %s acknowledged by a token inside a literal, reason %q", name, m, m.Reason)
			}
		}
		if !onLiteralLine || !below {
			t.Errorf("%s: fixture is not exercising both lines (literal line: %v, below: %v)", name, onLiteralLine, below)
		}
	}
}

// A raw string can span lines, so a scan that resets its state at every
// newline reads the code after the literal as if it were inside one.
func TestAMultiLineRawStringDoesNotSwallowTheCodeAfterIt(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\tprintln(`one\n" + AnnotationToken + " not a claim\nthree`)\n\treturn a < b\n}\n"
	got, err := GenerateGo("x.go", []byte(src), allLines(10))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Acknowledged() {
			t.Errorf("%s: acknowledged by a token inside a multi-line raw string, reason %q", m, m.Reason)
		}
		if m.Operator == ConditionalBoundary {
			seen = true
			if m.Line != 7 {
				t.Errorf("%s: reported at line %d, want 7 — line counting lost track inside the literal", m, m.Line)
			}
		}
	}
	if !seen {
		t.Fatal("no mutant for the comparison after a multi-line raw string")
	}
}

func TestABareDeclarationIsAnError(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\treturn a < b " + AnnotationToken + "\n}\n"
	_, err := GenerateGo("x.go", []byte(src), allLines(6))
	var want ErrNoReason
	if !errors.As(err, &want) {
		t.Fatalf("a bare declaration was accepted; err = %v, want ErrNoReason", err)
	}
	if want.Line != 4 {
		t.Errorf("ErrNoReason.Line = %d, want 4", want.Line)
	}
}
