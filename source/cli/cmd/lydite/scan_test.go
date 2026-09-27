package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/shell"
	scanstages "lydite/lydite/internal/stages/scan"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

// semgrep.Check's writer parameter is only useful if the call site actually
// passes cmd.ErrOrStderr() rather than some other stream — a unit test in
// internal/semgrep can prove warnSemgrepignore itself works and still miss a
// caller that wired the writer to the wrong place. PATH is stripped so
// neither semgrep nor pipx is found: ensure() then fails immediately, with no
// network and no pipx install, and Check still runs warnSemgrepignore first.
func TestScanPassesItsStderrToSemgrepsWriter(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, config.FileName, "go:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, ".semgrepignore", "docs/\n")

	var out, errOut bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	if !strings.Contains(errOut.String(), ".semgrepignore") {
		t.Fatalf("stderr = %q, want the .semgrepignore warning cmd.ErrOrStderr() was told about", errOut.String())
	}
}

// A failing check whose findings never streamed must print them. Biome sends
// its report to a file so the JSON cannot be corrupted by its own chatter,
// which means nothing reaches the terminal on its own — printing a bare
// status line left the developer to re-run the pinned toolchain by hand to
// find out what was wrong, and put nothing in the PR comment either.
func TestReportPrintsDetailForFailingChecks(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := report(cmd, ui.NewReport("scan"), t.TempDir(), []executil.Result{
		{
			Name:   "biome(.)",
			Detail: "src/bad.ts:1  lint/security/noGlobalEval  eval() is dangerous\nsrc/bad.ts:4  lint/correctness/noUnusedVariables  unused",
			Err:    errors.New("2 finding(s)"),
		},
	}, nil, false, true)
	if err == nil {
		t.Fatal("a failing check must still return an error")
	}
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("a failing gate must exit 1, got %v", err)
	}
	for _, want := range []string{"✗ biome(.)", "noGlobalEval", "eval() is dangerous", "noUnusedVariables"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report output missing %q:\n%s", want, out.String())
		}
	}
}

