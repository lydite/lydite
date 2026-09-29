package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/mutation"
	shardreport "lydite/lydite/internal/shard"
	mutationstages "lydite/lydite/internal/stages/mutation"
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

// readRowOf is the row the fold wrote about reading one shard's directory.
func readRowOf(t *testing.T, doc ui.Document, dir string) ui.Row {
	t.Helper()
	row, ok := rowNamed(doc, "read("+dir+")")
	if !ok {
		t.Fatalf("the fold wrote no row about reading %s", dir)
	}
	return row
}

// inFlightMutant is a mutant spelled the way the executor names it in a log.
func inFlightMutant(line int) string {
	return mutation.Mutant{Path: "modb/b.go", Line: line, Column: 4,
		Operator: mutation.NegateConditional, Original: "<", Mutated: ">="}.String()
}

// A cancelled shard writes no report, and the log it leaves is the only account
// of what it was doing: the fold names each mutant the log started and never
// finished, and how many of the projected mutants did finish. The row still
// fails, and so does completeness — a shard with no report is a run the fold
// cannot count, however much its log explains.
func TestTheFoldNamesWhatACancelledShardWasRunning(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)
	one, two, three := inFlightMutant(1), inFlightMutant(2), inFlightMutant(3)
	writeComponentLog(t, died, "b",
		"running the baseline suite", mutationProjection9,
		"start "+one, one+": killed in 1.1s",
		"start "+two, "start "+three, three+": survived in 2s")

	doc, err := runMutationMerge(t, mergeRepo(t), ran, died)
	if err == nil {
		t.Error("a shard with no report folded cleanly")
	}
	row := readRowOf(t, doc, died)
	if row.Status != ui.StatusFail || row.Value != "no mutation report" {
		t.Errorf("read(%s) = %+v, want the failing row a shard with no report takes", died, row)
	}
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "2 of 9 mutant(s) finished") {
		t.Errorf("the detail does not count what finished against what was projected:\n%s", detail)
	}
	if !strings.Contains(detail, two) {
		t.Errorf("the detail does not name the mutant in flight %q:\n%s", two, detail)
	}
	for _, done := range []string{one, three} {
		if strings.Contains(detail, done) {
			t.Errorf("the detail names %q, which finished:\n%s", done, detail)
		}
	}
	if shards, _ := rowNamed(doc, "shards"); shards.Status != ui.StatusFail {
		t.Errorf("shards = %+v, want a failure: b has no row", shards)
	}
}

// A log that names no mutant and no projection is a run that stopped before
// the first mutant: a build, an install or a baseline that never returned. The
// fold says so, and still fails the shard.
func TestTheFoldSaysACancelledShardStoppedBeforeAnyMutantRan(t *testing.T) {
	ran := mutationShardDir(t, t.TempDir(), []ui.Row{mutationRowOf("a")})
	died := mutationShardDir(t, t.TempDir(), nil)
	writeComponentLog(t, died, "b", "running the baseline suite", "go: downloading example.com/m v1.0.0")

	doc, err := runMutationMerge(t, mergeRepo(t), ran, died)
	if err == nil {
		t.Error("a shard with no report folded cleanly")
	}
	row := readRowOf(t, doc, died)
	if row.Status != ui.StatusFail {
		t.Errorf("read(%s) = %+v, want a failure", died, row)
	}
	if !strings.Contains(strings.Join(row.Detail, "\n"), "the run stopped before any mutant ran") {
		t.Errorf("the detail does not say no mutant ran: %v", row.Detail)
	}
	if shards, _ := rowNamed(doc, "shards"); shards.Status != ui.StatusFail {
		t.Errorf("shards = %+v, want a failure: b has no row", shards)
	}
}

