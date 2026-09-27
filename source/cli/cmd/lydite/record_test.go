package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
	recordstages "lydite/lydite/internal/stages/record"
	"lydite/lydite/internal/ui"
)

// recordCmdFixture is one repository a recording runs over, and the report
// directories it is handed.
type recordCmdFixture struct {
	root    string
	reports []string
	// names maps each placeholder an expected output is written with to what
	// it stands for in this fixture: a temporary path, or a tree the fixture
	// computed, neither of which an expected string can spell out.
	names map[string]string
}

// recordCmdOneComponent declares a single Go component, which every fixture
// below measures.
const recordCmdOneComponent = "components:\n  - name: svc\n    dir: svc\n    runner: go-test\n    args: [\"./...\"]\n"

// recordCmdRepo is a repository holding the declaration, the configuration and
// a file in each named directory, committed once and pushed to a file://
// origin — the remote the state branch is fetched from and pushed to. An empty
// configuration writes no file, which is the defaults.
func recordCmdRepo(t *testing.T, declaration, cfg string, dirs ...string) string {
	t.Helper()
	root, origin := t.TempDir(), t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run(origin, "init", "--bare", "-b", "main", ".")
	run(root, "init", "-b", "main", ".")
	run(root, "config", "user.email", "t@t")
	run(root, "config", "user.name", "t")
	write(t, root, component.FileName, declaration)
	if cfg != "" {
		write(t, root, config.FileName, cfg)
	}
	for _, dir := range dirs {
		write(t, root, dir+"/README.md", dir+"\n")
	}
	run(root, "add", "-A")
	run(root, "commit", "-m", "base")
	run(root, "remote", "add", "origin", "file://"+origin)
	run(root, "push", "--quiet", "-u", "origin", "main")
	return root
}

// recordCmdMeasured is a measurement of svc taken on the tree root has checked
// out: one of two lines covered.
func recordCmdMeasured(t *testing.T, root string) measurementsDoc {
	t.Helper()
	return measurementsDoc{
		Tree:       treeOf(t, root),
		Components: map[string]componentMeasurement{"svc": {Entry: producing(1, 2, "go 1.26")}},
	}
}

// recordCmdTextDuration and recordCmdJSONDuration match the one figure a
// report carries that no fixture controls: how long the run took, in each
// grammar.
var (
	recordCmdTextDuration = regexp.MustCompile(`(?m)^(record (?:passed|failed|referred)) in [0-9]+\.[0-9]s$`)
	recordCmdJSONDuration = regexp.MustCompile(`"duration_ms": [0-9]+`)
)

// recordCmdNormalise writes every value that differs between runs of the suite
// as its placeholder, longest first so a path is never half-replaced by a
// shorter path it begins with.
func recordCmdNormalise(fx recordCmdFixture, s string) string {
	placeholders := make([]string, 0, len(fx.names))
	for p := range fx.names {
		placeholders = append(placeholders, p)
	}
	sort.Slice(placeholders, func(i, j int) bool {
		return len(fx.names[placeholders[i]]) > len(fx.names[placeholders[j]])
	})
	for _, p := range placeholders {
		s = strings.ReplaceAll(s, fx.names[p], p)
	}
	s = recordCmdTextDuration.ReplaceAllString(s, "$1 in 0.0s")
	return recordCmdJSONDuration.ReplaceAllString(s, `"duration_ms": 0`)
}

// recordCmdExec runs the command over the fixture, in the terminal grammar or
// the document one, and answers its normalised stdout and the error it
// returned.
func recordCmdExec(t *testing.T, fx recordCmdFixture, asJSON bool) (string, error) {
	t.Helper()
	args := []string{"--dir", fx.root, "--no-color"}
	for _, dir := range fx.reports {
		args = append(args, "--reports", dir)
	}
	if asJSON {
		args = append(args, "--json")
	}
	cmd := newRecordCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return recordCmdNormalise(fx, out.String()), err
}

