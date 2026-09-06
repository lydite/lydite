// Package mutation asks whether a suite would notice if the code were wrong.
//
// Coverage measures execution. A test that calls a function and asserts
// nothing scores full marks on every line it touches, which is exactly the
// test something optimising for a green pipeline produces. A mutant is a
// single deliberate change to one line — a negated condition, a shifted
// boundary, a deleted statement — and the suite is asked whether anything
// fails. A change nothing notices is a survivor, and a survivor is the only
// outcome that fails the gate.
//
// Mutants are generated only from lines in the current change and only from
// lines coverage already reports as executed. The first bound makes the cost
// proportional to the diff rather than to the repository; the second removes
// mutants that cannot be killed by construction, and reporting one would only
// restate what patch coverage already said about the same line.
//
// Equivalence is undecidable, so nothing here tries to detect it. An author
// declares it with a "//lydite:equivalent <reason>" comment at the site, and
// the declared mutant is generated, counted and never run. The same token is
// one of internal/referral's suppression tokens, so a change carrying a fresh
// declaration is referred: that is what stops the annotation being a way
// around the gate rather than a way through it, and it is why the token is
// stated here and imported there rather than spelled twice.
// See docs/adr/0027-mutation-is-its-own-command.md.
package mutation

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Operator is one kind of deliberate change.
//
// The catalogue is fixed and has no declarable form. A catalogue a repository
// can empty is not a floor: emptying it passes mutation trivially while every
// run still reports green, which is the argument the built-in referral
// disqualifiers already win. It also keeps a mutation verdict meaning the same
// thing in two repositories, which is what owning the engine is for.
type Operator string

const (
	// ConditionalBoundary shifts a relational operator by one place
	// (`<` to `<=`), which is the off-by-one a test most often fails to
	// assert.
	ConditionalBoundary Operator = "conditional-boundary"
	// NegateConditional inverts a relational or equality test, so a branch
	// is taken exactly when it should not be.
	NegateConditional Operator = "negate-conditional"
	// ArithmeticOperator swaps one arithmetic operator for another.
	ArithmeticOperator Operator = "arithmetic-operator"
	// RemoveStatement deletes a statement outright.
	RemoveStatement Operator = "remove-statement"
	// ReplaceReturn substitutes a returned value.
	ReplaceReturn Operator = "replace-return"
)

// Operators is the whole catalogue, in a fixed order so a run's mutants are
// generated in the same sequence every time. Two runs over one tree that
// disagreed about the order would report the same outcomes against different
// mutant identities.
var Operators = []Operator{
	ConditionalBoundary,
	NegateConditional,
	ArithmeticOperator,
	RemoveStatement,
	ReplaceReturn,
}

// Mutant is one change at one site, held as the byte range it replaces rather
// than as a copy of the resulting file.
//
// A file yields many mutants and they differ from each other in a few bytes,
// so carrying a whole file per mutant multiplies one source by the number of
// sites in it and holds every copy for as long as the list lives. Apply builds
// the mutated file at the moment the isolation strategy needs it — Go hands
// that to `go build -overlay` and the other two write it into a worker
// directory — and one at a time is all either ever needs.
type Mutant struct {
	// Path is the mutated file, relative to the component directory, and is
	// checked to be so: it locates a file the executor writes, and a name
	// that becomes a path is a path.
	Path string
	// Line and Column locate the site in the original file. They are the
	// author's coordinates, so they can be printed at somebody looking at
	// their own code.
	Line, Column int
	// Operator is what was applied.
	Operator Operator
	// Offset and Length are the byte range of the original file this mutant
	// replaces. Length is zero for no operator in the catalogue, since every
	// one of them rewrites or deletes existing text.
	Offset, Length int
	// Original is the exact text in that range, and Mutated is what replaces
	// it. Apply checks the first against the source it is given, so a mutant
	// can never be spliced into a file it was not generated from.
	Original, Mutated string
	// Reason is the text of the //lydite:equivalent annotation covering this
	// site, and is empty for every mutant that is not acknowledged.
	Reason string
}

// Acknowledged reports whether an author has declared this mutant equivalent.
// An acknowledged mutant is never built and never run: the declaration is the
// answer, and running it would only reproduce the survival already claimed.
func (m Mutant) Acknowledged() bool { return m.Reason != "" }

// ErrStaleMutant reports that a mutant was applied to source it does not
// describe — the file changed between generation and execution, or a caller
// paired a mutant with the wrong file.
//
// It is an error rather than a best-effort splice because the alternative is
// silent: the bytes at that offset would be replaced anyway, producing a
// mutant nobody generated whose outcome is then reported against the operator
// named here.
type ErrStaleMutant struct {
	Path           string
	Offset, Length int
	Want, Got      string
}

