package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/gitstate"
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
	if _, ok := app.Elapsed(); !ok {
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

// writeMutants shares the reports directory with saveDocument and every other
// writer under it, so it must leave the same `.gitignore` behind: a report
// directory only writeMutants ever populates (a shard scoped to no other
// document) would otherwise reach git with nothing keeping it out.
func TestWriteMutantsIgnoresTheDirectoryItCreates(t *testing.T) {
	root := t.TempDir()
	doc := mutantsDoc{Tree: strings.Repeat("a", 12), Components: map[string]mutantCounts{"app": {Killed: 1}}}
	if err := writeMutants(root, doc); err != nil {
		t.Fatalf("writeMutants failed: %v", err)
	}

	gitignore, err := os.ReadFile(filepath.Join(reportsDir(root), ".gitignore")) // #nosec G304 -- a temp directory this test owns
	if err != nil {
		t.Fatalf("writeMutants left no %s: %v", ".gitignore", err)
	}
	if string(gitignore) != "*\n" {
		t.Errorf("the reports directory's .gitignore is %q, want %q", gitignore, "*\n")
	}

	written := readCountsFrom(t, root)
	if written.Tree != doc.Tree {
		t.Errorf("writeMutants wrote tree %q, want %q", written.Tree, doc.Tree)
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
