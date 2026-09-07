// Package annotation holds lydite's source declarations: the tokens an author
// writes to say a gate's finding is not evidence about their code, and the rule
// for reading one out of a language's comments.
//
// Every gate that reports per-site findings needs one, because every one of them
// can be right about the code and wrong about what the code is for: a mutant no
// test could kill, a function whose coverage is taken in another process. The
// declaration is how an author says which, and the reason is the whole of the
// record — so it is required, and a bare token is refused.
//
// One grammar, `[lydite:exclude_from_<gate>][<reason>]`, because two tokens
// spelled two ways are two things to learn and two things to get wrong. The
// gate is inside the token so a declaration answers one finding and no other: a
// function whose coverage is taken in another process has not thereby become
// unmutable, and a mutant nothing can kill says nothing about coverage.
//
// The reason is delimited rather than running to the end of its line, and that
// is what lets it wrap: an undelimited reason is capped by whatever line length
// a repository's linter enforces, and joining the next comment line instead
// needs a rule for when that line is a continuation and when it is prose.
//
// Four packages need this and none should depend on another. internal/mutation
// honours a declaration by generating the mutant, counting it and never running
// it; internal/coverage and internal/crap drop the function one names; and
// internal/referral recognises any of the tokens as a suppression, so a change
// that adds one is referred rather than merged unattended. Those are the two
// halves of one bargain — clear the gate and merge unattended, or declare it
// cannot be cleared and have a human read the claim — and a token spelled twice
// could leave one half honouring what the other does not see.
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

// Gate names a check a declaration can say a site is not evidence for.
type Gate string

const (
	// Mutation declares a mutant no test could kill, because the change it
	// makes is unobservable.
	Mutation Gate = "mutation"
	// CRAP declares a function whose complexity-and-coverage score is not
	// evidence about how well it is tested.
	CRAP Gate = "crap"
	// Coverage declares a function whose coverage this component's suite does
	// not measure, so its lines belong to neither side of a coverage figure.
	Coverage Gate = "coverage"
)

// Prefix opens every declaration, whatever gate it names.
//
// internal/referral reads this rather than the individual tokens: a suppression
// is a suppression whichever gate it silences, and a list of tokens there would
// go stale the first time a gate is added — silently, and in the one place
// where a missed suppression means a change merges unread.
//
// Named for what it is rather than called a token, which gosec reserves for
// credentials: G101 matches the identifier, so a constant of that name holding
// any string at all is reported as a hardcoded secret. ui.Marker is the same
// kind of thing under the same name.
const Prefix = "[lydite:exclude_from_"

// Marker is how an author opens a declaration for one gate. Go, Rust and
// TypeScript all spell a line comment "//", so one form covers them.
func Marker(g Gate) string { return Prefix + string(g) + "]" }

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

// ErrNoReason reports a declaration whose reason is empty, missing, or never
// closed.
//
// It is an error rather than an ignored comment, because a declaration silently
// not honoured reads as the gate disregarding its author, who then has a finding
// they believe they have already answered. A reason is required for the cause
// the exemption set requires one: the declaration is the entire risk record for
// a finding nobody can clear, and a bare token is not reviewable.
type ErrNoReason struct {
	Path string
	Line int
	Gate Gate
}

func (e ErrNoReason) Error() string {
	return fmt.Sprintf("%s:%d: %s needs a reason in brackets after it", e.Path, e.Line, Marker(e.Gate))
}

// Declarations returns the reason declared for gate on each line, keyed by the
// line the declaration opens on.
//
// A declaration belongs to the line its token is written on, and never to a line
// its reason happens to wrap onto. How far one reaches from there is the
// caller's rule, because only the caller knows what a finding occupies — but
// every caller owes the same bound: a declaration must never reach a line an
// author was not writing about. One that reached further would let a declaration
// already in the tree acknowledge code a later change adds nearby, and that
// finding is excluded and never reported while the change itself adds no line
// holding the token, so nothing refers it and it merges unread. Kept to lines
// the author wrote beside the site, the bargain holds by construction: a mutant
// exists only on a changed line, so a declaration that acknowledges one sits on
// a changed line, which internal/referral sees as added.
//
// A reason runs to the first `]`, across as many immediately following comment
// lines as it takes. A `]` inside a reason ends it early, which is the cost of
// the simple rule and is why a reason is prose rather than a citation.
func Declarations(path string, gate Gate, comments []Comment) (map[int]string, error) {
	marker := Marker(gate)
	out := map[int]string{}
	for i := 0; i < len(comments); i++ {
		rest, ok := strings.CutPrefix(body(comments[i].Text), marker)
		if !ok {
			continue
		}
		// The token has to be followed by its reason's opening bracket, or a
		// mistyped `[lydite:exclude_from_crap]foo` reads as a declaration
		// whose reason is the rest of its own misspelling.
		rest, ok = strings.CutPrefix(rest, "[")
		if !ok {
			return nil, ErrNoReason{Path: path, Line: comments[i].Line, Gate: gate}
		}
		reason, consumed, ok := gather(rest, comments[i].Line, comments[i+1:])
		if !ok {
			return nil, ErrNoReason{Path: path, Line: comments[i].Line, Gate: gate}
		}
		out[comments[i].Line] = reason
		// Past the lines the reason wrapped onto, so a continuation is never
		// read as a declaration of its own.
		i += consumed
	}
	return out, nil
}

// gather reads a reason from the text after its opening bracket, continuing
// into the comment lines that follow until one closes it.
//
// Only lines immediately following, checked by line number rather than by
// position in the slice: a language's parser hands over every comment in the
// file, so the next entry may be pages away, and a reason that swallowed it
// would silence a declaration nobody wrote and drop the prose in between.
//
// It returns how many lines it used, and false for a reason that no line closes
// or that closes with nothing in it.
func gather(rest string, open int, next []Comment) (reason string, consumed int, ok bool) {
	// Bounded by the comments the reason could continue into, and written as a
	// range so the bound belongs to the loop rather than to a counter its body
	// advances. A reason nothing closes has to run out of lines, not out of
	// memory: the body appends once per turn, so a walk that could be made not
	// to advance would append until the machine it runs on has no memory left,
	// which no timeout can prevent — a deadline bounds how long a process
	// lives and not how much it allocates before it dies.
	parts := []string{rest}
	for used := range len(next) + 1 {
		last := len(parts) - 1
		if before, _, closed := strings.Cut(parts[last], "]"); closed {
			parts[last] = before
			reason = strings.TrimSpace(strings.Join(parts, " "))
			return reason, used, reason != ""
		}
		if used == len(next) || next[used].Line != open+used+1 {
			return "", used, false
		}
		parts = append(parts, body(next[used].Text))
	}
	return "", len(next), false
}

// body is a comment's text with its introducer and the space after it removed,
// so a declaration reads the same whether an author wrote `// [lydite:...` or
// `//[lydite:...`.
func body(text string) string {
	return strings.TrimLeft(strings.TrimPrefix(strings.TrimLeft(text, " \t"), "//"), " \t")
}
