package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/ui"
)

// mutationProjection412 and mutationProjection9 are projection lines as a run
// writes them into a component's log: 412 mutants on a 30s baseline and 9 on a
// 20s one, each over four workers. The spelling is mutationstages', and the
// fold only quotes a line it reads back through that spelling.
const (
	mutationProjection412 = "412 mutant(s), budget 1m30s each, 4 worker(s): at most 2h34m30s"
	mutationProjection9   = "9 mutant(s), budget 1m0s each, 4 worker(s): at most 3m0s"
)

// mutationShardDir writes one shard's report directory at dir: the document it
// rendered, or nothing at all for a shard whose job died before it wrote one.
func mutationShardDir(t *testing.T, dir string, rows []ui.Row) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if rows == nil {
		return dir
	}
	rep := ui.NewReport("mutation")
	for _, r := range rows {
		rep.Add(r)
	}
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
	return dir
}

// writeComponentLog writes a component's mutation log where a run writes it:
// under a directory named for the component, inside the report directory.
func writeComponentLog(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o750); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, name, mutationLogName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// mutationRowOf is the row a shard writes for a component it mutated.
func mutationRowOf(name string) ui.Row {
	return ui.Row{Status: ui.StatusPass, Label: mutationLabel(name),
		Value: "1 of 1 mutant(s) killed in 1s"}
}

// mutationShardWithFindings writes one shard's report directory carrying both
// its rows and the findings its own survivors made, the way a real run leaves
// them beside each other in one document.
func mutationShardWithFindings(t *testing.T, findings []finding.Finding, rows ...ui.Row) string {
	t.Helper()
	dir := t.TempDir()
	rep := ui.NewReport("mutation")
	rep.AddFindings(findings...)
	for _, r := range rows {
		rep.Add(r)
	}
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
	return dir
}

// foldedShardsRow folds the given shard directories against mergeRepo's
// declaration — one component a shard reported, and one no shard did — and
// returns the row that says whether they are one run.
func foldedShardsRow(t *testing.T, reports ...string) ui.Row {
	t.Helper()
	rep, err := mergeMutationShards(t.Context(), mergeRepo(t), reports)
	if err != nil {
		t.Fatalf("the fold failed: %v", err)
	}
	for _, r := range rep.Rows() {
		if r.Label == "shards" {
			return r
		}
	}
	t.Fatal("the fold wrote no shards row")
	return ui.Row{}
}

// detailAbout is the one detail line naming a component, so a test asserts
// about the sentence the fold wrote rather than about the whole row.
func detailAbout(t *testing.T, row ui.Row, name string) string {
	t.Helper()
	for _, d := range row.Detail {
		if strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	t.Fatalf("no detail about %s in %v", name, row.Detail)
	return ""
}

// A component with no row and a surviving log is reported with what the run
// itself said it was about to cost, quoted: the projection is the only thing
// the artefacts of a vanished run actually state, and a reader deciding what to
// do about the gap wants that figure in front of them.
func TestAMissingComponentIsReportedWithTheProjectionItsLogCarries(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)
	line := mutationProjection412
	writeComponentLog(t, died, "b", "running the baseline suite", line, "mutant 1/412")

	detail := detailAbout(t, foldedShardsRow(t, ran, died), "b")
	if !strings.Contains(detail, "has no row in any shard's report") {
		t.Errorf("the fold stopped saying what it knows: %q", detail)
	}
	if !strings.Contains(detail, line) {
		t.Errorf("the projection %q is not quoted in %q", line, detail)
	}
	if !strings.Contains(detail, "projected") {
		t.Errorf("the quoted line is not named as a projection: %q", detail)
	}
}

// Three causes leave exactly this evidence behind — a job killed at its
// timeout, a runner out of memory, an upload that never arrived — so naming any
// of them is a guess in the voice of a diagnosis. The projection is a statement
// made before the run, and the sentence says so.
func TestTheFoldNamesNoCauseForAMissingComponent(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)
	writeComponentLog(t, died, "b", mutationProjection412)

	row := foldedShardsRow(t, ran, died)
	for _, claim := range []string{"too large", "too big", "timed out", "timeout", "out of memory", "killed"} {
		for _, d := range row.Detail {
			if strings.Contains(strings.ToLower(d), claim) {
				t.Errorf("the fold claims %q from evidence that cannot support it: %q", claim, d)
			}
		}
	}
}

// A log that never reached the projection says nothing about what the run was
// about to cost, so the fold says what it says with no log at all.
func TestALogWithNoProjectionAddsNothing(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)
	writeComponentLog(t, died, "b", "running the baseline suite", "ok fixture/b 1.2s")

	if got := detailAbout(t, foldedShardsRow(t, ran, died), "b"); got != "b has no row in any shard's report" {
		t.Errorf("a log carrying no projection changed the sentence to %q", got)
	}
}

// Nothing of the run survived, and the fold adds nothing to the one thing it
// can still say.
func TestNoLogLeavesTheSentenceAsItIs(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)

	if got := detailAbout(t, foldedShardsRow(t, ran, died), "b"); got != "b has no row in any shard's report" {
		t.Errorf("the sentence for a run that left nothing behind is %q", got)
	}
}