// The JSON report is what anything automated reads, so it must carry the
// same verdict and the same findings as the text — a machine surface that
// can disagree with the human one is worse than no machine surface.
func TestReportJSONCarriesVerdictAndDetail(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), []executil.Result{
		{Name: "biome(.)", Detail: "src/bad.ts:1  noGlobalEval", Err: errors.New("1 finding(s)")},
	}, nil, true, false)
	var got struct {
		Command string `json:"command"`
		Verdict string `json:"verdict"`
		Exit    int    `json:"exit"`
		Rows    []struct {
			Status string   `json:"status"`
			Label  string   `json:"label"`
			Detail []string `json:"detail"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the machine report must be valid JSON: %v\n%s", err, out.String())
	}
	if got.Command != "scan" || got.Verdict != "fail" || got.Exit != 1 {
		t.Errorf("got command=%q verdict=%q exit=%d, want scan/fail/1", got.Command, got.Verdict, got.Exit)
	}
	if len(got.Rows) != 1 || got.Rows[0].Status != "fail" || got.Rows[0].Label != "biome(.)" {
		t.Fatalf("unexpected rows: %+v", got.Rows)
	}
	if len(got.Rows[0].Detail) != 1 || !strings.Contains(got.Rows[0].Detail[0], "noGlobalEval") {
		t.Errorf("the finding must survive into JSON, got %+v", got.Rows[0].Detail)
	}
}

// A finding's own message is attacker-adjacent text: it can contain anything
// the scanned source contains, including something shaped like a verdict. A
// detail line is indented so it can never begin a line the way a status row
// does, which is what stops a finding from forging one.
func TestReportDetailCannotForgeAStatusLine(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), []executil.Result{
		{Name: "biome(.)", Detail: "✓ biome(.) ... passed", Err: errors.New("1 finding(s)")},
	}, nil, false, true)
	statusLines := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "✓ ") || strings.HasPrefix(line, "✗ ") {
			statusLines++
		}
	}
	if statusLines != 1 {
		t.Errorf("got %d unindented status lines, want exactly 1 — a finding forged one:\n%s", statusLines, out.String())
	}
}

// Passing checks print no detail, and a tool that streamed its own output is
// not reprinted: doing either would duplicate the log or bury the summary.
func TestReportPrintsNoDetailForPassingOrStreamingChecks(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), []executil.Result{
		{Name: "biome(.)", Detail: "should not appear"},
		{Name: "semgrep", Output: "already streamed to the terminal", Err: errors.New("findings")},
	}, nil, false, true)
	for _, unwanted := range []string{"should not appear", "already streamed"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("report printed %q:\n%s", unwanted, out.String())
		}
	}
}

// A repository that declares nothing is scanned by nothing, and an amber row
// would leave the job green over an unscanned tree — a security scan that
// silently stopped. The error names the file the author has to write.
func TestScanRefusesARepositoryThatDeclaresNoComponents(t *testing.T) {
	dir := t.TempDir()

	cmd := newScanCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir})

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("a repository declaring no components was scanned anyway")
	}
	var exitErr ui.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("want an error, not a verdict: %v", err)
	}
	if !strings.Contains(err.Error(), component.FileName) {
		t.Errorf("error should name the file to write, got: %v", err)
	}
}

// Each of the three keys narrowed a walk for manifests, and the walk is gone.
// Ignoring one would leave a repository scanning something other than what its
// author wrote while every run still reported a pass.
func TestScanRejectsARetiredExcludeKey(t *testing.T) {
	for _, key := range []string{"rust", "typescript", "go"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			writeLydite(t, dir, config.FileName, key+":\n  exclude: [\"legacy\"]\n")
			writeLydite(t, dir, component.FileName, "components:\n  - name: c\n    dir: .\n    runner: go-test\n")

			cmd := newScanCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--dir", dir})

			err := cmd.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("a retired exclude key was accepted")
			}
			if !strings.Contains(err.Error(), key+".exclude") {
				t.Errorf("error should name the key, got: %v", err)
			}
		})
	}
}

// A component lydite cannot derive a language for is one nothing scans.
// Dropping it in silence reads exactly like a component that was scanned and
// found clean, so it gets a row of its own — unlike a language the repository
// switched off, which is a decision rather than an incapacity.
func TestScanReportsAComponentWithNoDerivableLanguage(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: legacy\n    dir: .\n    command: [\"make\", \"check\"]\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var doc struct {
		Rows []struct{ Status, Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	// One row per gate the component would have had, and — because nothing
	// else ran either — the run's. All are unmeasured: each gate says why it
	// did not apply, and the last says no check executed at all.
	labels := map[string]string{}
	for _, r := range doc.Rows {
		labels[r.Label] = r.Status
	}
	for _, label := range []string{"scan(legacy)", "licence(legacy)", "findings(legacy)"} {
		if got, ok := labels[label]; !ok || got != string(ui.StatusUnmeasured) {
			t.Fatalf("rows = %+v, want an unmeasured %s", doc.Rows, label)
		}
	}
	if got, ok := labels["scan"]; !ok || got != string(ui.StatusUnmeasured) {
		t.Fatalf("rows = %+v, want the run to say no check ran", doc.Rows)
	}
}

// A gate that is simply absent from the document reads as one that ran and
// found nothing, so each gate a scanned component would have had says why it
// did not apply — in its own words, because the gates answer different
// questions and an author reading a licence row is not being told about a
// linter.
func TestEachGateThatDoesNotApplySaysSoInItsOwnWords(t *testing.T) {
	rows := unscannedRows("legacy", "")

	labels := make([]string, 0, len(rows))
	values := map[string]string{}
	for _, r := range rows {
		if r.Status != ui.StatusUnmeasured {
			t.Errorf("row %s = %s, want unmeasured: a gate that could not run is never a pass", r.Label, r.Status)
		}
		labels = append(labels, r.Label)
		values[r.Label] = r.Value
	}
	if !slices.Equal(labels, []string{"scan(legacy)", "licence(legacy)", "findings(legacy)"}) {
		t.Fatalf("labels = %v, want the SAST, licence and finding-gate rows", labels)
	}
	if values["scan(legacy)"] == values["licence(legacy)"] || values["licence(legacy)"] == values["findings(legacy)"] {
		t.Errorf("values = %v, want each gate's own reason rather than one sentence three times", values)
	}
	for label, value := range values {
		if !strings.Contains(value, "raw command") {
			t.Errorf("%s = %q, want the declaration an author would change named", label, value)
		}
	}
}

// A raw command states no language at all; a language lydite recognises as
// source and runs no tool over states one nothing checks. The declaration an
// author would change differs between them, so the sentence does too.
func TestTheReasonNothingScansAComponentNamesWhichCaseItIs(t *testing.T) {
	raw := unscannedReason("")
	unsupported := unscannedReason(runner.Python)

	if raw == unsupported {
		t.Fatalf("both reasons = %q, want a raw command told apart from an unscanned language", raw)
	}
	if !strings.Contains(unsupported, string(runner.Python)) {
		t.Errorf("reason = %q, want the language named", unsupported)
	}
}

// A language with a runner and no scanner must not leave through the opt-out
// branch: langEnabled answers false for every language it has no key for, which
// would skip it as silently as a switch the repository never touched.
func TestALanguageWithNoScannerIsNotReadAsAnOptOut(t *testing.T) {
	for _, l := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript, runner.Shell} {
		if !scannedLang(l) {
			t.Errorf("scannedLang(%s) = false, want the languages scan has checks for", l)
		}
	}
	for _, l := range []runner.Lang{"", runner.Python, runner.Lang("cobol")} {
		if scannedLang(l) {
			t.Errorf("scannedLang(%q) = true, want a language with no scanner to render its rows", l)
		}
	}
}

// A declared lang: is the language a component is scanned as, and never the
// one its suite runs in. A command component stating `lang: go` is provisioned
// nothing for its suite, which a runner never derived; a `lang: shell`
// component names a language no toolchain exists for, so it is provisioned
// nothing on either side.
func TestADeclaredLangReachesNoTestUnit(t *testing.T) {
	file := component.File{Components: []component.Component{
		{Name: "tool", Dir: "tool", Command: []string{"make", "test"}, DeclaredLang: runner.Go},
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
	}}

	if got := componentUnits(file.Components); len(got) != 0 {
		t.Fatalf("test units = %+v, want none: neither component has a runner to imply a suite's language", got)
	}

	reqs, err := toolchain.Requirements(t.TempDir(),
		[]toolchain.Unit{{Name: "scripts", Lang: runner.Shell, Dir: "scripts"}}, toolchain.Overrides{})
	if err != nil {
		t.Fatalf("requirements: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("requirements = %+v, want none for a language with no toolchain", reqs)
	}
}

// A component stating only a language lydite has no scanner for is scanned as
// that language: its three rows are unmeasured and say which language, rather
// than blaming a raw command it never declared or leaving through the opt-out
// branch langEnabled would take for a language with no config key.
func TestScanReportsADeclaredLanguageWithNoScannerAsUnmeasured(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: tools\n    dir: .\n    lang: python\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var doc struct {
		Rows []struct{ Status, Label, Value string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	rows := map[string]struct{ status, value string }{}
	for _, r := range doc.Rows {
		rows[r.Label] = struct{ status, value string }{r.Status, r.Value}
	}
	for _, label := range []string{"scan(tools)", "licence(tools)", "findings(tools)"} {
		r, ok := rows[label]
		if !ok || r.status != string(ui.StatusUnmeasured) {
			t.Fatalf("rows = %+v, want an unmeasured %s", doc.Rows, label)
		}
		if !strings.Contains(r.value, string(runner.Python)) || strings.Contains(r.value, "raw command") {
			t.Errorf("%s = %q, want the declared language named, not a raw command", label, r.value)
		}
	}
}

// Shell is off until a repository switches it on, so a `lang: shell` component
// in a repository that has not is no opt-out: each gate it would have had says
// it did not run and names the key that would run it, rather than the
// component vanishing from a report that then reads as a clean scan.
func TestAShellComponentWithShellOffIsUnmeasuredNamingTheKey(t *testing.T) {
	for name, cfgYAML := range map[string]string{
		"unset": "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n",
		"false": "shell:\n  enabled: false\nsemgrep:\n  enabled: false\nsecrets:\n  enabled: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeLydite(t, dir, config.FileName, cfgYAML)
			writeLydite(t, dir, component.FileName,
				"components:\n  - name: scripts\n    dir: .\n    lang: shell\n")
			writeLydite(t, dir, "install.sh", "#!/bin/sh\necho $1\n")

			rows := scanRows(t, dir)
			for _, label := range []string{"scan(scripts)", "licence(scripts)", "findings(scripts)"} {
				r, ok := rows[label]
				if !ok || r.status != string(ui.StatusUnmeasured) {
					t.Fatalf("rows = %+v, want an unmeasured %s", rows, label)
				}
				if !strings.Contains(r.value, "shell.enabled") {
					t.Errorf("%s = %q, want the key that would turn shell on named", label, r.value)
				}
			}
			if _, ok := rows["shellcheck(scripts)"]; ok {
				t.Errorf("rows = %+v, want no ShellCheck row with shell switched off", rows)
			}
		})
	}
}

// Switched on, a `lang: shell` component is scanned by ShellCheck: a script
// carrying a diagnostic fails the row and reaches the document as a located
// claim naming the component, and the licence gate says it has nothing to read
// rather than going missing.
func TestAShellComponentWithShellOnIsScannedByShellCheck(t *testing.T) {
	if !executil.Available("shellcheck") && !executil.Available("pipx") {
		t.Skip("neither shellcheck nor pipx is on PATH to provide the pinned ShellCheck")
	}
	dir := t.TempDir()
	writeLydite(t, dir, config.FileName, "shell:\n  enabled: true\nsemgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: scripts\n    dir: scripts\n    lang: shell\n")
	writeLydite(t, dir, "scripts/install.sh", "#!/bin/sh\necho $1\n")
	gitInit(t, dir)

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	var doc struct {
		Rows     []struct{ Status, Label, Value string } `json:"rows"`
		Findings []finding.Finding                       `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v\n%s", err, out.String())
	}
	rows := map[string]string{}
	for _, r := range doc.Rows {
		rows[r.Label] = r.Status
	}
	if got := rows["shellcheck(scripts)"]; got != string(ui.StatusFail) {
		t.Fatalf("rows = %+v, want a failing shellcheck(scripts) over an unquoted expansion", doc.Rows)
	}
	if got := rows["licence(scripts)"]; got != string(ui.StatusUnmeasured) {
		t.Errorf("rows = %+v, want an unmeasured licence(scripts)", doc.Rows)
	}
	for _, label := range []string{"scan(scripts)", "findings(scripts)"} {
		if _, ok := rows[label]; ok {
			t.Errorf("rows = %+v, want no %s beside the checks that ran", doc.Rows, label)
		}
	}
	var claim *finding.Finding
	for i, f := range doc.Findings {
		if f.Gate == shell.Gate && f.Rule == "SC2086" {
			claim = &doc.Findings[i]
		}
	}
	if claim == nil {
		t.Fatalf("findings = %+v, want SC2086 as a located claim", doc.Findings)
	}
	if claim.Component != "scripts" || claim.Path != "scripts/install.sh" || claim.Line != 2 || claim.Row != "shellcheck(scripts)" {
		t.Errorf("claim = %+v, want scripts/install.sh:2 under shellcheck(scripts)", *claim)
	}
}

