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