// `lydite test merge` shares the fold's reading of shards, and a directory
// holding a mutation log says nothing to it: its document for a directory with
// no report is the same bytes whatever else the directory holds, and the row
// says only why the report was not read.
func TestTheTestFoldReadsNothingBesideAShardWithNoReport(t *testing.T) {
	root := mergeRepo(t)
	ran := shardOf(t, "a", 1, 2)
	died := t.TempDir()

	before, err := runMergeCmd(t, root, ran, died)
	if err == nil {
		t.Fatalf("an empty report directory folded cleanly:\n%s", before)
	}
	writeComponentLog(t, died, "b", mutationProjection9, "start "+inFlightMutant(1))
	after, err := runMergeCmd(t, root, ran, died)
	if err == nil {
		t.Fatalf("an empty report directory folded cleanly:\n%s", after)
	}
	// The one field that differs between two runs of the same fold is how long
	// each took.
	elapsed := regexp.MustCompile(`"duration_ms": \d+`)
	before = elapsed.ReplaceAllString(before, `"duration_ms": 0`)
	if timeless := elapsed.ReplaceAllString(after, `"duration_ms": 0`); before != timeless {
		t.Errorf("a mutation log beside a missing test report changed the test fold:\nbefore:\n%s\nafter:\n%s", before, timeless)
	}

	var doc ui.Document
	if err := json.Unmarshal([]byte(after), &doc); err != nil {
		t.Fatalf("the test fold emitted no document: %v\n%s", err, after)
	}
	want := ui.Row{Status: ui.StatusFail, Label: "read(" + died + ")", Value: "no test report",
		Detail: []string{shardreport.Read(died, "test").Err.Error()}}
	if got := readRowOf(t, doc, died); !reflect.DeepEqual(got, want) {
		t.Errorf("read(%s) = %+v\nwant %+v", died, got, want)
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

// incompleteShard writes one shard's report directory the way a run the
// deadline stopped leaves it: a report marked incomplete, its rows, and the
// counts beside it — or none, when counts is nil.
func incompleteShard(t *testing.T, counts *mutantsDoc, rows ...ui.Row) string {
	t.Helper()
	dir := t.TempDir()
	rep := ui.NewReport("mutation")
	rep.MarkIncomplete()
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
	if counts != nil {
		data, err := json.Marshal(counts)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, mutantsName), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// incompleteCountsOf is the counts document of a shard whose component a the
// deadline stopped with measured of wanted mutants decided, s counting them.
func incompleteCountsOf(s mutation.Summary, measured, wanted int) *mutantsDoc {
	counts := mutation.CountsOf(s, 7*time.Second)
	counts.Incomplete = &mutation.IncompleteCounts{Measured: measured, Wanted: wanted}
	return &mutantsDoc{Tree: "abc", IncompleteComponents: map[string]mutantCounts{"a": counts}}
}

// completeShardB is a shard that mutated b to the end and killed everything.
func completeShardB(t *testing.T) string {
	t.Helper()
	return countsShard(t,
		mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"b": {Killed: 3, ElapsedSeconds: 30}}},
		ui.Row{Status: ui.StatusPass, Label: mutationLabel("b"), Value: "3 of 3 mutant(s) killed in 30s"})
}

// Incomplete wins the fold. A component any shard left unfinished keeps the
// row its shard gave it, out of the summary's total, and the fold exits 3 as
// the shard did — under --no-gate too, since the flag silences a survivor and
// not a measurement that never finished. A survivor found before the deadline
// outranks it, and the fold exits 1.
func TestAnIncompleteShardMakesTheFoldIncomplete(t *testing.T) {
	survivor := mutationSurvivor()
	clean := mutation.Summary{Killed: 2}
	survived := mutation.Summary{Killed: 1, Survived: 1}
	rowOf := func(s mutation.Summary, results []mutation.Result, noGate bool) ui.Row {
		row, _ := kindRow(mutationIncomplete("a", s, results, 2, 5), noGate)
		return row
	}
	for _, c := range []struct {
		name       string
		shard      func(t *testing.T) string
		wantExit   int
		wantStatus ui.Status
		wantCut    bool
	}{
		{
			name: "every shard complete",
			shard: func(t *testing.T) string {
				return countsShard(t, mutantsDoc{Tree: "abc", Components: map[string]mutantCounts{"a": {Killed: 2, ElapsedSeconds: 7}}},
					ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "2 of 2 mutant(s) killed in 7s"})
			},
			wantExit: ui.ExitPass, wantStatus: ui.StatusPass,
		},
		{
			name: "an incomplete shard",
			shard: func(t *testing.T) string {
				return incompleteShard(t, incompleteCountsOf(clean, 2, 5), rowOf(clean, nil, false))
			},
			wantExit: ui.ExitIncomplete, wantStatus: ui.StatusUnmeasured, wantCut: true,
		},
		{
			name: "an incomplete shard that found a survivor",
			shard: func(t *testing.T) string {
				return incompleteShard(t, incompleteCountsOf(survived, 2, 5), rowOf(survived, []mutation.Result{survivor}, false))
			},
			wantExit: ui.ExitFail, wantStatus: ui.StatusFail, wantCut: true,
		},
		{
			name: "an incomplete shard that found a survivor under --no-gate",
			shard: func(t *testing.T) string {
				return incompleteShard(t, incompleteCountsOf(survived, 2, 5), rowOf(survived, []mutation.Result{survivor}, true))
			},
			wantExit: ui.ExitIncomplete, wantStatus: ui.StatusContext, wantCut: true,
		},
		{
			name: "a shard the deadline stopped before its baseline",
			shard: func(t *testing.T) string {
				return incompleteShard(t, &mutantsDoc{Tree: "abc"}, unmeasuredRow(mutationLabel("a"),
					"the run reached its deadline before this component's baseline suite ran, rerun to resume"))
			},
			wantExit: ui.ExitIncomplete, wantStatus: ui.StatusUnmeasured,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, err := runMutationMerge(t, mergeRepo(t), c.shard(t), completeShardB(t))
			if doc.Exit != c.wantExit {
				t.Errorf("exit = %d, want %d; rows %+v", doc.Exit, c.wantExit, doc.Rows)
			}
			var exit ui.ExitError
			if (c.wantExit == ui.ExitPass) != (err == nil) || (err != nil && (!errors.As(err, &exit) || exit.Code != c.wantExit)) {
				t.Errorf("the fold returned %v, want exit %d", err, c.wantExit)
			}
			row, ok := rowNamed(doc, mutationLabel("a"))
			if !ok || row.Status != c.wantStatus {
				t.Errorf("mutation(a) = %+v, want %s", row, c.wantStatus)
			}
			if c.wantCut && !rowIncomplete(row) {
				t.Errorf("mutation(a) = %+v, want it to say how far it got", row)
			}
			summary, _ := rowNamed(doc, "mutation")
			if c.wantCut {
				want := "3 of 3 mutant(s) killed across 1 component(s) in 30s"
				if summary.Value != want || !detailSaying(summary, "1 component(s) stopped before every mutant had a verdict") {
					t.Errorf("summary = %+v, want %q and the incomplete component named as left out", summary, want)
				}
			}
			if shards, _ := rowNamed(doc, "shards"); shards.Status != ui.StatusPass {
				t.Errorf("shards = %+v, want the incomplete shard folded in as one of the run", shards)
			}
		})
	}
}