func (e ErrStaleMutant) Error() string {
	return fmt.Sprintf("%s: bytes [%d,%d) are %q, not the %q this mutant replaces",
		e.Path, e.Offset, e.Offset+e.Length, e.Got, e.Want)
}

// Apply builds this mutant's source from the file it was generated from.
func (m Mutant) Apply(src []byte) ([]byte, error) {
	if m.Offset < 0 || m.Length < 0 || m.Offset+m.Length > len(src) {
		return nil, ErrStaleMutant{Path: m.Path, Offset: m.Offset, Length: m.Length, Want: m.Original, Got: ""}
	}
	if got := string(src[m.Offset : m.Offset+m.Length]); got != m.Original {
		return nil, ErrStaleMutant{Path: m.Path, Offset: m.Offset, Length: m.Length, Want: m.Original, Got: got}
	}
	out := make([]byte, 0, len(src)-m.Length+len(m.Mutated))
	out = append(out, src[:m.Offset]...)
	out = append(out, m.Mutated...)
	out = append(out, src[m.Offset+m.Length:]...)
	return out, nil
}

// displayLimit bounds how much replaced text a mutant prints. A removed
// statement can be a whole multi-line call, and a report row is one line.
const displayLimit = 48

// String locates a mutant the way a compiler locates an error, so a survivor
// in a log can be opened.
//
// The replaced text is quoted and truncated. It is source, so it holds
// whatever the source holds — including a newline, and text shaped like one of
// lydite's own status rows — and an unquoted copy of it could begin a line in
// a report that a reader would take for a verdict.
func (m Mutant) String() string {
	return fmt.Sprintf("%s:%d:%d: %s (%s -> %s)",
		m.Path, m.Line, m.Column, m.Operator, clip(m.Original), clip(m.Mutated))
}

func clip(s string) string {
	if len(s) > displayLimit {
		return fmt.Sprintf("%q...", s[:displayLimit])
	}
	return fmt.Sprintf("%q", s)
}

// Outcome is what running one mutant established.
type Outcome string

const (
	// Killed means the suite failed, which is the result being asked for.
	Killed Outcome = "killed"
	// TimedOut means the run exceeded its per-mutant budget, and counts as
	// killed. An infinite loop is a behaviour change something noticed; the
	// distinction is kept because a suite full of timeouts is worth seeing
	// even when every one of them scores correctly.
	TimedOut Outcome = "timed-out"
	// Survived means every test still passed. It is the only outcome that
	// fails the gate.
	Survived Outcome = "survived"
	// Unviable means the mutant did not compile. It is evidence about the
	// generator rather than about the tests, so it is excluded from the
	// denominator entirely and never counted as killed — telling it from a
	// kill is the whole reason a runner derives a build-only variant, since
	// both exit non-zero.
	Unviable Outcome = "unviable"
	// Acknowledged means an author declared the mutant equivalent, so it was
	// never run. Excluded from the denominator for the same reason Unviable
	// is: a mutant nothing can kill measures nothing about the suite.
	Acknowledged Outcome = "acknowledged"
)

// Result is one mutant and what became of it.
type Result struct {
	Mutant  Mutant
	Outcome Outcome
	// Detail is what the run said, for an outcome a reader would otherwise
	// have to reproduce to understand — the compiler error behind an
	// Unviable, the reason behind an Acknowledged.
	Detail string
}

// Summary counts one component's results.
type Summary struct {
	Killed       int
	TimedOut     int
	Survived     int
	Unviable     int
	Acknowledged int
}

// Add folds one result in.
func (s *Summary) Add(r Result) {
	switch r.Outcome {
	case Killed:
		s.Killed++
	case TimedOut:
		s.TimedOut++
	case Survived:
		s.Survived++
	case Unviable:
		s.Unviable++
	case Acknowledged:
		s.Acknowledged++
	}
}

// Total is every mutant generated, whatever became of it.
func (s Summary) Total() int {
	return s.Killed + s.TimedOut + s.Survived + s.Unviable + s.Acknowledged
}

// Denominator is the mutants that say something about the suite: the killed,
// the timed out and the survived. Unviable and acknowledged mutants are
// excluded, because neither is evidence about the tests — one could not be
// built and the other has been declared unkillable.
func (s Summary) Denominator() int { return s.Killed + s.TimedOut + s.Survived }

// Score is the killed fraction of the denominator, and is meaningful only
// alongside it: 1 of 1 and 400 of 400 are the same number and not the same
// evidence. It is reported rather than gated on — the gate is Survived == 0,
// a boolean, which is what makes it survive differing operator sets.
func (s Summary) Score() (killed, total int) { return s.Killed + s.TimedOut, s.Denominator() }