// scanRows runs a scan over dir and answers its rows by label.
func scanRows(t *testing.T, dir string) map[string]struct{ status, value string } {
	t.Helper()
	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var doc struct {
		Rows []struct{ Status, Label, Value string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	rows := map[string]struct{ status, value string }{}
	for _, r := range doc.Rows {
		rows[r.Label] = struct{ status, value string }{r.Status, r.Value}
	}
	return rows
}

func writeLydite(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-b", "main", "."}, {"add", "-A"}} {
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
}

// Every opt-out taken together is a different fact from any one of them. A
// language switched off produces no row, deliberately — but a run where that
// is true of every declared component and of every root-scoped gate produced
// no rows at all,
// and an empty document renders as `verdict: pass`: the green of a scan that
// never happened, which is what the status exists to prevent.
func TestAScanThatRanNothingSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, config.FileName, "go:\n  enabled: false\nsemgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var doc struct {
		Rows []struct{ Status, Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	if len(doc.Rows) != 1 || doc.Rows[0].Status != string(ui.StatusUnmeasured) || doc.Rows[0].Label != "scan" {
		t.Fatalf("rows = %+v, want one unmeasured scan row rather than an empty document", doc.Rows)
	}
}

// A component's declared environment reaches the checks, as it reaches its
// suite: a Rust component declaring SQLX_OFFLINE or a Go one declaring
// CGO_ENABLED needs it to build at all, so without it the suite passes under
// `lydite test` and the build fails under `lydite scan`.
func TestScanComposesAComponentsDeclaredEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{"SQLX_OFFLINE": "true"}}

	if got := childEnv(nil, c, runner.Invocation{}); !slices.Contains(got, "SQLX_OFFLINE=true") {
		t.Fatalf("env = %q, want the component's declared variable", got)
	}
}