// A shard contradicting itself — a passing row beside counts saying the
// component did not finish — folds as the counts say: a gate that never
// finished does not render as one that cleared.
func TestAPassingRowOverIncompleteCountsFoldsUnmeasured(t *testing.T) {
	shard := countsShard(t, *incompleteCountsOf(mutation.Summary{Killed: 2}, 2, 5),
		ui.Row{Status: ui.StatusPass, Label: mutationLabel("a"), Value: "2 of 2 mutant(s) killed in 7s"})
	doc, _ := runMutationMerge(t, mergeRepo(t), shard, completeShardB(t))
	row, _ := rowNamed(doc, mutationLabel("a"))
	if row.Status != ui.StatusUnmeasured || row.Value != "2 of 5 measured, rerun to resume" {
		t.Errorf("mutation(a) = %+v, want unmeasured, saying how far it got", row)
	}
	if doc.Exit != ui.ExitIncomplete {
		t.Errorf("exit = %d, want %d", doc.Exit, ui.ExitIncomplete)
	}
	// The shard's own document is read as it was written, so the fold's
	// replacement is its alone.
	if b, _ := rowNamed(doc, mutationLabel("b")); b.Status != ui.StatusPass {
		t.Errorf("mutation(b) = %+v, want its own passing row", b)
	}
}

