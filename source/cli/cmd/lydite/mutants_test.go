package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// readCountsFrom loads the counts document a run left under root.
func readCountsFrom(t *testing.T, root string) mutantsDoc {
	t.Helper()
	doc, err := readMutants(filepath.Join(root, runner.ReportDir))
	if err != nil {
		t.Fatalf("the run wrote no readable %s: %v", mutantsName, err)
	}
	return doc
}

// Presence in the document is the whole "did this component run" signal, so
// the two halves of it are asserted together: a component that ran and killed
// everything is present with a survived count of nought, and one that never ran
// is absent rather than present with the same zeros. Zeros standing in for an
// absence read, permanently, as a suite that killed every mutant — and the
// ledger they reach is append-only, so nothing later corrects them.
func TestAComponentThatRanIsInTheCountsAndOneThatDidNotIsAbsent(t *testing.T) {
	root := goModuleRepoWith(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: web\n    dir: web\n    runner: go-test\n    args: [\"./...\"]\n    mutation: false\n",
		"app",
		map[string]string{
			"web/go.mod": "module fixture/web\n\ngo 1.26.4\n",
			"web/web.go": "package web\n\nfunc Version() int { return 1 }\n",
		},
		deeper, killsItsMutants)

	doc, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main")
	if err != nil {
		t.Fatalf("a suite that kills every mutant failed the gate: %v\n%+v", err, doc.Rows)
	}
	counts := readCountsFrom(t, root)

	tree, err := gitstate.TreeSHA(t.Context(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Tree != tree {
		t.Errorf("the counts name tree %q, want the tree they were taken from, %q", counts.Tree, tree)
	}
	app, ok := counts.Components["app"]
	if !ok {
		t.Fatalf("the component that ran is absent from the counts: %+v", counts.Components)
	}
	if app.Killed == 0 {
		t.Errorf("the component that ran records no killed mutant: %+v", app)
	}
	if app.Survived != 0 {
		t.Errorf("a suite that killed every mutant records %d survivor(s)", app.Survived)
	}
	// What the component cost is measured once, by the run that paid it, and
	// stored beside what it bought. A fold reading it back out of the row's
	// prose would be one wording change from a silent nought.
	if _, ok := app.elapsed(); !ok {
		t.Errorf("the component that ran recorded no elapsed time: %+v", app)
	}
	if _, ok := counts.Components["web"]; ok {
		t.Error("a component declaring `mutation: false` is present in the counts, where its zeros read as a suite that killed everything")
	}
}

// A run responsible for part of the declaration writes the counts for its own
// part and claims nothing about the rest: a shard that wrote zeros for a
// component another shard mutated would win any fold it was part of.
func TestANarrowedRunWritesTheCountsOfWhatItRan(t *testing.T) {
	root := goModuleRepoWith(t,
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: web\n    dir: web\n    runner: go-test\n    args: [\"./...\"]\n",
		"app",
		map[string]string{
			"web/go.mod": "module fixture/web\n\ngo 1.26.4\n",
			"web/web.go": "package web\n\nfunc Version() int { return 1 }\n",
		},
		deeper, killsItsMutants)

	if _, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main", "--component", "app"); err != nil {
		t.Fatalf("a narrowed run failed: %v", err)
	}
	counts := readCountsFrom(t, root)
	if _, ok := counts.Components["app"]; !ok {
		t.Errorf("the component this run was responsible for is absent: %+v", counts.Components)
	}
	if _, ok := counts.Components["web"]; ok {
		t.Error("the run wrote counts for a component it was not responsible for")
	}
}

// The counts are data and the report is rendered prose, and the second is a
// document consumers already read: adding the first must leave every byte of
// it alone. The rows a run publishes are named here, so a count that leaked
// into the report is a row this does not expect.
func TestTheRenderedReportCarriesNothingOfTheCounts(t *testing.T) {
	root := goModuleRepo(t, deeper, killsItsMutants)
	if _, _, err := runMutationCmd(t, "--dir", root, "--base-branch", "main"); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, runner.ReportDir, documentName("mutation"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	var doc ui.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, r := range doc.Rows {
		labels = append(labels, r.Label)
	}
	want := []string{"schedule", mutationLabel("app"), "mutation"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("the report holds rows %v, want %v", labels, want)
	}
	for _, key := range []string{mutantsName, `"tree"`, `"acknowledged"`, `"out_of_memory"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("the rendered report carries %s, which belongs in %s alone", key, mutantsName)
		}
	}
}

// The counts share the report directory and the extension and are not a
// report. readDocuments refuses anything with no command, so a reader that did
// not skip this by name would take the whole pull-request comment down with it.
func TestTheCountsAreNotReadAsAReport(t *testing.T) {
	dir := t.TempDir()
	rep := ui.NewReport("mutation")
	rep.Add(ui.Row{Status: ui.StatusPass, Label: mutationLabel("app"), Value: "1 of 1 mutant(s) killed in 1s"})
	f, err := os.Create(filepath.Join(dir, documentName("mutation"))) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutantsName), []byte(`{"tree":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	docs, err := readDocuments(dir)
	if err != nil {
		t.Fatalf("a report directory holding %s could not be read: %v", mutantsName, err)
	}
	if len(docs) != 1 || docs[0].Command != "mutation" {
		t.Errorf("readDocuments returned %d document(s), want the mutation report alone", len(docs))
	}
}

// A document naming no tree cannot be bound to a checkout, so nothing can be
// recorded against it. Defaulted instead, it would land one tree's counts under
// another tree's key.
func TestCountsNamingNoTreeAreRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mutantsName), []byte(`{"components":{"app":{"killed":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := readMutants(dir)
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
	_, err := foldMutants([]mutantsDoc{
		{Tree: "aaaaaaaaaaaa", Components: map[string]mutantCounts{"a": {Killed: 1}}},
		{Tree: "bbbbbbbbbbbb", Components: map[string]mutantCounts{"b": {Killed: 1}}},
	})
	if err == nil {
		t.Fatal("two trees folded into one run")
	}
	if !strings.Contains(err.Error(), "different trees") {
		t.Errorf("the refusal says %q, want it to name the disagreement", err)
	}
	// A fold over nothing is refused rather than answered with an empty
	// document: a caller cannot tell one from a run that mutated nothing.
	if _, err := foldMutants(nil); err == nil {
		t.Error("a fold over no document was answered rather than refused")
	}
}

// The fold is the union across the shards, and a shard that ran nothing
// contributes nothing rather than zeros that would win it.
func TestTheFoldUnionsTheShardsCounts(t *testing.T) {
	folded, err := foldMutants([]mutantsDoc{
		{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 4, Survived: 1}}},
		{Tree: "abc"},
		{Tree: "abc", Components: map[string]mutantCounts{"b": {Killed: 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded.Components) != 2 {
		t.Fatalf("the fold holds %+v, want one entry per component that ran", folded.Components)
	}
	if folded.Components["a"] != (mutantCounts{Killed: 4, Survived: 1}) {
		t.Errorf("a folded to %+v", folded.Components["a"])
	}
	// Every declared component belongs to exactly one shard, so a second
	// answer means two jobs ran the same work: the first is kept and the fold
	// does not pretend to arbitrate.
	twice, err := foldMutants([]mutantsDoc{
		{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 4}}},
		{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 9}}},
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
	s := mutation.Summary{Killed: 1, TimedOut: 2, OutOfMemory: 3, Survived: 4, Unviable: 5, Acknowledged: 6}
	if got := countsOf(s, 90*time.Second).summary(); got != s {
		t.Errorf("round-tripped to %+v, want %+v", got, s)
	}
	data, err := json.Marshal(countsOf(s, 90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"killed", "timed_out", "out_of_memory", "survived", "unviable", "acknowledged", "elapsed_seconds"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("the stored shape has no %q key: %s", key, data)
		}
	}
}

// The elapsed time is data in the document, not a sentence a reader has to
// parse back: a run's own span reaches a fold and the ledger through this field
// and through nothing else. Sub-second precision survives it, because the span
// a budget is argued from is the one that was measured rather than the one the
// row rounded for a reader.
func TestTheElapsedTimeRoundTripsThroughTheStoredCounts(t *testing.T) {
	var decoded mutantCounts
	data, err := json.Marshal(countsOf(mutation.Summary{Killed: 2}, 1500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.elapsed()
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
	if err := os.WriteFile(filepath.Join(dir, mutantsName),
		[]byte(`{"tree":"abc","components":{"app":{"killed":4,"survived":0}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := readMutants(dir)
	if err != nil {
		t.Fatalf("a document written before the elapsed time would not read: %v", err)
	}
	app := doc.Components["app"]
	if app.Killed != 4 {
		t.Errorf("the counts read back as %+v, want the four killed mutants", app)
	}
	if d, ok := app.elapsed(); ok {
		t.Errorf("a document carrying no elapsed time reported %s", d)
	}
}