// recordCmdCheck runs the command over a fresh fixture once per grammar, and
// asserts stdout byte for byte, the error it returned, and the document it
// saved beside the reports — which is the --json bytes whichever grammar
// reached stdout, and absent when the command reached no answer at all.
//
// A fresh fixture per grammar, because a recording writes to the state branch
// and a second run over the same one would be answering a different question.
// The fixture the document grammar ran over is returned, for a test that
// asserts what reached the branch.
func recordCmdCheck(t *testing.T, build func(t *testing.T) recordCmdFixture, wantText, wantJSON, wantErr string) recordCmdFixture {
	t.Helper()
	var fx recordCmdFixture
	for _, asJSON := range []bool{false, true} {
		fx = build(t)
		got, err := recordCmdExec(t, fx, asJSON)
		want := wantText
		if asJSON {
			want = wantJSON
		}
		if got != want {
			t.Errorf("--json=%v stdout:\n%s\nwant:\n%s", asJSON, got, want)
		}
		recordCmdCheckErr(t, err, wantErr)

		saved, readErr := os.ReadFile(filepath.Join(reportsDir(fx.root), documentName("record"))) // #nosec G304 -- a temp directory this test owns
		switch {
		case wantJSON == "":
			if !errors.Is(readErr, os.ErrNotExist) {
				t.Errorf("--json=%v saved a document (%v) for a run that reached no answer:\n%s", asJSON, readErr, saved)
			}
		case readErr != nil:
			t.Errorf("--json=%v saved no document: %v", asJSON, readErr)
		case recordCmdNormalise(fx, string(saved)) != wantJSON:
			t.Errorf("--json=%v saved document:\n%s\nwant:\n%s", asJSON, recordCmdNormalise(fx, string(saved)), wantJSON)
		}
	}
	return fx
}

// recordCmdCheckErr asserts the error's text, and that it is a ui.ExitError
// exactly when the text is one: a run that reached a verdict has already said
// so on stdout, and any other error is one main has to print.
func recordCmdCheckErr(t *testing.T, err error, want string) {
	t.Helper()
	var exit ui.ExitError
	switch {
	case want == "":
		if err != nil {
			t.Errorf("err = %v, want none", err)
		}
	case err == nil:
		t.Errorf("err = nil, want %q", want)
	case err.Error() != want:
		t.Errorf("err = %q, want %q", err.Error(), want)
	case errors.As(err, &exit) != strings.HasPrefix(want, "exit status "):
		t.Errorf("err = %T %q, want a ui.ExitError exactly when the run reached a verdict", err, err)
	}
}

// recordCmdOnBranch reports whether origin's state branch holds path.
func recordCmdOnBranch(t *testing.T, root, path string) bool {
	t.Helper()
	ctx := context.Background()
	if r := executil.RunQuiet(ctx, root, "git", "fetch", "--quiet", "origin", gitstate.BranchName); !r.Ok() {
		return false
	}
	return executil.RunQuiet(ctx, root, "git", "cat-file", "-e", "origin/"+gitstate.BranchName+":"+path).Ok()
}

// A measurement the branch already holds for this tree is not written again,
// and the row says the tree already holds it rather than that it was recorded.
func TestRecordingAMeasurementTheTreeAlreadyHoldsSaysSo(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) recordCmdFixture {
		root := recordCmdRepo(t, recordCmdOneComponent, "", "svc")
		if err := writeMeasurements(root, recordCmdMeasured(t, root)); err != nil {
			t.Fatal(err)
		}
		fx := recordCmdFixture{root: root, reports: []string{reportsDir(root)}, names: map[string]string{
			"<REPORTS>": reportsDir(root), "<ROOT>": root, "<TREE>": shortSHA(treeOf(t, root)),
		}}
		if _, err := recordCmdExec(t, fx, false); err != nil {
			t.Fatalf("the first recording: %v", err)
		}
		return fx
	}
	recordCmdCheck(t, build, `→ read(<REPORTS>) 1 component(s) for <TREE>
→ findings ...................... not counted — no report directory holds a scan.json
✓ record ........................ <TREE> already holds this measurement
→ history ....................... already recorded

record passed in 0.0s
`, `{
  "command": "record",
  "verdict": "pass",
  "exit": 0,
  "duration_ms": 0,
  "rows": [
    {
      "status": "context",
      "label": "read(<REPORTS>)",
      "value": "1 component(s) for <TREE>"
    },
    {
      "status": "context",
      "label": "findings",
      "value": "not counted — no report directory holds a scan.json"
    },
    {
      "status": "pass",
      "label": "record",
      "value": "<TREE> already holds this measurement"
    },
    {
      "status": "context",
      "label": "history",
      "value": "already recorded"
    }
  ]
}
`, "")
}

