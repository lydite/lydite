package annotation

import (
	"errors"
	"strings"
	"testing"
)

// open is a declaration's opening text for one gate, so a test states the
// reason it cares about and nothing else.
func open(g Gate, reason string) string { return "// " + Marker(g) + "[" + reason }

func TestADeclarationIsReadWithItsReason(t *testing.T) {
	t.Parallel()
	got, err := Declarations("a.go", Mutation, []Comment{
		{Line: 4, Text: open(Mutation, "the caller bounds n]")},
		{Line: 9, Text: "// an ordinary comment"},
	})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if got[4] != "the caller bounds n" {
		t.Errorf("line 4 reason = %q", got[4])
	}
	if _, ok := got[9]; ok {
		t.Error("an ordinary comment was read as a declaration")
	}
}

// The gate is inside the token, so a declaration answers one finding and no
// other: a function whose coverage is taken in another process has not thereby
// become unmutable.
func TestADeclarationAnswersOneGate(t *testing.T) {
	t.Parallel()
	comments := []Comment{{Line: 4, Text: open(Coverage, "the proving ground exercises it]")}}
	got, err := Declarations("a.go", Coverage, comments)
	if err != nil || got[4] == "" {
		t.Fatalf("Declarations(coverage) = (%v, %v), want the reason", got, err)
	}
	for _, other := range []Gate{Mutation, CRAP} {
		if got, err := Declarations("a.go", other, comments); err != nil || len(got) != 0 {
			t.Errorf("Declarations(%s) = (%v, %v), want nothing — it names another gate", other, got, err)
		}
	}
}

// A declaration is keyed to the line its token is written on, and never to a
// line its reason wrapped onto. How far a caller lets it reach from there is the
// caller's rule; what is fixed here is that this function invents no second line
// for it.
func TestADeclarationIsKeyedToTheLineItsTokenIsOn(t *testing.T) {
	t.Parallel()
	got, err := Declarations("a.go", CRAP, []Comment{
		{Line: 4, Text: open(CRAP, "the proving ground exercises this end to end, and a unit")},
		{Line: 5, Text: "// test here would run the machine's own toolchain]"},
	})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if len(got) != 1 || got[4] == "" {
		t.Fatalf("Declarations returned %v, want the one line the token is on", got)
	}
	if _, ok := got[5]; ok {
		t.Error("the line the reason wrapped onto became a declaration of its own")
	}
}

// A reason runs to the first `]`, across as many comment lines as it takes.
// That delimiter is what lets a reason wrap at all — an undelimited one is
// capped by whatever line length a repository's linter enforces.
func TestAReasonWrapsAcrossCommentLines(t *testing.T) {
	t.Parallel()
	got, err := Declarations("a.go", CRAP, []Comment{
		{Line: 10, Text: open(CRAP, "the proving ground exercises this end to end; a unit")},
		{Line: 11, Text: "// test here would run the machine's own toolchain rather"},
		{Line: 12, Text: "// than lydite's code]"},
	})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	want := "the proving ground exercises this end to end; a unit test here would run " +
		"the machine's own toolchain rather than lydite's code"
	if got[10] != want {
		t.Errorf("reason = %q, want %q", got[10], want)
	}
}

// A continuation must be the very next line. A language's parser hands over
// every comment in the file, so the next entry may be pages away — and a reason
// that swallowed it would silence a declaration nobody wrote and eat the prose
// in between.
func TestAReasonDoesNotReachAcrossAGap(t *testing.T) {
	t.Parallel()
	var want ErrNoReason
	_, err := Declarations("a.go", CRAP, []Comment{
		{Line: 10, Text: open(CRAP, "never closed on this line")},
		{Line: 40, Text: "// a comment about something else]"},
	})
	if !errors.As(err, &want) || want.Line != 10 {
		t.Errorf("err = %v, want ErrNoReason at line 10 — the closing bracket is 30 lines away", err)
	}
}

// A token that runs into its reason is a misspelling, and reading it as a
// declaration hands back a reason that is the rest of the typo.
func TestATokenMustBeFollowedByItsBracket(t *testing.T) {
	t.Parallel()
	var want ErrNoReason
	if _, err := Declarations("a.go", CRAP, []Comment{
		{Line: 1, Text: "// " + Marker(CRAP) + "foo]"},
	}); !errors.As(err, &want) {
		t.Errorf("err = %v, want a declaration with no bracket refused", err)
	}
	// A token of another shape entirely is not this gate's declaration and is
	// not an error either — it is somebody else's comment.
	got, err := Declarations("a.go", CRAP, []Comment{{Line: 1, Text: "// [lydite:something_else][x]"}})
	if err != nil || len(got) != 0 {
		t.Errorf("Declarations = (%v, %v), want an unrelated token passed over", got, err)
	}
}

// A block comment cannot carry a declaration: its text does not open with the
// token, so quoting one in documentation declares nothing.
func TestABlockCommentIsNotADeclaration(t *testing.T) {
	t.Parallel()
	got, err := Declarations("a.go", Mutation, []Comment{
		{Line: 2, Text: "/* " + Marker(Mutation) + "[nope] */"},
	})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a token inside a block comment was honoured: %v", got)
	}
}

// The introducer and the space after it are stripped, so a declaration reads
// the same however tightly its author wrote it.
func TestTheIntroducerIsStrippedEitherWay(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"// " + Marker(CRAP) + "[a reason]",
		"//" + Marker(CRAP) + "[a reason]",
		"//\t" + Marker(CRAP) + "[a reason]",
	} {
		got, err := Declarations("a.go", CRAP, []Comment{{Line: 1, Text: text}})
		if err != nil || got[1] != "a reason" {
			t.Errorf("Declarations(%q) = (%v, %v), want the reason", text, got, err)
		}
	}
}

func TestADeclarationWithNothingInItIsAnError(t *testing.T) {
	t.Parallel()
	var want ErrNoReason
	_, err := Declarations("a.go", Mutation, []Comment{{Line: 6, Text: "// " + Marker(Mutation) + "[]"}})
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want ErrNoReason", err)
	}
	if want.Line != 6 || want.Path != "a.go" || want.Gate != Mutation {
		t.Errorf("ErrNoReason = %+v, want a.go:6 for mutation", want)
	}
	// The message names the token an author has to fix, gate and all.
	if !strings.Contains(want.Error(), Marker(Mutation)) {
		t.Errorf("message = %q, want it to name the token", want.Error())
	}
	if _, err := Declarations("a.go", Mutation, []Comment{
		{Line: 6, Text: "// " + Marker(Mutation) + "[   ]"},
	}); !errors.As(err, &want) {
		t.Error("a declaration of nothing but whitespace was accepted")
	}
	// And one nothing ever closes, which is the shape a missing bracket takes.
	if _, err := Declarations("a.go", Mutation, []Comment{
		{Line: 6, Text: "// " + Marker(Mutation) + "[a reason with no end"},
	}); !errors.As(err, &want) {
		t.Error("an unterminated reason was accepted")
	}
}

// Every gate's token shares one prefix, which is what internal/referral reads:
// a suppression is a suppression whichever gate it silences, and a list of
// tokens there would go stale the first time a gate is added.
func TestEveryGateSharesThePrefix(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, g := range []Gate{Mutation, CRAP, Coverage} {
		m := Marker(g)
		if !strings.HasPrefix(m, Prefix) {
			t.Errorf("Marker(%s) = %q, want it to open with %q", g, m, Prefix)
		}
		if seen[m] {
			t.Errorf("Marker(%s) = %q, which another gate already uses", g, m)
		}
		seen[m] = true
	}
}
