package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/crap"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// over is a component whose CRAP report names `n` functions above the
// threshold, each in its own file so the fingerprints differ on something a
// reader can see.
func over(name string, n int) measurement {
	m := measured(name, runner.Go, 9, 10)
	m.CRAP = crap.Report{Scored: 40, Worst: 200}
	for i := range n {
		m.CRAP.Over = append(m.CRAP.Over, crap.Function{
			Name: "F" + string(rune('A'+i)), File: name + "/lib.go", Line: 10 * (i + 1),
			Complexity: 12, Value: float64(200 - i),
			Lines: coverage.LineCount{Covered: 0, Total: 20},
		})
	}
	return m
}

// A gate emits findings exactly where it makes a claim the author must clear.
// The functions already above the threshold on a passing row are debt this
// change did not add, and a claim on each of them would fire on ordinary work.
func TestOnlyAFailingCRAPRowMakesAClaim(t *testing.T) {
	t.Parallel()
	m := over("api", 3)

	if _, findings := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 2}}, true); len(findings) != 3 {
		t.Errorf("a failing row made %d claims, want one per function over the threshold", len(findings))
	}
	if _, findings := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 3}}, true); len(findings) != 0 {
		t.Errorf("standing debt made %d claims, want none", len(findings))
	}
	if _, findings := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 2}}, false); len(findings) != 0 {
		t.Errorf("an ungated run made %d claims, want none", len(findings))
	}
}

// The prose a human reads and the data a consumer anchors are one derivation.
// Two would be free to disagree, and the one nobody looks at is the one that
// would drift.
func TestAFailingRowsDetailIsRenderedFromItsFindings(t *testing.T) {
	t.Parallel()
	row, findings := crapRow(over("api", 2), gitstate.CRAPBaseline{"api": {Above: 0}}, true)

	if row.Status != ui.StatusFail {
		t.Fatalf("row status is %q, want fail", row.Status)
	}
	detail := strings.Join(row.Detail, "\n")
	for _, f := range findings {
		if !strings.Contains(detail, f.Message) {
			t.Errorf("the row's detail does not carry the claim %q:\n%s", f.Message, detail)
		}
		if !strings.Contains(detail, f.Path) {
			t.Errorf("the row's detail does not name %q:\n%s", f.Path, detail)
		}
	}
}

// A CRAP claim is identified by the function's own name, so the function
// moving down its file is the same claim and a different function is not.
func TestACRAPClaimIsIdentifiedByItsFunction(t *testing.T) {
	t.Parallel()
	_, first := crapRow(over("api", 2), gitstate.CRAPBaseline{"api": {Above: 0}}, true)

	moved := over("api", 2)
	for i := range moved.CRAP.Over {
		moved.CRAP.Over[i].Line += 500
	}
	_, second := crapRow(moved, gitstate.CRAPBaseline{"api": {Above: 0}}, true)

	if first[0].Fingerprint() != second[0].Fingerprint() {
		t.Errorf("a function that moved was re-identified: %s then %s",
			first[0].Fingerprint(), second[0].Fingerprint())
	}
	if first[0].Fingerprint() == first[1].Fingerprint() {
		t.Errorf("two functions share the fingerprint %s", first[0].Fingerprint())
	}
}

func survivorAt(pathInComponent string, line int, original, mutated string) mutation.Result {
	return mutation.Result{
		Outcome: mutation.Survived,
		Mutant: mutation.Mutant{
			Path: pathInComponent, Line: line, Column: 3,
			Operator: mutation.ConditionalBoundary, Original: original, Mutated: mutated,
		},
	}
}

// A mutant's path is relative to the component it came from, and every other
// producer names a file from the scan root. One file named from two roots is
// two claims, and only one of them can be anchored.
func TestAMutationClaimNamesItsFileFromTheScanRoot(t *testing.T) {
	t.Parallel()
	_, findings := mutationRow(mutationLabel("api"), "api", "services/api", testLog(t),
		mutation.Summary{Killed: 1, Survived: 1},
		[]mutation.Result{survivorAt("handler.go", 42, "<", ">=")}, nil, time.Second)

	if len(findings) != 1 {
		t.Fatalf("%d claims, want 1", len(findings))
	}
	if got := findings[0].Path; got != "services/api/handler.go" {
		t.Errorf("path is %q, want it rebased onto the scan root", got)
	}
	if findings[0].Component != "api" || findings[0].Gate != "mutation" {
		t.Errorf("claim is not attributed: %+v", findings[0])
	}
}