// A measurement taken on another tree is not recorded here, and nothing
// reaches the branch: it is refused before the findings are counted or the
// history composed, because a record filed against this commit would describe
// another one.
func TestAMeasurementOfAnotherTreeIsRefusedAndWritesNothing(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) recordCmdFixture {
		root := recordCmdRepo(t, recordCmdOneComponent, "", "svc")
		doc := recordCmdMeasured(t, root)
		doc.Tree = strings.Repeat("5eed", 10)
		if err := writeMeasurements(root, doc); err != nil {
			t.Fatal(err)
		}
		return recordCmdFixture{root: root, reports: []string{reportsDir(root)}, names: map[string]string{
			"<REPORTS>": reportsDir(root), "<ROOT>": root, "<TREE>": shortSHA(treeOf(t, root)),
		}}
	}
	fx := recordCmdCheck(t, build, `→ read(<REPORTS>) 1 component(s) for 5eed5eed5eed
✗ record ........................ not recorded — the measurement was taken on 5eed5eed5eed, but <TREE> is checked out
  → record where the measurement was taken, or check that tree out first

record failed in 0.0s
`, `{
  "command": "record",
  "verdict": "fail",
  "exit": 1,
  "duration_ms": 0,
  "rows": [
    {
      "status": "context",
      "label": "read(<REPORTS>)",
      "value": "1 component(s) for 5eed5eed5eed"
    },
    {
      "status": "fail",
      "label": "record",
      "value": "not recorded — the measurement was taken on 5eed5eed5eed, but <TREE> is checked out",
      "detail": [
        "record where the measurement was taken, or check that tree out first"
      ]
    }
  ]
}
`, "exit status 1")
	if recordCmdOnBranch(t, fx.root, ledger.Dir) || recordCmdOnBranch(t, fx.root, gitstate.StatePath(treeOf(t, fx.root))) {
		t.Error("a measurement of another tree reached the state branch")
	}
}

// Report directories holding no measurements document render nothing at all
// and fail with the document's name, even when one of them holds a scan: only
// measurements name the tree a recording is bound to.
func TestReportDirectoriesHoldingNoMeasurementsRenderNothing(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) recordCmdFixture {
		root := recordCmdRepo(t, recordCmdOneComponent, "", "svc")
		empty := filepath.Join(t.TempDir(), "report-directory-holding-nothing")
		if err := os.MkdirAll(empty, 0o750); err != nil {
			t.Fatal(err)
		}
		scanned := filepath.Join(t.TempDir(), "report-directory-holding-a-scan")
		scanDocument(t, scanned, nil, gosecFinding("svc", "sha1.New()"))
		return recordCmdFixture{root: root, reports: []string{empty, scanned}, names: map[string]string{"<ROOT>": root}}
	}
	recordCmdCheck(t, build, "", "",
		"none of the named report directories holds a measurements.json\n       a `lydite test` run writes one; a run with --no-coverage does not")
}

// A baseline missing a declared component is refused, and the history the
// same measurement carries is appended all the same: a baseline is a cache
// refused whenever it would be partial, and a record is not.
func TestABaselineMissingADeclaredComponentIsRefusedAndItsHistoryLands(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) recordCmdFixture {
		root := recordCmdRepo(t, recordCmdOneComponent+"  - name: api\n    dir: api\n    runner: go-test\n    args: [\"./...\"]\n", "", "svc", "api")
		if err := writeMeasurements(root, recordCmdMeasured(t, root)); err != nil {
			t.Fatal(err)
		}
		return recordCmdFixture{root: root, reports: []string{reportsDir(root)}, names: map[string]string{
			"<REPORTS>": reportsDir(root), "<ROOT>": root, "<TREE>": shortSHA(treeOf(t, root)),
		}}
	}
	fx := recordCmdCheck(t, build, `→ read(<REPORTS>) 1 component(s) for <TREE>
→ findings ...................... not counted — no report directory holds a scan.json
✗ record ........................ not recorded — api has no entry, and a baseline missing a component gates on nothing
  → the next change against this tree measures it instead of gating against a partial baseline
→ history ....................... 1 record(s) appended

record failed in 0.0s
`, `{
  "command": "record",
  "verdict": "fail",
  "exit": 1,
  "duration_ms": 0,
  "rows": [
    {
      "status": "context",
      "label": "read(<REPORTS>)",
      "value": "1 component(s) for <TREE>"
    },
    {
      "status": "context",
      "label": "findings",
      "value": "not counted — no report directory holds a scan.json"
    },
    {
      "status": "fail",
      "label": "record",
      "value": "not recorded — api has no entry, and a baseline missing a component gates on nothing",
      "detail": [
        "the next change against this tree measures it instead of gating against a partial baseline"
      ]
    },
    {
      "status": "context",
      "label": "history",
      "value": "1 record(s) appended"
    }
  ]
}
`, "exit status 1")
	if recordCmdOnBranch(t, fx.root, gitstate.StatePath(treeOf(t, fx.root))) {
		t.Error("a baseline missing a declared component reached the state branch")
	}
	if !recordCmdOnBranch(t, fx.root, ledger.Dir) {
		t.Error("the history the refused baseline carried never reached the state branch")
	}
}

