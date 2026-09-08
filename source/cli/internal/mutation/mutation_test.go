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
	mismatch := ErrStaleMutant{Path: "a.go", Offset: 2, Length: 3, Size: 20, Want: "abc", Got: "xyz"}
	if got := mismatch.Error(); !strings.Contains(got, `"xyz"`) || !strings.Contains(got, `"abc"`) {
		t.Errorf("ErrStaleMutant.Error() = %q, want both texts", got)
	}
	// A range the source is too short to hold describes a different problem
	// from one that fits and holds something else, and reporting the second
	// for the first says the file's content is wrong when its length is.
	outside := ErrStaleMutant{Path: "a.go", Offset: 99, Length: 3, Size: 6, Want: "abc"}
	if got := outside.Error(); strings.Contains(got, `are ""`) {
		t.Errorf("ErrStaleMutant.Error() = %q, want it to name the range rather than empty content", got)
	}
	if got := outside.Error(); !strings.Contains(got, "outside") || !strings.Contains(got, "6") {
		t.Errorf("ErrStaleMutant.Error() = %q, want the source length", got)
	}
}

// A declaration is honoured for the mutants on its own line. Acknowledged
// rather than suppressed, so the count still reports what was claimed.
func TestADeclarationAcknowledgesTheMutantsOnItsLine(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\treturn a < b " + equiv("b is always a+1 here") + "\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(6))
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
	src := "package p\n\nfunc F(n int) bool {\n\t" + equiv("bound is arbitrary") + "\n\treturn n < 10\n}\n"
	got, _, err := GenerateGo("a.go", []byte(src), map[int]bool{5: true})
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
		"double-quoted string":             "\tprintln(\"" + bareDeclaration("not a claim") + "\")\n",
		"raw string":                       "\tprintln(`" + bareDeclaration("not a claim") + "`)\n",
		"multi-line raw string":            "\tprintln(`one\n" + bareDeclaration("not a claim") + "\nthree`)\n",
		"block comment":                    "\t/* " + bareDeclaration("not a claim") + " */\n",
		"block comment with an apostrophe": "\t/* don't */\n",
	}
	for name, middle := range cases {
		src := "package p\n\nfunc F(a, b int) bool {\n" + middle + "\treturn a < b\n}\n"
		got, _, err := GenerateGo("x.go", []byte(src), allLines(12))
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
	src := "package p\n\nfunc F(a, b int) bool {\n\ts := \"ab\" +\n\t\t\"cd\"\n\t_ = s\n\treturn a < b " + equiv("claimed") + "\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(10))
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
			t.Errorf("%s: the trailing declaration did not reach it", m)
		}
	}
	if !seen {
		t.Fatal("no comparison mutant")
	}
}

// A statement spanning lines can be declared on the line it closes on, which
// is where a trailing declaration goes and is not where the mutant over it is
// reported.
func TestADeclarationOnAStatementsClosingLineCoversIt(t *testing.T) {
	src := "package p\n\nfunc F(a int) {\n\tprintln(\n\t\ta,\n\t) " + equiv("printing is not observable") + "\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != RemoveStatement {
			continue
		}
		seen = true
		if !m.Acknowledged() {
			t.Errorf("%s: the declaration on the statement's closing line did not reach it", m)
		}
	}
	if !seen {
		t.Fatal("no remove-statement mutant for the multi-line call")
	}
}

// A declaration inside a statement's span still cannot reach past it: the
// span is bounded by the lines the caller asked for.
func TestADeclarationDoesNotReachPastTheStatementItIsIn(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\tprintln(\n\t\ta,\n\t) " + equiv("printing is not observable") + "\n\treturn a < b\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(9))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != ConditionalBoundary {
			continue
		}
		seen = true
		if m.Acknowledged() {
			t.Errorf("%s: acknowledged by a declaration belonging to the statement above", m)
		}
	}
	if !seen {
		t.Fatal("no comparison mutant on the line after the call")
	}
}

// The span is a floor and a ceiling both. A declaration written beneath a
// statement belongs to whatever comes next, and reaching down to it would
// acknowledge a mutant its author never claimed — the same silent exemption as
// reaching up, arriving from the other direction.
func TestADeclarationBelowAStatementDoesNotCoverIt(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\treturn a < b\n\t" + equiv("belongs to nothing above it") + "\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(7))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != ConditionalBoundary {
			continue
		}
		seen = true
		if m.Acknowledged() {
			t.Errorf("%s: acknowledged by a declaration written below it, reason %q", m, m.Reason)
		}
	}
	if !seen {
		t.Fatal("no comparison mutant on the line above the declaration")
	}
}

// A declaration beside an inner expression is a claim about that expression.
// Reaching it from the enclosing statement would acknowledge deleting a whole
// call on the strength of a reason about one operator inside it — never run,
// never counted, and the gate green over a survivor nobody claimed. The
// innermost mutant at the line is the one an author annotating that line is
// looking at, and it wins whether or not the statement spans several lines.
func TestADeclarationInsideAStatementDoesNotCoverTheStatement(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) {\n\tprintln(\n\t\ta < b, " + equiv("the comparison is unobservable") + "\n\t)\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var sawCall, sawInner bool
	for _, m := range got {
		switch m.Operator {
		case RemoveStatement:
			sawCall = true
			if m.Acknowledged() {
				t.Errorf("%s: the whole call was acknowledged by a claim about an inner expression", m)
			}
		case ConditionalBoundary:
			sawInner = true
			if !m.Acknowledged() {
				t.Errorf("%s: the declaration beside it did not reach it", m)
			}
		}
	}
	if !sawCall || !sawInner {
		t.Fatalf("fixture is not exercising both (call: %v, inner: %v)", sawCall, sawInner)
	}
}

