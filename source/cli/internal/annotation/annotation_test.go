package annotation

import (
	"errors"
	"testing"
)

func TestADeclarationIsReadWithItsReason(t *testing.T) {
	got, err := Declarations("a.go", []Comment{
		{Line: 4, Text: Token + " the caller bounds n"},
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

// A declaration covers the line it is written on and no other. Carrying it
// down would let a declaration already in the tree acknowledge code a later
// change adds beneath it — excluded from the run and never counted, while the
// change adds no line holding the token, so nothing refers it.
func TestADeclarationCoversItsOwnLineOnly(t *testing.T) {
	got, err := Declarations("a.go", []Comment{{Line: 4, Text: Token + " bound is arbitrary"}})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Declarations returned %v, want the one line it was written on", got)
	}
	if _, ok := got[5]; ok {
		t.Error("the declaration reached the line below it")
	}
}

// A token that runs into its reason is a misspelling, and reading it as a
// declaration hands back a reason that is the rest of the typo.
func TestATokenMustEndWhereItIsWritten(t *testing.T) {
	got, err := Declarations("a.go", []Comment{{Line: 1, Text: Token + "foo"}})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("%q was read as a declaration: %v", Token+"foo", got)
	}
}

// A block comment cannot carry a declaration: its text does not open with the
// token, so quoting one in documentation declares nothing.
func TestABlockCommentIsNotADeclaration(t *testing.T) {
	got, err := Declarations("a.go", []Comment{{Line: 2, Text: "/* " + Token + " nope */"}})
	if err != nil {
		t.Fatalf("Declarations: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a token inside a block comment was honoured: %v", got)
	}
}

func TestABareDeclarationIsAnError(t *testing.T) {
	var want ErrNoReason
	_, err := Declarations("a.go", []Comment{{Line: 6, Text: Token}})
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want ErrNoReason", err)
	}
	if want.Line != 6 || want.Path != "a.go" {
		t.Errorf("ErrNoReason = %+v, want a.go:6", want)
	}
	if _, err := Declarations("a.go", []Comment{{Line: 6, Text: Token + "   "}}); !errors.As(err, &want) {
		t.Error("a declaration of nothing but whitespace was accepted")
	}
}
