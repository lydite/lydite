package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/ui"
)

// twoComponents is the smallest declaration a gap in the fold is visible in:
// one component a shard reported, and one no shard did.
var twoComponents = component.File{Components: []component.Component{
	{Name: "a", Dir: "moda"},
	{Name: "b", Dir: "modb"},
}}

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

// foldedShardsRow folds the given shard directories and returns the row that
// says whether they are one run.
func foldedShardsRow(t *testing.T, reports ...string) ui.Row {
	t.Helper()
	rep := ui.NewReport("mutation")
	mergeMutationShards(rep, twoComponents, reports)
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
	line := costProjection(412, 4, budget(30*time.Second, 0))
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
	writeComponentLog(t, died, "b", costProjection(412, 4, budget(30*time.Second, 0)))

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
	line := costProjection(9, 4, budget(20*time.Second, 0))

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

// The projection is written through one format string and read back through the
// same one, so a reader cannot hold a copy of the wording that drifts from the
// writer's.
func TestTheProjectionIsReadBackThroughTheFormatItIsWrittenWith(t *testing.T) {
	line := costProjection(37, 3, budget(time.Minute, 0))
	got, ok := costProjectionIn(line)
	if !ok || got != line {
		t.Errorf("costProjectionIn(%q) = %q, %v; want the line back", line, got, ok)
	}
	for _, other := range []string{
		"running 412 tests",
		"",
		"9 mutant(s), budget 1m0s each, 4 worker(s): at most",
		"app | " + line,
	} {
		if got, ok := costProjectionIn(other); ok || got != "" {
			t.Errorf("%q was read as the projection %q, %v; want no line at all", other, got, ok)
		}
	}
}

// Every failure of the search answers with no line at all, and not with text a
// caller taking the line without its flag would go on to quote as something a
// run said: a component with no log and a log that never reached the projection
// are both nothing to say.
func TestAShardWithNoProjectionToQuoteAnswersWithNoLine(t *testing.T) {
	dir := mutationShardDir(t, t.TempDir(), nil)

	if line, ok := shardProjection(dir, "b"); ok || line != "" {
		t.Errorf("a component with no log answered %q, %v; want no line at all", line, ok)
	}

	writeComponentLog(t, dir, "b", "running the baseline suite", "ok fixture/b 1.2s")
	if line, ok := shardProjection(dir, "b"); ok || line != "" {
		t.Errorf("a log that never reached the projection answered %q, %v; want no line at all", line, ok)
	}
}

// A suite writes whatever it likes into the log the projection shares — a
// fixture dumped whole, a payload in a panic — and such a line is far longer
// than the limit a scanner reads with by default. The projection sits below
// those lines rather than above them, so a run that wrote one is read past it.
func TestTheProjectionIsFoundBelowALineLongerThanTheDefaultLimit(t *testing.T) {
	dir := mutationShardDir(t, t.TempDir(), nil)
	line := costProjection(412, 4, budget(30*time.Second, 0))
	writeComponentLog(t, dir, "b", strings.Repeat("x", 512*1024), line)

	got, ok := shardProjection(dir, "b")
	if !ok || got != line {
		t.Errorf("the projection below a long line read back as %q, %v; want %q", got, ok, line)
	}
}