// A multi-line statement can be declared on the line it opens on as readily as
// on the line it closes on, and a mutant over it is reported at the first. Both
// are lines the author wrote beside this statement and neither belongs to
// anything else.
func TestADeclarationOnAStatementsOpeningLineCoversIt(t *testing.T) {
	src := "package p\n\nfunc F(a int) {\n\tprintln( " + equiv("printing is not observable") + "\n\t\ta,\n\t)\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != RemoveStatement {
			continue
		}
		seen = true
		if !m.Acknowledged() {
			t.Errorf("%s: the declaration on the statement's opening line did not reach it", m)
		}
	}
	if !seen {
		t.Fatal("no remove-statement mutant for the multi-line call")
	}
}

// One line holds mutants at several scopes, and position alone cannot say
// which one an author meant. The innermost is what somebody annotating that
// line is looking at, so a claim about an operator acknowledges the operator
// and leaves deleting the whole call a mutant they have not answered.
func TestASingleLineClaimCoversTheInnermostMutant(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) {\n\tprintln(a < b) " + equiv("the comparison is unobservable") + "\n}\n"
	got, _, err := GenerateGo("x.go", []byte(src), allLines(6))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	var sawCall, sawInner bool
	for _, m := range got {
		switch m.Operator {
		case RemoveStatement:
			sawCall = true
			if m.Acknowledged() {
				t.Errorf("%s: deleting the whole call was acknowledged by a claim about one operator in it", m)
			}
		case ConditionalBoundary, NegateConditional:
			sawInner = true
			if !m.Acknowledged() {
				t.Errorf("%s: the declaration beside it did not reach it", m)
			}
		}
	}
	if !sawCall || !sawInner {
		t.Fatalf("fixture is not exercising both scopes (call: %v, inner: %v)", sawCall, sawInner)
	}
}

// A declaration written inside a statement is written inside it whichever line
// it lands on, so reach is decided by what a mutant replaces rather than by a
// window of lines around where it is reported.
func TestADeclarationOnAnInteriorLineCoversTheStatement(t *testing.T) {
	src := "package p\n\nfunc F(a int) {\n\tprintln(\n\t\ta, " + equiv("printing is not observable") + "\n\t)\n}\n"
	got, unmatched, err := GenerateGo("x.go", []byte(src), allLines(8))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(unmatched) != 0 {
		t.Errorf("declaration reported as covering nothing: %v", unmatched)
	}
	var seen bool
	for _, m := range got {
		if m.Operator != RemoveStatement {
			continue
		}
		seen = true
		if !m.Acknowledged() {
			t.Errorf("%s: the declaration written inside it did not reach it", m)
		}
	}
	if !seen {
		t.Fatal("no remove-statement mutant for the multi-line call")
	}
}

// An author who wrote a declaration is owed an answer about it. One covering
// no mutant leaves them believing a survivor is already answered, and nothing
// they can see says otherwise — the stance ErrNoReason takes on a declaration
// with no reason.
func TestADeclarationCoveringNoMutantIsReported(t *testing.T) {
	src := "package p\n\nfunc F() {\n\t" + equiv("nothing on this line") + "\n}\n"
	_, unmatched, err := GenerateGo("x.go", []byte(src), allLines(6))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(unmatched) != 1 {
		t.Fatalf("unmatched = %v, want one", unmatched)
	}
	if unmatched[0].Line != 4 || unmatched[0].Reason != "nothing on this line" {
		t.Errorf("unmatched[0] = %+v, want line 4 with its reason", unmatched[0])
	}
	if got := unmatched[0].String(); !strings.Contains(got, "x.go:4") || !strings.Contains(got, annotation.Marker(annotation.Mutation)) {
		t.Errorf("String() = %q, want the site and the token", got)
	}
}

// A declaration that did cover something is not reported as covering nothing.
func TestAMatchedDeclarationIsNotReported(t *testing.T) {
	src := "package p\n\nfunc F(a, b int) bool {\n\treturn a < b " + equiv("b is always a+1 here") + "\n}\n"
	_, unmatched, err := GenerateGo("x.go", []byte(src), allLines(6))
	if err != nil {
		t.Fatalf("GenerateGo: %v", err)
	}
	if len(unmatched) != 0 {
		t.Errorf("unmatched = %v, want none", unmatched)
	}
}

// bareDeclaration is a mutation declaration without its comment introducer, for a
// fixture that puts one somewhere no language would read a comment.
func bareDeclaration(reason string) string {
	return annotation.Marker(annotation.Mutation) + "[" + reason + "]"
}

// equiv is that declaration written as a line comment, so a fixture states the
// claim it is about rather than the grammar around it.
func equiv(reason string) string { return "// " + bareDeclaration(reason) }
