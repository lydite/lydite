// Package annotation holds the equivalence declaration: the token an author
// writes, and the rule for reading one out of a language's comments.
//
// Two packages need it and neither should depend on the other. internal/mutation
// honours a declaration by generating the mutant, counting it and never running
// it; internal/referral recognises the same token as a suppression, so a change
// that adds one is referred rather than merged unattended. Those are the two
// halves of one bargain — kill the mutant and merge unattended, or declare it
// unkillable and have a human read the claim — and a token spelled twice could
// leave one half honouring what the other does not see.
//
// It is a leaf: nothing here imports anything of lydite's. internal/referral
// decides what merges without being read, and linking a language parser and a
// download client into that decision to obtain one string is a dependency
// nobody asked for.
package annotation

import (
	"fmt"
	"strings"
)

// Token is how an author declares a mutant equivalent. Go, Rust and
// TypeScript all spell a line comment "//", so one token covers them.
const Token = "//lydite:equivalent"

// Comment is one comment as a language's own parser reports it: the line it
// starts on, and its text including the introducer.
//
// Each language answers what a comment is, because each language already
// knows. A scan that decided for itself has to lex three languages correctly
// to be right once — block comments, an apostrophe in prose, a Rust lifetime,
// a string continuation — and every case it gets wrong either honours a
// declaration nobody made or drops one somebody did.
type Comment struct {
	Line int
	Text string
}

// ErrNoReason reports a declaration with nothing after it.
//
// It is an error rather than an ignored comment, because a declaration
// silently not honoured reads as the engine disregarding its author, who then
// has a survivor they believe they have already answered. A reason is required
// for the cause the exemption set requires one: the declaration is the entire
// risk record for a mutant nobody can kill, and a bare token is not reviewable.
type ErrNoReason struct {
	Path string
	Line int
}

func (e ErrNoReason) Error() string {
	return fmt.Sprintf("%s:%d: %s needs a reason after it", e.Path, e.Line, Token)
}

// Declarations returns the reason declared on each line, keyed by that line.
//
// A declaration belongs to the line it is written on. How far one reaches from
// there is the caller's rule, because only the caller knows what a mutant
// occupies — but every caller owes the same bound: a declaration must never
// reach a line an author was not writing about. One that reached further would
// let a declaration already in the tree acknowledge code a later change adds
// nearby, and that mutant is excluded and never run while the change itself
// adds no line holding the token, so nothing refers it and it merges unread.
// Kept to lines the author wrote beside the mutant, the bargain holds by
// construction: a mutant exists only on a changed line, so a declaration that
// acknowledges one sits on a changed line, which internal/referral sees as
// added.
func Declarations(path string, comments []Comment) (map[int]string, error) {
	out := map[int]string{}
	for _, c := range comments {
		rest, ok := strings.CutPrefix(c.Text, Token)
		if !ok {
			continue
		}
		// The token has to end where it is written, or a mistyped
		// "//lydite:equivalentfoo" reads as a declaration whose reason is
		// the rest of its own misspelling.
		if rest != "" && !isSpace(rest[0]) {
			continue
		}
		reason := strings.TrimSpace(rest)
		if reason == "" {
			return nil, ErrNoReason{Path: path, Line: c.Line}
		}
		out[c.Line] = reason
	}
	return out, nil
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f'
}