// Two identical comparisons in one file produce two mutants alike in
// everything a fingerprint reads, and only source order separates them.
// Without that they are one claim reported once.
func TestTwoIdenticalMutantsInOneFileAreTwoClaims(t *testing.T) {
	t.Parallel()
	_, findings := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 0, Survived: 2},
		[]mutation.Result{
			survivorAt("a.go", 10, "<", ">="),
			survivorAt("a.go", 40, "<", ">="),
		}, nil, time.Second)

	if len(findings) != 2 {
		t.Fatalf("%d claims, want 2", len(findings))
	}
	if findings[0].Fingerprint() == findings[1].Fingerprint() {
		t.Errorf("two survivors share the fingerprint %s", findings[0].Fingerprint())
	}
}

// A killed mutant is evidence the suite works, not a claim about the code.
func TestOnlySurvivorsBecomeClaims(t *testing.T) {
	t.Parallel()
	_, findings := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 3}, nil, nil, time.Second)

	if len(findings) != 0 {
		t.Errorf("a component that killed everything made %d claims: %+v", len(findings), findings)
	}
}

// A patch claim is a stretch of untested new code, identified by its own text
// so that inserting code above it does not report it as something new.
func TestAPatchClaimIsIdentifiedByTheCodeItIsAbout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSource := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := measured("api", runner.Go, 1, 4)
	m.Hits = coverage.LineHits{"a.go": {3: 0, 4: 0}}

	writeSource("package a\n\nfirst()\nsecond()\n")
	before := patchFindings(dir, m, map[string][]int{"a.go": {3, 4}})
	if len(before) != 1 {
		t.Fatalf("%d claims, want one stretch", len(before))
	}
	if before[0].Line != 3 || before[0].EndLine != 4 {
		t.Errorf("the stretch is %d-%d, want 3-4", before[0].Line, before[0].EndLine)
	}

	// The same two untested lines, pushed down by an import above them.
	writeSource("package a\n\nimport \"fmt\"\n\nfirst()\nsecond()\n")
	m.Hits = coverage.LineHits{"a.go": {5: 0, 6: 0}}
	after := patchFindings(dir, m, map[string][]int{"a.go": {5, 6}})
	if len(after) != 1 {
		t.Fatalf("%d claims after the insertion, want one", len(after))
	}
	if before[0].Fingerprint() != after[0].Fingerprint() {
		t.Errorf("an insertion above the stretch re-identified it: %s then %s",
			before[0].Fingerprint(), after[0].Fingerprint())
	}
}

// A stretch whose source cannot be read keeps its claim. The row has already
// gated on it, so dropping the claim to protect its identity would hide a
// failure lydite found; what it loses is only what tells it from a neighbour,
// and the ordinal supplies that.
func TestAStretchWhoseCodeCannotBeReadStillMakesAClaim(t *testing.T) {
	t.Parallel()
	m := measured("api", runner.Go, 1, 4)
	m.Hits = coverage.LineHits{"gone.go": {3: 0}}

	findings := patchFindings(t.TempDir(), m, map[string][]int{"gone.go": {3}})
	if len(findings) != 1 {
		t.Fatalf("%d claims, want 1 — an unreadable file must not lose the claim", len(findings))
	}
	if findings[0].Line != 3 {
		t.Errorf("the claim lost its location: %+v", findings[0])
	}
}

// Every producer must number its own claims, or two identical sites in one
// file collapse into one fingerprint.
func TestEveryProducerNumbersItsClaims(t *testing.T) {
	t.Parallel()
	m := measured("api", runner.Go, 1, 4)
	m.CRAP = crap.Report{Scored: 2, Worst: 90, Over: []crap.Function{
		{Name: "F", File: "a.go", Line: 1, Complexity: 12, Lines: coverage.LineCount{Total: 20}},
		{Name: "F", File: "a.go", Line: 90, Complexity: 12, Lines: coverage.LineCount{Total: 20}},
	}}
	_, findings := crapRow(m, gitstate.CRAPBaseline{"api": {Above: 0}}, true)

	if len(findings) != 2 {
		t.Fatalf("%d claims, want 2", len(findings))
	}
	if findings[0].Fingerprint() == findings[1].Fingerprint() {
		t.Errorf("two claims on one site share the fingerprint %s", findings[0].Fingerprint())
	}
	if got := []int{findings[0].Ordinal, findings[1].Ordinal}; got[0] != 0 || got[1] != 1 {
		t.Errorf("ordinals are %v, want [0 1]", got)
	}
}