// component.validate enforces unique names and not unique directories, so two
// components over one root are legitimate — `lydite test` runs both suites.
// Their scanners read the identical tree, so running both spends time to
// report every finding twice under two labels.
func TestTwoComponentsOverOneDirectoryAreScannedOnce(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: .\n    runner: go-test\n"+
			"  - name: api-integration\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	var doc struct {
		Rows []struct{ Status, Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	// One set of checks, run under the first component in declaration order.
	var checks []string
	for _, r := range doc.Rows {
		if r.Status != string(ui.StatusUnmeasured) {
			checks = append(checks, r.Label)
		}
	}
	if !slices.Equal(checks, []string{"gosec(api)", "govulncheck(api)", "licence(api)"}) {
		t.Fatalf("checks = %v, want gosec, govulncheck and licence once each for the shared directory", checks)
	}
	// And the second component says why it has none of its own, rather than
	// disappearing from a report that is otherwise one row per component.
	var deduped string
	for _, r := range doc.Rows {
		if r.Label == "scan(api-integration)" && r.Status == string(ui.StatusUnmeasured) {
			deduped = r.Label
		}
	}
	if deduped == "" {
		t.Fatalf("rows = %+v, want the deduplicated component to say so", doc.Rows)
	}
}

// The rows saying a gate does not apply belong to the component nothing scans
// and to no other. A component in a language lydite does run carries exactly
// the rows its checks produced — a `scan(api)` or `findings(api)` beside them
// would report a gate as inapplicable to a component that gate had just run
// over.
func TestAScannedComponentCarriesNoneOfTheRowsForAComponentNothingScans(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: .\n    runner: go-test\n"+
			"  - name: legacy\n    dir: legacy\n    command: [\"make\", \"check\"]\n")
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeLydite(t, dir, "legacy/Makefile", "check:\n\t@true\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	var doc struct {
		Rows []struct{ Status, Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	var scanned, unscanned []string
	for _, r := range doc.Rows {
		switch {
		case strings.HasSuffix(r.Label, "(api)"):
			scanned = append(scanned, r.Label)
		case strings.HasSuffix(r.Label, "(legacy)"):
			unscanned = append(unscanned, r.Label)
		}
	}
	if !slices.Equal(scanned, []string{"gosec(api)", "govulncheck(api)", "licence(api)"}) {
		t.Errorf("rows for the Go component = %v, want its checks and nothing else", scanned)
	}
	if !slices.Equal(unscanned, []string{"scan(legacy)", "licence(legacy)", "findings(legacy)"}) {
		t.Errorf("rows for the raw-command component = %v, want one per gate that does not apply", unscanned)
	}
}

// A repository may say how its own code builds; it may not say where lydite's
// scanners come from. `go install`, `cargo install` and `npm ci` read GOPROXY,
// GOSUMDB, CARGO_REGISTRIES_* and npm_config_registry, so a declared
// environment reaching them chooses which binary lydite fetches and then runs
// — and the result is cached, so one poisoned build outlives the run and, on a
// runner sharing ~/.cache/lydite, reaches other repositories.
func TestAComponentsEnvironmentReachesTheChecksAndNotTheInstalls(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{
		"GOPROXY":      "http://127.0.0.1:1",
		"SQLX_OFFLINE": "true",
	}}
	tc := &toolchain.Env{Vars: []string{"GOTOOLCHAIN=local"}}

	env := executil.Env{
		Check:   childEnv(tc, c, runner.Invocation{}),
		Install: tc.Environ(),
	}

	// The build the repository declared for its own code.
	if !slices.Contains(env.Check, "SQLX_OFFLINE=true") {
		t.Errorf("check env = %q, want the component's declared variable", env.Check)
	}
	// And nothing of the repository's in what provisions lydite's own tools.
	for _, kv := range env.Install {
		if strings.HasPrefix(kv, "GOPROXY=") || strings.HasPrefix(kv, "SQLX_OFFLINE=") {
			t.Fatalf("install env carries %q from the scanned repository", kv)
		}
	}
	if !slices.Contains(env.Install, "GOTOOLCHAIN=local") {
		t.Errorf("install env = %q, want lydite's own resolved toolchain", env.Install)
	}
}

// Two components over one directory declaring different environments are two
// builds, not one. Dropping either scans that tree with an environment it
// never asked for, and the row would carry the other component's name.
func TestTwoComponentsOverOneDirectoryWithDifferentEnvironmentsBothRun(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n"+
			"  - name: api\n    dir: .\n    runner: go-test\n"+
			"  - name: api-cgo\n    dir: .\n    runner: go-test\n    env:\n      CGO_ENABLED: \"1\"\n")
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	var doc struct {
		Rows []struct{ Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	var sawCGO bool
	for _, r := range doc.Rows {
		if strings.Contains(r.Label, "api-cgo") {
			sawCGO = true
		}
	}
	if !sawCGO {
		t.Fatalf("rows = %+v, want the component declaring its own environment scanned under its own name", doc.Rows)
	}
}

// lydite's own declaration must leave nothing scanned by nobody.
//
// This replaces internal/config's TestPinDirectoriesAreExcluded, which failed
// when a *-pin directory was not excluded from detection. Detection is gone,
// but the obligation moved rather than vanished: every cargo pin must carry a
// src/lib.rs — cargo refuses a [package] manifest without one — which is real
// Rust under the `cli` component that nothing builds. The declaration excludes
// them by glob, and a pin added outside that glob would go unmentioned with
// nothing failing.
//
// It asserts the whole property rather than the pin case, so a future
// directory of any language nothing claims fails here too.
func TestLyditesOwnDeclarationLeavesNothingUnscanned(t *testing.T) {
	// Four levels up: the module is at source/cli/, and the scan root is
	// the repository root where .lydite/ lives.
	const repoRoot = "../../../.."

	file, err := component.Load(repoRoot)
	if err != nil {
		t.Fatalf("loading this repository's own declaration: %v", err)
	}
	gaps, err := orphan.Unscanned(context.Background(), repoRoot, file, nil)
	if err != nil {
		t.Fatalf("checking this repository: %v", err)
	}
	for _, g := range gaps {
		t.Errorf("%d %s file(s) are scanned by no component, e.g. %s — declare one, or exclude them in %s",
			len(g.Files), g.Lang, g.Files[0], component.FileName)
	}
}

// Counting rows is not the test for "did anything run". A raw-command
// component and a deduplicated one each add an unmeasured row, and unmeasured
// does not vote — so a repository whose every component declares a raw
// command, with every root-scoped gate off, would otherwise produce a document full of amber
// rows and `verdict: pass` having executed nothing at all.
func TestARunOfOnlyUnmeasuredRowsIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n"+
			"  - name: legacy\n    dir: .\n    command: [\"make\", \"check\"]\n"+
			"  - name: tools\n    dir: .\n    command: [\"make\", \"tools\"]\n")
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")

	var out bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	var doc struct {
		Rows []struct{ Status, Label string } `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	var sawRunRow bool
	for _, r := range doc.Rows {
		if r.Label == "scan" && r.Status == string(ui.StatusUnmeasured) {
			sawRunRow = true
		}
	}
	if !sawRunRow {
		t.Fatalf("rows = %+v, want the run to say no check ran rather than reporting a pass over amber rows", doc.Rows)
	}
}

// A check's located claims reach the document naming the row that made them,
// which is what lets the standing comment tell the claims it keeps from the
// ones a thread carries — and they reach it exactly as the stage that ran the
// check labelled and anchored them. Anchoring them again here, against lines
// the report was never given, would move every claim on a line of the change
// into the standing comment.
func TestACheckSFindingsReachTheDocumentNamingTheirRow(t *testing.T) {
	rep := ui.NewReport("scan")
	claim := finding.Finding{Gate: "biome", Component: "cli", Path: "a.ts", Line: 3,
		Message: "a finding", Site: "one", Anchor: finding.AnchorLine, Row: "biome(cli)"}
	record(rep, t.TempDir(), []executil.Result{{
		Name: "biome(cli)", Err: errors.New("failed"), Findings: []finding.Finding{claim},
	}}, []finding.Finding{claim})
	found := rep.Findings()
	if len(found) != 1 {
		t.Fatalf("the check's claim did not reach the document: %+v", found)
	}
	if found[0].Row != "biome(cli)" {
		t.Errorf("the claim does not name the row that made it: %q", found[0].Row)
	}
	if found[0].Anchor != finding.AnchorLine {
		t.Errorf("the claim was anchored again: %q, want the stage's %q", found[0].Anchor, finding.AnchorLine)
	}
	if rows := rep.Rows(); len(rows) != 1 || rows[0].Label != "biome(cli)" {
		t.Errorf("the row went missing with it: %+v", rows)
	}
}

// Each language names the gates its checks report findings under, and a
// language lydite runs no check for names none.
//
// The set is what makes a recorded nought mean "this gate found nothing" rather
// than "this gate never looked", so a language gaining a scanner that does not
// reach this list records a permanent nought for a check that is running.
func TestEachLanguageNamesTheGatesThatReportItsFindings(t *testing.T) {
	for _, tc := range []struct {
		lang runner.Lang
		want []string
	}{
		{runner.Go, []string{golang.GateGosec, golang.GateGovulncheck}},
		{runner.Rust, []string{rust.GateClippy, rust.GateAudit, rust.GateDeny}},
		{runner.TypeScript, []string{typescript.GateBiome, typescript.GateLicence}},
		{runner.Shell, []string{shell.Gate}},
	} {
		got := scannerGates(tc.lang)
		for _, gate := range tc.want {
			if !slices.Contains(got, gate) {
				t.Errorf("%s names %v, want it to include %s", tc.lang, got, gate)
			}
		}
	}
	// cargo fmt is excluded on purpose. lydite is not a formatter, so a
	// formatting diff is never a finding — and a gate named here with no
	// parser behind it records a nought on every commit as though it had
	// looked.
	if got := scannerGates(runner.Rust); slices.Contains(got, rust.GateFmt) {
		t.Errorf("rust names %v, want cargo fmt left out: lydite reports no formatting diff as a finding", got)
	}
	// A language lydite checks nothing for. Its components record no count at
	// all, which is the honest answer rather than a nought.
	if got := scannerGates(runner.Lang("cobol")); got != nil {
		t.Errorf("an unchecked language names %v, want nothing", got)
	}
}

// The secret gate runs once over the scan root, its claims name no component,
// and one key in .lydite/config.yml switches the whole of it off.
//
// Root-scoped is the load-bearing half. A claim attributed to whichever
// component happens to contain its path is the ownership question ADR 0033
// refuses to answer, and it is what decides whether the count lands in
// root_findings or inside a component.
//
// The credential is assembled here rather than written, so this file is not
// itself a finding under the repository's own gate.
func TestTheSecretGateIsRootScopedAndSwitchesOffWithOneKey(t *testing.T) {
	// Invented, and assembled from halves: generic-api-key wants a
	// high-entropy value beside a keyword, which is a shape this file must not
	// itself carry.
	invented := "8a7b6c5d4e3f2a1b0c9d" + "8e7f6a5b4c3d2e1f0a9b"
	leaky := "aws_key: \"" + invented + "\"\n"

	scan := func(t *testing.T, cfgYAML string) []byte {
		t.Helper()
		dir := t.TempDir()
		// A raw-command component, so the only thing that can run is the
		// root-scoped gate under test: a language component would pull gosec
		// and govulncheck into a test about neither.
		writeLydite(t, dir, component.FileName,
			"components:\n  - name: legacy\n    dir: .\n    command: [\"make\", \"check\"]\n")
		writeLydite(t, dir, config.FileName, cfgYAML)
		writeLydite(t, dir, "config.yml", leaky)

		var out bytes.Buffer
		cmd := newScanCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"--dir", dir, "--json"})
		_ = cmd.ExecuteContext(context.Background())
		return out.Bytes()
	}

	var doc struct {
		Rows     []struct{ Status, Label string } `json:"rows"`
		Findings []finding.Finding                `json:"findings"`
	}
	if err := json.Unmarshal(scan(t, "semgrep:\n  enabled: false\n"), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	var row string
	for _, r := range doc.Rows {
		if r.Label == secrets.Gate {
			row = r.Status
		}
	}
	if row != string(ui.StatusFail) {
		t.Fatalf("rows = %+v, want a failing %s row over a tree holding a credential", doc.Rows, secrets.Gate)
	}
	var claims int
	for _, f := range doc.Findings {
		if f.Gate != secrets.Gate {
			continue
		}
		claims++
		if f.Component != "" {
			t.Errorf("claim names component %q; a root-scoped gate attributes nothing", f.Component)
		}
		if f.Row != secrets.Gate {
			t.Errorf("claim names row %q, want the row that made it", f.Row)
		}
		if strings.Contains(f.Site, invented) {
			t.Errorf("the site carries the credential: %q", f.Site)
		}
	}
	if claims == 0 {
		t.Errorf("findings = %+v, want the leak as a located claim", doc.Findings)
	}

	// And switched off it runs nothing and reports nothing — the same tree,
	// one key different.
	doc.Rows, doc.Findings = nil, nil
	if err := json.Unmarshal(scan(t, "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n"), &doc); err != nil {
		t.Fatalf("parsing the report: %v", err)
	}
	for _, r := range doc.Rows {
		if r.Label == secrets.Gate {
			t.Errorf("rows = %+v, want no %s row with the gate switched off", doc.Rows, secrets.Gate)
		}
	}
	for _, f := range doc.Findings {
		if f.Gate == secrets.Gate {
			t.Errorf("a gate switched off reported %+v", f)
		}
	}
}

// A root-scoped gate's claims are counted over the repository rather than
// inside a component, which is where ledger.Record.RootFindings holds them.
//
// Semgrep is not the only such gate any more, and a count that landed inside
// whichever component happened to contain the path would answer an ownership
// question the declaration does not.
func TestASecretClaimIsCountedOverTheRepositoryAndNotInAComponent(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}

	perComponent, root := findingCounts(t.TempDir(), decl, config.Default(), []finding.Finding{{
		Gate: secrets.Gate, Path: "cli/config.yml", Line: 3,
		Message: "Detected a Generic API Key. " + "Rotate this credential",
		Site:    "generic-api-key\x1faws_key: ",
	}}, true)

	if got := root[secrets.Gate]; got != 1 {
		t.Errorf("root[%s] = %d, want the claim counted over the repository", secrets.Gate, got)
	}
	if got, ok := perComponent["cli"][secrets.Gate]; ok {
		t.Errorf("perComponent[cli][%s] = %d, want no key: the claim names no component", secrets.Gate, got)
	}
}

// A component scanned as a declared lang: records its language's gate
// noughts like a runner component does, and one whose language is switched off
// or states none records none — a nought there would read as a clean scan of
// source nothing checked.
func TestFindingCountsReadTheLanguageAComponentIsScannedAs(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "tool", Dir: "tool", Command: []string{"make", "test"}, DeclaredLang: runner.Go},
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
		{Name: "legacy", Dir: "legacy", Command: []string{"make", "check"}},
	}}

	perComponent, _ := findingCounts(t.TempDir(), decl, config.Default(), nil, true)

	for _, gate := range golang.FindingGates() {
		if gate == licence.Gate {
			continue
		}
		if got, ok := perComponent["tool"][gate]; !ok || got != 0 {
			t.Errorf("tool[%s] = %d (present %v), want a nought for the Go gate it is scanned by", gate, got, ok)
		}
	}
	for _, name := range []string{"scripts", "legacy"} {
		if got, ok := perComponent[name]; ok {
			t.Errorf("perComponent[%s] = %v, want no entry: no gate scans it", name, got)
		}
	}

	// Switched on, shell records ShellCheck's nought and nothing else: it has
	// no dependency set, so no licence key.
	enabled := config.Default()
	enabled.Shell.Enabled = true
	enabled.Licence.Policy.Allow = []string{"MIT"}
	perComponent, _ = findingCounts(t.TempDir(), decl, enabled, nil, true)
	if got := perComponent["scripts"]; len(got) != 1 || got[shell.Gate] != 0 {
		t.Errorf("perComponent[scripts] = %v, want only a %s nought", got, shell.Gate)
	}
}

// Which document decided a Rust component's licences cannot be read off the
// verdict, and a reader told only that nothing was gated has no way to find the
// file an edit would go in.
func TestPolicySourceNamesTheDocumentThatDecided(t *testing.T) {
	cases := []struct {
		source rust.PolicySource
		want   string
	}{
		{rust.PolicyFromLydite, config.FileName},
		{rust.PolicyFromConsumer, rust.DenyConfigFile},
		{rust.PolicyFromNone, rust.DenyConfigFile},
		// A source this does not recognise names itself rather than reading as
		// one of the three it does.
		{rust.PolicySource("something later"), "something later"},
	}
	for _, c := range cases {
		if got := policySourceSays(c.source); !strings.Contains(got, c.want) {
			t.Errorf("policySourceSays(%q) = %q, want %q named in it", c.source, got, c.want)
		}
	}
	if policySourceSays(rust.PolicyFromConsumer) == policySourceSays(rust.PolicyFromNone) {
		t.Error("a component's own deny.toml and no deny.toml at all read the same, and they are answered by different edits")
	}
}

// Only a gating verdict renders green or red. A policy nobody stated, a run
// given no diff base and a base that could not be built each gate nothing, and
// rendering any of them as `pass` is a gate that never ran reported as one that
// ran and found nothing.
func TestOnlyAGatingLicenceVerdictRendersGreenOrRed(t *testing.T) {
	pairs := []licence.Dependency{{Package: "copyleft", Version: "v1.0.0", Licence: "GPL-3.0-only"}}
	cases := []struct {
		comparison licence.Comparison
		status     ui.Status
		says       string
	}{
		{licence.Comparison{Verdict: licence.VerdictPass}, ui.StatusPass, "passed"},
		{licence.Comparison{Verdict: licence.VerdictFail, Pairs: pairs}, ui.StatusFail, "introduced"},
		{licence.Comparison{Verdict: licence.VerdictUnmeasured, Reason: "checking out deadbee"}, ui.StatusUnmeasured, "deadbee"},
		{licence.Comparison{Verdict: licence.VerdictContext, Pairs: pairs}, ui.StatusContext, "no diff base"},
		{licence.Comparison{Verdict: licence.VerdictNotConfigured}, ui.StatusContext, config.FileName},
	}
	for _, c := range cases {
		row := licenceRow("licence(api)", c.comparison)
		if row.Status != c.status {
			t.Errorf("%q rendered %q, want %q", c.comparison.Verdict, row.Status, c.status)
		}
		if !strings.Contains(row.Value, c.says) {
			t.Errorf("%q reads %q, want %q in it", c.comparison.Verdict, row.Value, c.says)
		}
	}
}

// A row a reader cannot act on names the dependency and the licence rather than
// only a count — and a verdict about no pair at all carries no detail, rather
// than an empty block under it.
func TestTheLicenceDetailNamesEveryPairTheVerdictIsAbout(t *testing.T) {
	if got := licenceDetail(nil); got != nil {
		t.Errorf("detail = %v, want none where the verdict is about no pair", got)
	}
	got := licenceDetail([]licence.Dependency{
		{Package: "copyleft", Version: "v1.0.0", Licence: "GPL-3.0-only"},
		{Package: "unversioned", Licence: "LGPL-3.0"},
	})
	want := []string{"copyleft v1.0.0: GPL-3.0-only", "unversioned: LGPL-3.0"}
	if !slices.Equal(got, want) {
		t.Errorf("detail = %v, want %v", got, want)
	}
}

// A component whose own dependencies could not be enumerated has had nothing
// decided about it, and a red row would ask its author to answer for a claim
// the gate never made. The row says why in its detail and carries no claim, in
// every language with a gate — Rust's included, whose row names no policy
// document because none was read.
func TestALicenceRowIsUnmeasuredWhereTheDependenciesCouldNotBeRead(t *testing.T) {
	for _, lang := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript} {
		t.Run(string(lang), func(t *testing.T) {
			rep := ui.NewReport("scan")
			recordLicence(rep, "api", lang, scanstages.LicenceVerdict{
				Gated:        true,
				Err:          errors.New("the dependency graph would not load"),
				PolicySource: rust.PolicyFromLydite,
				Crashes:      []finding.Crash{{Gate: licence.Gate, Component: "api"}},
			})

			rows := rep.Rows()
			if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured || rows[0].Label != "licence(api)" {
				t.Fatalf("rows = %+v, want one unmeasured licence(api) row", rows)
			}
			if rows[0].Value != "the component's dependencies could not be read" {
				t.Errorf("value = %q, want the row to say the dependencies could not be read", rows[0].Value)
			}
			if !slices.Equal(rows[0].Detail, []string{"the dependency graph would not load"}) {
				t.Errorf("detail = %q, want the reason the component could not be read", rows[0].Detail)
			}
			if len(rep.Findings()) != 0 {
				t.Errorf("claims = %+v, want none from a gate that decided nothing", rep.Findings())
			}
			if got := rep.Crashed(); !slices.Equal(got, []finding.Crash{{Gate: licence.Gate, Component: "api"}}) {
				t.Errorf("crashed = %+v, want the licence bucket named crashed", got)
			}
		})
	}
}

// A Rust component governed by neither document has had no licence check run at
// all, and the row says which document is missing rather than rendering the
// green of a check that ran and found nothing.
func TestTheRustLicenceRowNamesTheDocumentThatDecidedNothing(t *testing.T) {
	rep := ui.NewReport("scan")
	recordLicence(rep, "svc", runner.Rust, scanstages.LicenceVerdict{
		Gated:        true,
		PolicySource: rust.PolicyFromNone,
		Comparison:   licence.Comparison{Verdict: licence.VerdictNotConfigured},
	})

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusContext {
		t.Fatalf("rows = %+v, want one context licence row", rows)
	}
	if !strings.Contains(rows[0].Value, config.FileName) || !strings.Contains(rows[0].Value, rust.DenyConfigFile) {
		t.Fatalf("value = %q, want both files an edit could go in named", rows[0].Value)
	}
}

// Under lydite's own policy a Rust row fails on what the change introduced
// against the merge-base, names the document that decided the licences, and
// carries the claims the verdict is about exactly as the gate located them.
func TestTheRustLicenceRowUnderLyditesPolicyNamesWhatTheChangeIntroduced(t *testing.T) {
	claim := finding.Finding{Gate: licence.Gate, Component: "svc", Path: "svc/Cargo.lock", Line: 12,
		Row: "licence(svc)", Anchor: finding.AnchorLine}
	rep := ui.NewReport("scan")
	recordLicence(rep, "svc", runner.Rust, scanstages.LicenceVerdict{
		Gated:        true,
		PolicySource: rust.PolicyFromLydite,
		Comparison: licence.Comparison{Verdict: licence.VerdictFail,
			Pairs: []licence.Dependency{{Package: "cbindgen", Version: "0.26.0", Licence: "MPL-2.0"}}},
		Findings: []finding.Finding{claim},
	})

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusFail {
		t.Fatalf("rows = %+v, want one failing licence row", rows)
	}
	if rows[0].Label != "licence(svc)" || !strings.Contains(rows[0].Value, "introduced") {
		t.Fatalf("row = %+v, want the component's licence row naming what the change introduced", rows[0])
	}
	if !strings.Contains(rows[0].Value, config.FileName) {
		t.Errorf("value = %q, want the document that decided the licences named on it", rows[0].Value)
	}
	if found := rep.Findings(); len(found) != 1 || found[0].Path != "svc/Cargo.lock" || found[0].Anchor != finding.AnchorLine {
		t.Errorf("claims = %+v, want the one claim as the gate located and anchored it", found)
	}
}

// A component's own deny.toml is evaluated whole and absolutely, so a failing
// row counts every crate it rejected rather than what this change introduced —
// and a passing one still reads as the pass it is, never as a count of nothing.
func TestARustComponentsOwnPolicyCountsEveryCrateItRejected(t *testing.T) {
	cases := []struct {
		name       string
		comparison licence.Comparison
		status     ui.Status
		says       string
	}{
		{"rejected", licence.Comparison{Verdict: licence.VerdictFail,
			Pairs: []licence.Dependency{{Package: "cbindgen", Version: "0.26.0", Licence: "MPL-2.0"}}},
			ui.StatusFail, "1 non-conforming licence(s)"},
		{"allowed", licence.Comparison{Verdict: licence.VerdictPass}, ui.StatusPass, "passed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := ui.NewReport("scan")
			recordLicence(rep, "svc", runner.Rust, scanstages.LicenceVerdict{
				Gated: true, PolicySource: rust.PolicyFromConsumer, Comparison: c.comparison,
			})

			rows := rep.Rows()
			if len(rows) != 1 || rows[0].Status != c.status {
				t.Fatalf("rows = %+v, want one %q licence row", rows, c.status)
			}
			if !strings.Contains(rows[0].Value, c.says) {
				t.Errorf("value = %q, want %q in it", rows[0].Value, c.says)
			}
			if strings.Contains(rows[0].Value, "merge-base") {
				t.Errorf("value = %q, want no delta against a base: the component's own policy carries none", rows[0].Value)
			}
			if !strings.Contains(rows[0].Value, rust.DenyConfigFile) {
				t.Errorf("value = %q, want the document that decided named on it", rows[0].Value)
			}
		})
	}
}

// Only a Rust row names a policy document, because only Rust has more than one
// to be decided by. A Go or TypeScript row is the comparison's own rendering,
// with the claims a failing verdict is about beneath it in the order the gate
// gave them.
func TestAGoOrTypeScriptLicenceRowIsTheComparisonsOwn(t *testing.T) {
	comparison := licence.Comparison{Verdict: licence.VerdictFail, Pairs: []licence.Dependency{
		{Package: "copyleft", Version: "v1.0.0", Licence: "GPL-3.0-only"},
		{Package: "weak", Version: "v2.0.0", Licence: "MPL-2.0"},
	}}
	claims := []finding.Finding{
		{Gate: licence.Gate, Component: "api", Path: "api/go.mod", Line: 7, Row: "licence(api)", Message: "copyleft", Site: "copyleft"},
		{Gate: licence.Gate, Component: "api", Path: "api/go.mod", Line: 9, Row: "licence(api)", Message: "weak", Site: "weak"},
	}
	for _, lang := range []runner.Lang{runner.Go, runner.TypeScript} {
		t.Run(string(lang), func(t *testing.T) {
			rep := ui.NewReport("scan")
			recordLicence(rep, "api", lang, scanstages.LicenceVerdict{
				Gated: true, Comparison: comparison, Findings: claims,
			})

			want := licenceRow("licence(api)", comparison)
			rows := rep.Rows()
			if len(rows) != 1 || rows[0].Status != want.Status || rows[0].Value != want.Value || !slices.Equal(rows[0].Detail, want.Detail) {
				t.Fatalf("rows = %+v, want the comparison's own row %+v", rows, want)
			}
			var messages []string
			for _, f := range rep.Findings() {
				messages = append(messages, f.Message)
			}
			if !slices.Equal(messages, []string{"copyleft", "weak"}) {
				t.Errorf("claims = %v, want both, in the order the gate gave them", messages)
			}
		})
	}
}

// Shell declares no dependency set, so a scanned shell component's licence gate
// has nothing to read — and says so in a row of its own, because a licence row
// absent from a scanned component reads as a gate that ran and found nothing.
func TestAShellComponentsLicenceRowSaysThereIsNothingToRead(t *testing.T) {
	rep := ui.NewReport("scan")
	recordLicence(rep, "scripts", runner.Shell, scanstages.LicenceVerdict{})

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured || rows[0].Label != "licence(scripts)" {
		t.Fatalf("rows = %+v, want one unmeasured licence(scripts) row", rows)
	}
	if want := "not measured — shell declares no dependency set to read licences from"; rows[0].Value != want {
		t.Errorf("value = %q, want %q", rows[0].Value, want)
	}
	if len(rep.Findings()) != 0 || len(rep.Crashed()) != 0 {
		t.Errorf("claims = %+v, crashed = %+v, want neither from a gate that does not exist", rep.Findings(), rep.Crashed())
	}
}

// The composition the warning describes is childEnv's, so it is childEnv's
// environment the scan hands its stages, and the declaration they name is
// split exactly as childEnv splits it: a reordering that let a declared
// GOTOOLCHAIN win would otherwise still be reported as cancelled.
func TestDeclaredEnvReadsTheEnvironmentChildEnvComposed(t *testing.T) {
	c := component.Component{Name: "cli", Env: map[string]string{"GOTOOLCHAIN": "auto", "SQLX_OFFLINE": "true", "PATH": "ci-bin"}}
	tc := &toolchain.Env{Vars: []string{"GOTOOLCHAIN=local"}}
	var environment scanEnvironment

	composed := environment.Compose(tc, c)
	if want := childEnv(tc, c, runner.Invocation{}); !slices.Equal(composed, want) {
		t.Fatalf("composed = %q, want childEnv's %q", composed, want)
	}
	effective := map[string]string{}
	for _, kv := range composed {
		k, v, _ := strings.Cut(kv, "=")
		effective[k] = v
	}
	if effective["GOTOOLCHAIN"] != "local" || effective["SQLX_OFFLINE"] != "true" {
		t.Fatalf("composed = %q, want the resolved toolchain's GOTOOLCHAIN to win and the declared SQLX_OFFLINE to reach the check", composed)
	}

	dirs, vars := environment.Declared(c)
	if !slices.Equal(dirs, []string{"ci-bin"}) {
		t.Errorf("declared PATH = %q, want the directory it extends the path with", dirs)
	}
	if want := []string{"GOTOOLCHAIN=auto", "SQLX_OFFLINE=true"}; !slices.Equal(vars, want) {
		t.Errorf("declared variables = %q, want %q", vars, want)
	}
}

// warnDeclaredEnv is only useful if the scan calls it with the stream it
// reserves for warnings and with the environment it actually composed — a unit
// test over the function proves neither. The PATH is stripped and the
// toolchain disabled so no tool is found and every check fails at once, which
// is after the warning either way.
func TestScanWarnsAboutADeclaredEnvironmentOnItsStderr(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n    env:\n      GOFLAGS: \"-tags secretvalue\"\n")
	writeLydite(t, dir, config.FileName,
		"toolchain:\n  enabled: false\nsemgrep:\n  enabled: false\nsecrets:\n  enabled: false\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")

	var out, errOut bytes.Buffer
	cmd := newScanCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--dir", dir, "--json"})
	_ = cmd.ExecuteContext(context.Background())

	if !strings.Contains(errOut.String(), "GOFLAGS") {
		t.Fatalf("stderr = %q, want the declared environment named on the stream warnings go to", errOut.String())
	}
	if strings.Contains(errOut.String()+out.String(), "secretvalue") {
		t.Fatalf("a declared value reached the scan's output:\nstderr: %s\nstdout: %s", errOut.String(), out.String())
	}
}