// Passed reports whether this component clears the gate. An acknowledged
// mutant does not fail it; the referral its annotation triggers is what puts a
// human on the claim.
func (s Summary) Passed() bool { return s.Survived == 0 }

// Survivors returns the results that failed the gate, in file and line order,
// so a report lists them the way an author reads their own change.
func Survivors(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Outcome == Survived {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Mutant, out[j].Mutant
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}

// AnnotationToken is how an author declares a mutant equivalent. All three
// languages spell a line comment "//", so one form covers them.
//
// internal/referral holds this same constant in its suppression set, which is
// what refers a change that adds one.
const AnnotationToken = "//lydite:equivalent"

// ErrNoReason reports an annotation with nothing after it.
//
// It is an error rather than an ignored annotation, because an annotation
// silently not honoured reads as the engine disregarding its author, who then
// has a survivor they believe they have already answered. A reason is required
// for the same cause the exemption set requires one: the annotation is the
// entire risk record for a mutant nobody can kill, and a bare token is not
// reviewable.
type ErrNoReason struct {
	Path string
	Line int
}

func (e ErrNoReason) Error() string {
	return fmt.Sprintf("%s:%d: %s needs a reason after it", e.Path, e.Line, AnnotationToken)
}

// ErrPathEscapes reports a source path that is not inside the component.
type ErrPathEscapes struct{ Path string }

func (e ErrPathEscapes) Error() string {
	return fmt.Sprintf("%s: source path is absolute or escapes the component directory", e.Path)
}

// checkPath refuses a path that does not name a file inside the component.
//
// A mutant's path is joined to a worker directory and written there, so a
// path that is absolute or climbs out of the tree writes outside the directory
// the executor owns. The invariant is established where the value is produced,
// so no consumer has to remember to re-check it — the reason internal/download
// keeps one path-traversal guard rather than a copy per caller.
func checkPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return ErrPathEscapes{Path: p}
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return ErrPathEscapes{Path: p}
	}
	return nil
}

// Annotations reads the equivalence declarations out of one file's source,
// returning the reason by the line it covers.
//
// A declaration on a line of its own covers that line and the one below it, so
// it can be written above the statement it is about. A trailing declaration
// covers only the line it sits on: an author writing one has made a claim
// about the code to its left and none at all about the next statement, and
// carrying it down would acknowledge mutants nobody declared — silently, since
// an acknowledged mutant is excluded from the denominator and never run.
//
// The scan tracks string, character and raw-string literals and skips a token
// inside one, so source that merely quotes the annotation does not acknowledge
// anything. It is textual rather than taken off a syntax tree because one
// implementation serves three languages, and Go, Rust and TypeScript agree on
// "//" and on how a string is escaped. Its limit is a language whose literals
// do not: a token inside a TypeScript template literal is read as a comment.
func Annotations(path string, src []byte) (map[int]string, error) {
	out := map[int]string{}
	line, lineStart := 1, 0

	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '\n':
			line++
			lineStart = i + 1
		case '"', '\'':
			i = skipQuoted(src, i, src[i])
		case '`':
			for j := i + 1; j < len(src); j++ {
				if src[j] == '\n' {
					line++
					lineStart = j + 1
				}
				if src[j] == '`' {
					i = j
					break
				}
				i = j
			}
		case '/':
			if i+1 >= len(src) || src[i+1] != '/' {
				continue
			}
			end := lineEnd(src, i)
			if !bytes.HasPrefix(src[i:end], []byte(AnnotationToken)) {
				i = end - 1
				continue
			}
			reason := strings.TrimSpace(string(src[i+len(AnnotationToken) : end]))
			if reason == "" {
				return nil, ErrNoReason{Path: path, Line: line}
			}
			out[line] = reason
			if strings.TrimSpace(string(src[lineStart:i])) == "" {
				out[line+1] = reason
			}
			i = end - 1
		}
	}
	// A declaration on its own line hands its reason down, but a line with a
	// declaration of its own keeps it: the nearer claim is the one its author
	// wrote about that code.
	return out, nil
}

// skipQuoted returns the index of the closing quote, or the last byte before
// the line ends — neither Go, Rust nor TypeScript lets an ordinary string span
// a newline, so an unterminated one is a broken file rather than a literal
// that swallows the rest of the source.
func skipQuoted(src []byte, start int, quote byte) int {
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '\n':
			return i - 1
		case quote:
			return i
		}
	}
	return len(src) - 1
}

// lineEnd returns the index one past the last byte of the line holding i.
func lineEnd(src []byte, i int) int {
	if n := bytes.IndexByte(src[i:], '\n'); n >= 0 {
		return i + n
	}
	return len(src)
}