// A shard whose counts were not read has its score read back out of its rows,
// and an incomplete component's row that found a survivor states its score in
// the words a complete one does. The fold recognises the row by the progress
// it carries and refuses to read a partial count back as a finished score.
func TestTheFoldNeverReadsAnIncompleteRowBackAsAScore(t *testing.T) {
	survivor := mutationSurvivor()
	for _, c := range []struct {
		name     string
		s        mutation.Summary
		results  []mutation.Result
		wantExit int
	}{
		{name: "with a survivor", s: mutation.Summary{Killed: 1, Survived: 1}, results: []mutation.Result{survivor}, wantExit: ui.ExitFail},
		{name: "with none", s: mutation.Summary{Killed: 2}, wantExit: ui.ExitIncomplete},
	} {
		t.Run(c.name, func(t *testing.T) {
			row, _ := kindRow(mutationIncomplete("a", c.s, c.results, 2, 5), false)
			doc, _ := runMutationMerge(t, mergeRepo(t), mutationShard(t, row), completeShardB(t))
			summary, _ := rowNamed(doc, "mutation")
			if want := "3 of 3 mutant(s) killed across 1 component(s) in 30s"; summary.Value != want {
				t.Errorf("summary = %q, want %q: the partial count was read back as a score", summary.Value, want)
			}
			if doc.Exit != c.wantExit {
				t.Errorf("exit = %d, want %d", doc.Exit, c.wantExit)
			}
		})
	}
	// A fold of nothing but an unfinished component reports no score at all,
	// and says why.
	row, _ := kindRow(mutationIncomplete("a", mutation.Summary{Killed: 2}, nil, 2, 5), false)
	decl := component.File{Components: []component.Component{{Name: "a"}}}
	folded := foldedMutationRow([]shardInput{{read: true, doc: ui.Document{Rows: []ui.Row{row}}}}, mutantsDoc{}, decl)
	if folded.Value != "no component was mutated" || !detailSaying(folded, "1 component(s) stopped") {
		t.Errorf("summary = %+v, want no score and the unfinished component named", folded)
	}
}

// Every row a run gives an incomplete component says how far it got in the
// words the fold recognises — unmeasured, failing, under --no-gate, and with
// its teardown's row in front — and no completed component's row does. This
// is what holds the renderer and the fold's reader together.
func TestEveryIncompleteRowIsRecognisedAsIncomplete(t *testing.T) {
	survivor := mutationSurvivor()
	cut := mutationIncomplete("a", mutation.Summary{Killed: 1}, nil, 1, 4)
	cutSurvived := mutationIncomplete("a", mutation.Summary{Survived: 1}, []mutation.Result{survivor}, 1, 4)
	torn := cut
	torn.TeardownErr = errors.New("docker compose down: exit status 1")
	for name, c := range map[string]struct {
		o      mutationstages.ComponentOutcome
		noGate bool
	}{
		"unmeasured":             {o: cut},
		"failing on a survivor":  {o: cutSurvived},
		"under --no-gate":        {o: cutSurvived, noGate: true},
		"with a failed teardown": {o: torn},
	} {
		row, _ := outcomeRow(c.o, c.noGate)
		if !rowIncomplete(row) {
			t.Errorf("%s: %+v is not recognised as incomplete", name, row)
		}
	}
	done, _ := outcomeRow(mutationCompleted("a", mutation.Summary{Survived: 1}, []mutation.Result{survivor}, time.Second), false)
	if rowIncomplete(done) {
		t.Errorf("a completed row %+v is recognised as incomplete", done)
	}
}