// Each report directory gets one row saying what came out of it: context when
// it yielded a document, amber when it yielded none, and every document that is
// there and will not parse named in its detail — a missing one only when
// nothing else came out of that directory.
//
// Go, Semgrep and gitleaks are switched off so the findings row does not
// depend on how many gates a language's checks report under.
func TestEachReportDirectorySaysWhatItHeldAndWhatWouldNotParse(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) recordCmdFixture {
		root := recordCmdRepo(t, recordCmdOneComponent,
			"go:\n  enabled: false\nsemgrep:\n  enabled: false\nsecrets:\n  enabled: false\n", "svc")
		tree := treeOf(t, root)

		// Every document there and readable.
		whole := filepath.Join(t.TempDir(), "report-directory-holding-every-document")
		if err := writeMeasurements(whole, recordCmdMeasured(t, root)); err != nil {
			t.Fatal(err)
		}
		if err := writeMutants(whole, mutantsDoc{Tree: tree, Components: map[string]mutantCounts{"svc": {Killed: 3}}}); err != nil {
			t.Fatal(err)
		}
		scanDocument(t, reportsDir(whole), nil, gosecFinding("svc", "sha1.New()"))

		// Nothing there at all.
		empty := filepath.Join(t.TempDir(), "report-directory-holding-nothing")
		if err := os.MkdirAll(empty, 0o750); err != nil {
			t.Fatal(err)
		}

		// Every document there and none of them readable.
		broken := filepath.Join(t.TempDir(), "report-directory-holding-nothing-readable")
		for _, name := range []string{measurementsName, documentName("scan"), mutantsName} {
			write(t, broken, name, "{not json")
		}

		// A readable scan beside measurements that name no tree.
		treeless := filepath.Join(t.TempDir(), "report-directory-holding-treeless-measurements")
		write(t, treeless, measurementsName, "{}\n")
		scanDocument(t, treeless, nil)

		return recordCmdFixture{root: root, reports: []string{reportsDir(whole), empty, broken, treeless}, names: map[string]string{
			"<ROOT>": root, "<TREE>": shortSHA(tree),
			"<WHOLE>": reportsDir(whole), "<EMPTY>": empty, "<BROKEN>": broken, "<TREELESS>": treeless,
		}}
	}
	recordCmdCheck(t, build, `→ read(<WHOLE>) 1 component(s) for <TREE>, 1 finding(s) from scan, 1 component(s) mutated for <TREE>
! read(<EMPTY>) no measurements
  → open <EMPTY>/measurements.json: no such file or directory
! read(<BROKEN>) no measurements
  → <BROKEN>/measurements.json: invalid character 'n' looking for beginning of object key string
  → <BROKEN>/scan.json: read report document: invalid character 'n' looking for beginning of object key string
  → <BROKEN>/mutants.json: invalid character 'n' looking for beginning of object key string
→ read(<TREELESS>) 0 finding(s) from scan
  → <TREELESS>/measurements.json: names no tree, so there is nothing it can be recorded against
→ findings ...................... not counted — no declared component has a gate that reports findings
✓ record ........................ 1 component(s) recorded for <TREE>
→ history ....................... 1 record(s) appended

record passed in 0.0s
`, `{
  "command": "record",
  "verdict": "pass",
  "exit": 0,
  "duration_ms": 0,
  "rows": [
    {
      "status": "context",
      "label": "read(<WHOLE>)",
      "value": "1 component(s) for <TREE>, 1 finding(s) from scan, 1 component(s) mutated for <TREE>"
    },
    {
      "status": "unmeasured",
      "label": "read(<EMPTY>)",
      "value": "no measurements",
      "detail": [
        "open <EMPTY>/measurements.json: no such file or directory"
      ]
    },
    {
      "status": "unmeasured",
      "label": "read(<BROKEN>)",
      "value": "no measurements",
      "detail": [
        "<BROKEN>/measurements.json: invalid character 'n' looking for beginning of object key string",
        "<BROKEN>/scan.json: read report document: invalid character 'n' looking for beginning of object key string",
        "<BROKEN>/mutants.json: invalid character 'n' looking for beginning of object key string"
      ]
    },
    {
      "status": "context",
      "label": "read(<TREELESS>)",
      "value": "0 finding(s) from scan",
      "detail": [
        "<TREELESS>/measurements.json: names no tree, so there is nothing it can be recorded against"
      ]
    },
    {
      "status": "context",
      "label": "findings",
      "value": "not counted — no declared component has a gate that reports findings"
    },
    {
      "status": "pass",
      "label": "record",
      "value": "1 component(s) recorded for <TREE>"
    },
    {
      "status": "context",
      "label": "history",
      "value": "1 record(s) appended"
    }
  ]
}
`, "")
}

