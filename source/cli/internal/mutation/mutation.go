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
// the declared mutant is generated, counted and never run. That it is also a
// suppression, and therefore refers the change through internal/referral, is
// what stops the annotation being a way around the gate rather than a way
// through it. See docs/adr/0027-mutation-is-its-own-command.md.
package mutation

import (
	"fmt"
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

// Mutant is one change at one site, carrying the whole mutated file rather
// than a patch.
//
// The source is carried because that is what the isolation strategies consume:
// Go hands it to `go build -overlay`, which compiles it without the tree ever
// being written, and the other two write it into a worker directory. A patch
// would have to be applied by something, and the thing that applied it would
// be a second place a mutant could be got wrong.
type Mutant struct {
	// Path is the mutated file, relative to the component directory.
	Path string
	// Line and Column locate the site in the original file. They are the
	// author's coordinates, not the mutated file's, because they exist to
	// be printed at somebody who is looking at their own code.
	Line, Column int
	// Operator is what was applied.
	Operator Operator
	// Original and Mutated are the exact text replaced and its replacement,
	// so a report can say what was done without a diff.
	Original, Mutated string
	// Source is the complete mutated file.
	Source []byte
	// Reason is the text of the //lydite:equivalent annotation covering this
	// site, and is empty for every mutant that is not acknowledged.
	Reason string
}

// Acknowledged reports whether an author has declared this mutant equivalent.
// An acknowledged mutant is never built and never run: the declaration is the
// answer, and running it would only reproduce the survival already claimed.
func (m Mutant) Acknowledged() bool { return m.Reason != "" }

// String locates a mutant the way a compiler locates an error, so a survivor
// in a log can be opened.
func (m Mutant) String() string {
	return fmt.Sprintf("%s:%d:%d: %s (%s -> %s)", m.Path, m.Line, m.Column, m.Operator, m.Original, m.Mutated)
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

// Annotations reads the equivalence declarations out of one file's source,
// returning the reason by the line it covers.
//
// An annotation covers the line it sits on and the line it precedes, so both
// a trailing comment and one written above the statement work. There is no
// file-level or function-level form: `mutation: false` in the declaration is
// already the coarse control and it lives where its history is the review
// record, whereas a broad in-code escape would be a second coarse control in
// a place with no such record.
//
// The scan is textual and deliberately so. It runs before parsing, so a file
// that does not parse still reports its annotations, and it is one
// implementation for three languages rather than three that agree until one
// learns a comment form the others have not.
func Annotations(path string, src []byte) (map[int]string, error) {
	out := map[int]string{}
	for i, line := range strings.Split(string(src), "\n") {
		idx := strings.Index(line, AnnotationToken)
		if idx < 0 {
			continue
		}
		reason := strings.TrimSpace(line[idx+len(AnnotationToken):])
		if reason == "" {
			return nil, ErrNoReason{Path: path, Line: i + 1}
		}
		// One-indexed: the line the comment is on, and the next one, so a
		// declaration written above its statement covers it.
		out[i+1] = reason
		if _, taken := out[i+2]; !taken {
			out[i+2] = reason
		}
	}
	return out, nil
}
