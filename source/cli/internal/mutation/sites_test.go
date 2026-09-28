package mutation

import "testing"

// add is where the byte range a mutant claims is held against the source it
// came from, and each of those bounds is a boundary nothing else in the
// package reaches: a generator walks a syntax tree, so it offers a range the
// file holds or it has a defect. The guard is what stops one that does not
// from being spliced outside the source, so a boundary nothing asserts is a
// guard nobody has checked.
func TestTheRangeAMutantClaimsIsHeldAgainstTheSource(t *testing.T) {
	src := []byte("package p\n")
	for _, tc := range []struct {
		name     string
		lo, hi   int
		recorded bool
	}{
		{"a range starting at the first byte", 0, 1, true},
		{"a range ending at the last byte", len(src) - 1, len(src), true},
		{"the whole file", 0, len(src), true},
		{"a range starting before the file", -1, 1, false},
		{"a range ending past the file", 0, len(src) + 1, false},
		{"a range replacing nothing", 1, 1, false},
		{"a range ending before it starts", 2, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sites{path: "x.go", src: src, lines: map[int]bool{1: true}}
			s.add(NegateConditional, 1, 1, tc.lo, tc.hi, 1, 1, "q")
			if recorded := len(s.out) == 1; recorded != tc.recorded {
				t.Fatalf("add recorded %d mutant(s), want recorded = %v", len(s.out), tc.recorded)
			}
			if len(s.out) != len(s.spans) {
				t.Errorf("%d mutant(s) and %d span(s): a declaration can only be matched against a mutant that has one",
					len(s.out), len(s.spans))
			}
		})
	}
}

// A declaration covers the innermost mutants on its line, and innermost is a
// matter of which range encloses which. Width is not: an operator two bytes
// wide is as innermost as its one-byte neighbour, and one declaration beside
// both is a claim about both.
func TestADeclarationCoversEveryMutantNoOtherNestsInside(t *testing.T) {
	// Byte offsets into "f(a <= b + 1)":
	// the call is [0,13), `<=` is [4,6), `+` is [9,10), `b + 1` is [7,12).
	src := []byte("f(a <= b + 1)\n")
	type span struct {
		op     Operator
		lo, hi int
	}
	for _, tc := range []struct {
		name    string
		mutants []span
		// declared is the index of every mutant the declaration must cover;
		// every other one must be left unanswered.
		declared []int
	}{
		{
			name:     "a boundary shift and a negation of one operator",
			mutants:  []span{{ConditionalBoundary, 4, 6}, {NegateConditional, 4, 6}},
			declared: []int{0, 1},
		},
		{
			name:     "two operators of different widths, neither enclosing the other",
			mutants:  []span{{ConditionalBoundary, 4, 6}, {ArithmeticOperator, 9, 10}},
			declared: []int{0, 1},
		},
		{
			name:     "a call enclosing the operator inside it",
			mutants:  []span{{RemoveStatement, 0, 13}, {ConditionalBoundary, 4, 6}, {NegateConditional, 4, 6}},
			declared: []int{1, 2},
		},
		{
			name:     "a range enclosing one operator and beside another",
			mutants:  []span{{ReplaceReturn, 7, 12}, {ArithmeticOperator, 9, 10}, {ConditionalBoundary, 4, 6}},
			declared: []int{1, 2},
		},
		{
			name:     "a range sharing one end with the range it encloses",
			mutants:  []span{{RemoveStatement, 0, 13}, {ReplaceReturn, 0, 6}},
			declared: []int{1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sites{path: "x.ts", src: src, lines: map[int]bool{1: true}, reasons: map[int]string{1: "claimed"}}
			for _, m := range tc.mutants {
				s.add(m.op, 1, m.lo+1, m.lo, m.hi, 1, 1, "q")
			}
			if len(s.out) != len(tc.mutants) {
				t.Fatalf("recorded %d of %d mutants", len(s.out), len(tc.mutants))
			}
			if unmatched := s.resolve(); len(unmatched) != 0 {
				t.Errorf("unmatched = %v, want none", unmatched)
			}
			want := map[int]bool{}
			for _, i := range tc.declared {
				want[i] = true
			}
			for i, m := range s.out {
				if got := m.Acknowledged(); got != want[i] {
					t.Errorf("%s over [%d,%d): acknowledged = %v, want %v",
						m.Operator, m.Offset, m.Offset+m.Length, got, want[i])
				}
			}
		})
	}
}

// A declaration binds to the mutants whose own lines hold it. One written on
// the line above the code it is about covers nothing there, and is reported as
// covering nothing rather than reaching down.
func TestADeclarationOnTheLineAboveCoversNothing(t *testing.T) {
	src := []byte("// claim\nf(a <= b)\n")
	s := &sites{path: "x.ts", src: src, lines: map[int]bool{1: true, 2: true}, reasons: map[int]string{1: "claimed"}}
	s.add(ConditionalBoundary, 2, 5, 13, 15, 2, 2, "<")
	s.add(NegateConditional, 2, 5, 13, 15, 2, 2, ">")
	unmatched := s.resolve()
	if len(unmatched) != 1 || unmatched[0].Line != 1 {
		t.Errorf("unmatched = %v, want the declaration on line 1", unmatched)
	}
	for _, m := range s.out {
		if m.Acknowledged() {
			t.Errorf("%s: acknowledged by a declaration on the line above it", m)
		}
	}
}