// The reader folds the documents its reads returned, and never reads a
// directory a second time: what was folded is what each directory's row said
// it held. A directory it never read is refused rather than folded as an empty
// document.
func TestTheReportReaderFoldsWhatItReadRatherThanReadingAgain(t *testing.T) {
	root := t.TempDir()
	dir := reportsDir(root)
	if err := writeMeasurements(root, measurementsDoc{
		Tree:       "deadbeef",
		Components: map[string]componentMeasurement{"svc": {Entry: producing(1, 2, "go 1.26")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeMutants(root, mutantsDoc{Tree: "deadbeef", Components: map[string]mutantCounts{
		"svc": {Killed: 1, TimedOut: 2, OutOfMemory: 3, Survived: 4, Unviable: 5, Acknowledged: 6, ElapsedSeconds: 7.5},
	}}); err != nil {
		t.Fatal(err)
	}

	reader := newRecordReports()
	if _, err := reader.ReadMeasurements(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadMutants(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{measurementsName, mutantsName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	measured, err := reader.FoldMeasurements([]string{dir})
	if err != nil {
		t.Fatalf("folding what was read: %v", err)
	}
	if measured.Tree != "deadbeef" || measured.Components["svc"].Covered != 1 || measured.Snapshot.Coverage["svc"].Covered != 1 {
		t.Errorf("folded measurements = %+v, want svc's one covered line on deadbeef, in the snapshot too", measured)
	}
	mutated, err := reader.FoldMutants([]string{dir})
	if err != nil {
		t.Fatalf("folding what was read: %v", err)
	}
	want := recordstages.MutantCounts{Killed: 1, TimedOut: 2, OutOfMemory: 3, Survived: 4, Unviable: 5, Acknowledged: 6, ElapsedSeconds: 7.5}
	if mutated.Tree != "deadbeef" || mutated.Components["svc"] != want {
		t.Errorf("folded mutants = %+v, want %+v on deadbeef", mutated, want)
	}

	unread := t.TempDir()
	if _, err := reader.FoldMeasurements([]string{unread}); err == nil {
		t.Error("a directory the reader never read was folded")
	}
	if _, err := reader.FoldMutants([]string{unread}); err == nil {
		t.Error("a directory the reader never read was folded")
	}
}

// Each reason a recording appends no history reads as its own, and a history
// nothing composed is an error rather than a row claiming an append.
func TestEveryHistoryReasonSaysWhyNothingWasAppended(t *testing.T) {
	for _, tc := range []struct {
		composed recordstages.ComposeHistoryOut
		want     string
	}{
		{recordstages.ComposeHistoryOut{Reason: recordstages.HistoryToAppend}, ""},
		{recordstages.ComposeHistoryOut{Reason: recordstages.HistoryNoBranch},
			"this checkout names no branch, so pass " + gitstate.BranchFlag +
				" — history is per branch, and one filed under the wrong branch is worse than none"},
		{recordstages.ComposeHistoryOut{Reason: recordstages.HistoryNoScalar}, "no component produced a scalar"},
		{recordstages.ComposeHistoryOut{Reason: recordstages.HistoryUndescribed, Err: errors.New("no HEAD")},
			"this commit could not be described: no HEAD"},
	} {
		got, err := historyWhy(tc.composed)
		if err != nil || got != tc.want {
			t.Errorf("historyWhy(%d) = %q, %v; want %q", tc.composed.Reason, got, err, tc.want)
		}
	}
	if got, err := historyWhy(recordstages.ComposeHistoryOut{}); err == nil || got != "" {
		t.Errorf("historyWhy(0) = %q, %v; want no reason and an error for a history nothing composed", got, err)
	}
}

// A baseline nothing decided is an error rather than a row: read as a
// recording, it would say a baseline landed that no stage decided to land.
func TestABaselineWithNoVerdictRendersNoRow(t *testing.T) {
	if row, err := baselineRow(recordstages.DecideBaselineOut{}, "deadbeef"); err == nil {
		t.Errorf("a baseline with no verdict rendered %+v", row)
	}
}