// A finding travels beside the rows, so what a run found is readable by
// something that never saw its terminal output.
func TestFindingsReachTheDocument(t *testing.T) {
	t.Parallel()
	rep := ui.NewReport("mutation")
	_, findings := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 0, Survived: 1},
		[]mutation.Result{survivorAt("a.go", 10, "<", ">=")}, nil, time.Second)
	rep.AddFindings(findings...)

	if got := rep.Findings(); len(got) != 1 || got[0].Gate != "mutation" {
		t.Errorf("the report carries %+v, want one mutation claim", got)
	}
}

// A shard matrix must not lose what its shards found: the fold is the only
// document anything downstream reads, and a claim missing from it is one that
// silently stopped being made the day the repository grew a second shard.
func TestTheFoldCarriesEveryShardsClaims(t *testing.T) {
	t.Parallel()
	dirs := make([]string, 2)
	for i, name := range []string{"api", "web"} {
		root := t.TempDir()
		dirs[i] = reportsDir(root)
		shard := ui.NewReport("test")
		shard.Add(ui.Row{Status: ui.StatusFail, Label: "patch(" + name + ")", Value: "failed"})
		shard.AddFindings(finding.Finding{
			Gate: "patch", Component: name, Path: name + "/a.go", Line: 3,
			Message: "untested", Site: "first()",
		})
		saveDocument(root, shard)
	}

	rep := ui.NewReport("test")
	readShards(rep, dirs, "test", nil)

	if got := rep.Findings(); len(got) != 2 {
		t.Fatalf("the fold carries %d claims, want one from each shard: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, f := range rep.Findings() {
		seen[f.Component] = true
	}
	if !seen["api"] || !seen["web"] {
		t.Errorf("the fold lost a shard's claims: %v", seen)
	}
}

// A claim defaults to unanchorable and a producer that can prove otherwise
// says so. That direction is what keeps a claim in the standing comment rather
// than offered to a platform that would refuse it.
func TestAClaimIsAnchoredOnlyAgainstLinesTheChangeTouched(t *testing.T) {
	t.Parallel()
	survivors := []mutation.Result{survivorAt("a.go", 10, "<", ">=")}

	_, unknown := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 0, Survived: 1}, survivors, nil, time.Second)
	if unknown[0].Anchor != finding.AnchorNowhere {
		t.Errorf("a run that knew of no change anchored %q, want nowhere", unknown[0].Anchor)
	}

	_, known := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 0, Survived: 1}, survivors,
		map[string][]int{"api/a.go": {10}}, time.Second)
	if known[0].Anchor != finding.AnchorLine {
		t.Errorf("a survivor on a changed line anchored %q, want line", known[0].Anchor)
	}

	_, elsewhere := mutationRow(mutationLabel("api"), "api", "api", testLog(t),
		mutation.Summary{Killed: 0, Survived: 1}, survivors,
		map[string][]int{"api/a.go": {900}}, time.Second)
	if elsewhere[0].Anchor != finding.AnchorFile {
		t.Errorf("a survivor in a changed file anchored %q, want file", elsewhere[0].Anchor)
	}
}

// Every stretch is made of changed lines by construction, and the rule saying
// so is asked rather than assumed so a producer cannot quietly stop obeying it.
func TestAPatchClaimIsAnchoredToItsLines(t *testing.T) {
	t.Parallel()
	m := measured("api", runner.Go, 1, 4)
	m.Hits = coverage.LineHits{"a.go": {3: 0, 4: 0}}

	got := patchFindings(t.TempDir(), m, map[string][]int{"a.go": {3, 4}})
	if len(got) != 1 || got[0].Anchor != finding.AnchorLine {
		t.Errorf("a stretch of changed lines anchored %q, want line", got[0].Anchor)
	}
}
