package mutation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A document naming no tree cannot be bound to a checkout, so nothing can be
// recorded against it. Defaulted instead, it would land one tree's counts
// under another tree's key.
func TestCountsNamingNoTreeAreRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CountsFileName), []byte(`{"components":{"app":{"killed":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadCounts(dir)
	if err == nil {
		t.Fatal("a document naming no tree was accepted")
	}
	if !strings.Contains(err.Error(), "names no tree") {
		t.Errorf("the refusal says %q, want it to name what is missing", err)
	}
}

// Shards that mutated different trees are not parts of one run, and folding
// them would report a score no tree ever had — each number right and the total
// wrong.
func TestCountsOfDifferentTreesDoNotFold(t *testing.T) {
	_, err := FoldCounts([]CountsDocument{
		{Tree: "aaaaaaaaaaaa", Components: map[string]ComponentCounts{"a": {Killed: 1}}},
		{Tree: "bbbbbbbbbbbb", Components: map[string]ComponentCounts{"b": {Killed: 1}}},
	})
	if err == nil {
		t.Fatal("two trees folded into one run")
	}
	if !strings.Contains(err.Error(), "different trees") {
		t.Errorf("the refusal says %q, want it to name the disagreement", err)
	}
	// A fold over nothing is refused rather than answered with an empty
	// document: a caller cannot tell one from a run that mutated nothing.
	if _, err := FoldCounts(nil); err == nil {
		t.Error("a fold over no document was answered rather than refused")
	}
}

// The fold is the union across the shards, and a shard that ran nothing
// contributes nothing rather than zeros that would win it.
func TestTheFoldUnionsTheShardsCounts(t *testing.T) {
	folded, err := FoldCounts([]CountsDocument{
		{Tree: "abc", Components: map[string]ComponentCounts{"a": {Killed: 4, Survived: 1}}},
		{Tree: "abc"},
		{Tree: "abc", Components: map[string]ComponentCounts{"b": {Killed: 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded.Components) != 2 {
		t.Fatalf("the fold holds %+v, want one entry per component that ran", folded.Components)
	}
	if folded.Components["a"] != (ComponentCounts{Killed: 4, Survived: 1}) {
		t.Errorf("a folded to %+v", folded.Components["a"])
	}
	// Every declared component belongs to exactly one shard, so a second
	// answer means two jobs ran the same work: the first is kept and the fold
	// does not pretend to arbitrate.
	twice, err := FoldCounts([]CountsDocument{
		{Tree: "abc", Components: map[string]ComponentCounts{"a": {Killed: 4}}},
		{Tree: "abc", Components: map[string]ComponentCounts{"a": {Killed: 9}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if twice.Components["a"].Killed != 4 {
		t.Errorf("a folded to %+v, want the first answer", twice.Components["a"])
	}
}

// The stored counts and the tally every score is taken from carry the same six
// numbers. A field that reached one and not the other is a score computed over
// a mutant nothing counted.
func TestTheStoredCountsRoundTripThroughTheSummary(t *testing.T) {
	s := Summary{Killed: 1, TimedOut: 2, OutOfMemory: 3, Survived: 4, Unviable: 5, Acknowledged: 6}
	if got := CountsOf(s, 90*time.Second).Summary(); got != s {
		t.Errorf("round-tripped to %+v, want %+v", got, s)
	}
	data, err := json.Marshal(CountsOf(s, 90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"killed", "timed_out", "out_of_memory", "survived", "unviable", "acknowledged", "elapsed_seconds"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("the stored shape has no %q key: %s", key, data)
		}
	}
}

// The reused count is an optional field: a run that measured everything writes
// no key for it, a document without one reads as nought, and a shard's count
// survives the fold.
func TestTheReusedCountIsOptionalAndSurvivesTheFold(t *testing.T) {
	data, err := json.Marshal(ComponentCounts{Killed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "reused") {
		t.Errorf("a component that reused nothing wrote a reused key: %s", data)
	}
	var older ComponentCounts
	if err := json.Unmarshal([]byte(`{"killed":2}`), &older); err != nil || older.Reused != 0 {
		t.Errorf("a document with no reused key read as %+v, %v", older, err)
	}
	folded, err := FoldCounts([]CountsDocument{
		{Tree: "abc", Components: map[string]ComponentCounts{"a": {Killed: 4, Reused: 3}}},
		{Tree: "abc", Components: map[string]ComponentCounts{"b": {Killed: 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if folded.Components["a"].Reused != 3 || folded.Components["b"].Reused != 0 {
		t.Errorf("the fold holds %+v, want each shard's reused count kept", folded.Components)
	}
	out, err := json.Marshal(ComponentCounts{Killed: 1, Reused: 2})
	if err != nil || !strings.Contains(string(out), `"reused":2`) {
		t.Errorf("the stored shape has no reused key: %s, %v", out, err)
	}
}

// The elapsed time is data in the document, not a sentence a reader has to
// parse back: a run's own span reaches a fold and the ledger through this field
// and through nothing else. Sub-second precision survives it, because the span
// a budget is argued from is the one that was measured rather than the one the
// row rounded for a reader.
func TestTheElapsedTimeRoundTripsThroughTheStoredCounts(t *testing.T) {
	var decoded ComponentCounts
	data, err := json.Marshal(CountsOf(Summary{Killed: 2}, 1500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.Elapsed()
	if !ok {
		t.Fatalf("a recorded elapsed time read back as unrecorded: %s", data)
	}
	if got != 1500*time.Millisecond {
		t.Errorf("elapsed round-tripped to %s, want %s", got, 1500*time.Millisecond)
	}
}

// A document an older lydite wrote carries no elapsed time at all, and nought
// is how that arrives. It reads as unrecorded rather than as a component that
// took no time to mutate: a baseline suite and a mutant after it cannot happen
// instantly, so the two are one answer and neither is a measured zero.
func TestCountsWithNoElapsedTimeReadAsUnrecorded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, CountsFileName),
		[]byte(`{"tree":"abc","components":{"app":{"killed":4,"survived":0}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadCounts(dir)
	if err != nil {
		t.Fatalf("a document written before the elapsed time would not read: %v", err)
	}
	app := doc.Components["app"]
	if app.Killed != 4 {
		t.Errorf("the counts read back as %+v, want the four killed mutants", app)
	}
	if d, ok := app.Elapsed(); ok || d != 0 {
		t.Errorf("a document carrying no elapsed time reported %s, %v; want nought and unrecorded", d, ok)
	}
}
