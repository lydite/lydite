package mutation

import "sort"

// sites collects one file's mutants and enforces the two rules every generator
// owes, whatever syntax tree it walked.
//
// One collector rather than one per language, because both rules are about the
// bargain rather than about the grammar. The line bound is what keeps mutation
// proportional to the change and what makes the referral composition
// structural; the declaration matching is what decides which mutants an author
// has answered. Two implementations would agree until one of them learned
// something, and the mutant a drifted copy silently excluded is one nobody
// would ever see reported.
type sites struct {
	path string
	src  []byte
	// lines is the intersection of the change and what coverage reports as
	// executed, computed by the caller.
	lines map[int]bool
	// reasons is the equivalence declaration on each line, as the language's
	// own parser reported its comments.
	reasons map[int]string
	out     []Mutant
	// spans is the line range of each mutant in out, at the same index, so a
	// declaration can be matched against what each one actually replaces.
	spans []lineSpan
}

type lineSpan struct{ first, last int }

// add records one mutant, after establishing that it edits only requested
// lines and that the range it claims is inside the source.
//
// line and column are where the mutant is reported — the author's own
// coordinates, 1-indexed, so they can be printed at somebody looking at their
// own code. lo and hi are the byte range it replaces, and first and last are
// the lines that range spans.
//
// Every line the splice touches has to be one the caller asked for. A
// statement can span lines, and gating on its first alone deletes source
// outside the change.
func (s *sites) add(op Operator, line, column, lo, hi, first, last int, mutated string) {
	for l := first; l <= last; l++ {
		if !s.lines[l] {
			return
		}
	}
	if lo < 0 || hi > len(s.src) || lo > hi {
		return
	}
	s.out = append(s.out, Mutant{
		Path:     s.path,
		Line:     line,
		Column:   column,
		Operator: op,
		Offset:   lo,
		Length:   hi - lo,
		Original: string(s.src[lo:hi]),
		Mutated:  mutated,
	})
	s.spans = append(s.spans, lineSpan{first: first, last: last})
}

// resolve attaches each declaration to the mutants it is about, and reports
// the declarations that turned out to be about nothing.
//
// A declaration covers the mutants whose replaced range contains its line and
// whose range is the shortest of those. Position alone cannot tell what an
// author meant, because one line holds mutants at several scopes: beside
// `println(a < b)` sit two mutants of the comparison and one that deletes the
// whole call. The shortest range is the innermost thing written at that line,
// which is what somebody annotating a line is looking at — so a claim about an
// operator acknowledges the operator, and deleting the call remains a mutant
// they have not answered.
//
// Reaching by containment rather than by a window of lines is what lets a
// declaration inside a multi-line statement work: it sits on no line the
// statement opens or closes on, and it is still written inside it.
//
// The bound the referral bargain rests on is unchanged. Every line of a
// mutant's range is a line the caller asked for, so a declaration inside one is
// on a changed line, which internal/referral sees as added.
func (s *sites) resolve() []UnmatchedDeclaration {
	var unmatched []UnmatchedDeclaration
	for _, line := range sortedKeys(s.reasons) {
		shortest := -1
		for i, sp := range s.spans {
			if line < sp.first || line > sp.last {
				continue
			}
			if shortest < 0 || s.out[i].Length < shortest {
				shortest = s.out[i].Length
			}
		}
		if shortest < 0 {
			unmatched = append(unmatched, UnmatchedDeclaration{Path: s.path, Line: line, Reason: s.reasons[line]})
			continue
		}
		for i, sp := range s.spans {
			if line >= sp.first && line <= sp.last && s.out[i].Length == shortest {
				s.out[i].Reason = s.reasons[line]
			}
		}
	}
	return unmatched
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
