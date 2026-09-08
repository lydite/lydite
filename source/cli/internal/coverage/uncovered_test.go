package coverage

import (
	"reflect"
	"testing"
)

// A patch-coverage claim is about a stretch of untested new code, because that
// is one thing to do. Per line it would be one claim per line of a large
// addition, which is a surface nobody reads.
func TestAdjacentUntestedLinesAreOneRun(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 11, 12}},
		LineHits{"a.go": {10: 0, 11: 0, 12: 0}},
	)
	want := []Run{{File: "a.go", First: 10, Last: 12, Lines: 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestCoverResumingEndsARun(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 11, 12, 13}},
		LineHits{"a.go": {10: 0, 11: 3, 12: 0, 13: 0}},
	)
	want := []Run{
		{File: "a.go", First: 10, Last: 10, Lines: 1},
		{File: "a.go", First: 12, Last: 13, Lines: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// Unchanged code between two untested stretches makes them two stretches: the
// change stopped and resumed elsewhere, so it left the first place behind.
func TestUnchangedCodeBetweenTwoStretchesSeparatesThem(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 11, 40, 41}},
		LineHits{"a.go": {10: 0, 11: 0, 40: 0, 41: 0}},
	)
	want := []Run{
		{File: "a.go", First: 10, Last: 11, Lines: 2},
		{File: "a.go", First: 40, Last: 41, Lines: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A comment written inside an untested block counts towards neither side of
// PatchPercent, and splitting the block on it would report two claims where an
// author sees one.
func TestALineTheReportKnowsNothingAboutDoesNotSplitARun(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 11, 12}},
		LineHits{"a.go": {10: 0, 12: 0}}, // 11 is a comment: no entry at all
	)
	want := []Run{{File: "a.go", First: 10, Last: 12, Lines: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFullyCoveredChangedLinesProduceNothing(t *testing.T) {
	if got := Uncovered(
		map[string][]int{"a.go": {10, 11}},
		LineHits{"a.go": {10: 1, 11: 2}},
	); len(got) != 0 {
		t.Errorf("a tested change produced %+v", got)
	}
}

// A file the report speaks for nothing of is unmeasured, not uncovered.
// Claiming its changed lines are untested would report a component nobody
// measured as one that failed.
func TestAFileWithNoReportProducesNothing(t *testing.T) {
	if got := Uncovered(
		map[string][]int{"a.go": {10, 11}},
		LineHits{"b.go": {10: 0}},
	); len(got) != 0 {
		t.Errorf("a file with no report produced %+v", got)
	}
}

// Two runs of the same change must produce one answer, or a fingerprint
// derived from a run would move between runs.
func TestRunsAreOrderedByFileAndLine(t *testing.T) {
	got := Uncovered(
		map[string][]int{
			"z.go": {5},
			"a.go": {40, 10},
		},
		LineHits{"z.go": {5: 0}, "a.go": {10: 0, 40: 0}},
	)
	want := []Run{
		{File: "a.go", First: 10, Last: 10, Lines: 1},
		{File: "a.go", First: 40, Last: 40, Lines: 1},
		{File: "z.go", First: 5, Last: 5, Lines: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A line named twice is one line. Left in, the second copy reads as a gap
// against the first — the run flushes and reopens — and one stretch is
// reported as two overlapping claims.
func TestALineNamedTwiceIsOneLine(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 10, 11}},
		LineHits{"a.go": {10: 0, 11: 0}},
	)
	want := []Run{{File: "a.go", First: 10, Last: 11, Lines: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// A run's length is not its span. A comment written inside an untested block
// lies between the two ends and counts towards neither side of PatchPercent,
// so a claim measured by the span would say more lines are untested than the
// gate ever counted.
func TestARunCountsWhatTheReportSpeaksFor(t *testing.T) {
	got := Uncovered(
		map[string][]int{"a.go": {10, 11, 12, 13}},
		LineHits{"a.go": {10: 0, 13: 0}}, // 11 and 12 are comments
	)
	want := []Run{{File: "a.go", First: 10, Last: 13, Lines: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v — the span is 4 and only 2 lines are untested", got, want)
	}
}