// download-artifact gives a matched artifact its own subdirectory only when the
// pattern matched two or more, so the directory the fold is handed is sometimes
// the extraction path itself. The log is found by the layout a run writes — a
// directory named for the component — so both shapes read the same, and no
// depth is assumed.
func TestALoneShardsUnnestedDirectoryReadsTheSameAsANestedOne(t *testing.T) {
	line := mutationProjection9

	flat := mutationShardDir(t, t.TempDir(), nil)
	writeComponentLog(t, flat, "b", line)

	nested := mutationShardDir(t, filepath.Join(t.TempDir(), "shard-b", ".lydite-reports"), nil)
	writeComponentLog(t, nested, "b", line)

	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	flatDetail := detailAbout(t, foldedShardsRow(t, ran, flat), "b")
	nestedDetail := detailAbout(t, foldedShardsRow(t, ran, nested), "b")
	for _, d := range []string{flatDetail, nestedDetail} {
		if !strings.Contains(d, line) {
			t.Errorf("the projection is not quoted in %q", d)
		}
	}
	if !strings.Contains(flatDetail, flat) {
		t.Errorf("the sentence does not name the directory the log was found in: %q", flatDetail)
	}
}

// A component declaring no suite is in no shard, so no shard reports it — and
// that is the plan working, not a shard that died. Its row comes from the
// declaration, and the component beside it folds from the shards as it would
// alone.
func TestTheMutationFoldReportsANoSuiteComponentFromItsDeclaration(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n"+
			"  - name: scripts\n    dir: scripts\n    lang: shell\n")
	for _, dir := range []string{"moda", "scripts"} {
		write(t, root, dir+"/.keep", "")
	}
	killed := ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "4 of 4 mutant(s) killed in 12s"}

	doc, err := runMutationMerge(t, root, mutationShard(t, killed), mutationShard(t))
	if err != nil {
		t.Fatalf("the fold failed over a component no shard was meant to run: %v", err)
	}
	shards, ok := rowNamed(doc, "shards")
	if !ok {
		t.Fatal("the fold emitted no shards row")
	}
	if shards.Status != ui.StatusPass || strings.Contains(strings.Join(shards.Detail, " "), "scripts has no row") {
		t.Errorf("shards = %+v, want a pass: no shard was meant to run scripts", shards)
	}
	row, ok := rowNamed(doc, mutationLabel("scripts"))
	if !ok {
		t.Fatal("the component declaring no suite took no row")
	}
	if row.Status != ui.StatusUnmeasured || !strings.Contains(row.Value, "declares no suite") {
		t.Errorf("mutation(scripts) = %+v, want unmeasured, naming that it declares no suite", row)
	}
	if got, _ := rowNamed(doc, mutationLabel("a")); got.Status != killed.Status || got.Value != killed.Value {
		t.Errorf("mutation(a) = %+v, want the shard's row folded unchanged", got)
	}
}

// readShards adds every shard's findings as they arrive, in --reports order —
// never re-sorted by declaration order or by component name. The declaration
// here (mergeRepo) names "a" before "b", and this passes --reports with "b"'s
// shard first, so a fold that reordered findings to match the declaration
// would report "a" first instead.
func TestFindingsCrossShardsInReportsOrderNotDeclarationOrder(t *testing.T) {
	root := mergeRepo(t)
	bSurvivor := finding.Finding{Gate: "mutation", Component: "b", Row: mutationLabel("b"),
		Path: "modb/b.go", Line: 3, Message: "b survived",
		Site: string(mutation.NegateConditional) + "\x1f<\x1f>="}
	aSurvivor := finding.Finding{Gate: "mutation", Component: "a", Row: mutationLabel("a"),
		Path: "moda/a.go", Line: 5, Message: "a survived",
		Site: string(mutation.NegateConditional) + "\x1f<\x1f>="}

	shardB := mutationShardWithFindings(t, []finding.Finding{bSurvivor},
		ui.Row{Status: ui.StatusFail, Label: mutationLabel("b"), Value: "1 of 2 mutant(s) survived in 3s",
			Detail: []string{bSurvivor.Message}})
	shardA := mutationShardWithFindings(t, []finding.Finding{aSurvivor},
		ui.Row{Status: ui.StatusFail, Label: mutationLabel("a"), Value: "1 of 2 mutant(s) survived in 3s",
			Detail: []string{aSurvivor.Message}})

	doc, err := runMutationMerge(t, root, shardB, shardA)
	if err == nil {
		t.Error("a survivor did not fail the fold")
	}
	if len(doc.Findings) != 2 {
		t.Fatalf("findings = %+v, want exactly the two survivors", doc.Findings)
	}
	if doc.Findings[0].Component != "b" || doc.Findings[1].Component != "a" {
		t.Errorf("findings are in component order %q, %q; want the --reports order (b's shard first)",
			doc.Findings[0].Component, doc.Findings[1].Component)
	}
}
