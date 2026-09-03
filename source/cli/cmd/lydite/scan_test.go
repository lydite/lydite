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
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// The "auto" path needs a real repo with an origin/main to resolve against, so
// it's left to the integration surface; what's worth pinning here is the two
// branches that decide whether git is consulted at all.
func TestResolveDiffBase(t *testing.T) {
	cases := []struct {
		name     string
		diffBase string
		appToken string
		want     string
	}{
		{"unset means scan everything", "", "", ""},
		{"literal ref passes through", "origin/release", "", "origin/release"},
		{"a token short-circuits auto — semgrep ci scopes itself", "auto", "tok", ""},
		{"a token short-circuits a literal ref too", "origin/release", "tok", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(semgrep.AppTokenEnv, tc.appToken)
			got, err := resolveDiffBase(context.Background(), t.TempDir(), tc.diffBase, "")
			if err != nil {
				t.Fatalf("resolveDiffBase(%q) returned %v", tc.diffBase, err)
			}
			if got != tc.want {
				t.Errorf("resolveDiffBase(%q) = %q, want %q", tc.diffBase, got, tc.want)
			}
		})
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
	err := report(cmd, ui.NewReport("scan"), []executil.Result{
		{
			Name:   "biome(.)",
			Detail: "src/bad.ts:1  lint/security/noGlobalEval  eval() is dangerous\nsrc/bad.ts:4  lint/correctness/noUnusedVariables  unused",
			Err:    errors.New("2 finding(s)"),
		},
	}, false, true)
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
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "src/bad.ts:1  noGlobalEval", Err: errors.New("1 finding(s)")},
	}, true, false)
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
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "✓ biome(.) ... passed", Err: errors.New("1 finding(s)")},
	}, false, true)
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
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "should not appear"},
		{Name: "semgrep", Output: "already streamed to the terminal", Err: errors.New("findings")},
	}, false, true)
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
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\n")
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
	// The component's own row, and — because nothing else ran either — the
	// run's. Both are unmeasured: one says this component has no language,
	// the other says no check executed at all.
	labels := map[string]string{}
	for _, r := range doc.Rows {
		labels[r.Label] = r.Status
	}
	if got, ok := labels["scan(legacy)"]; !ok || got != string(ui.StatusUnmeasured) {
		t.Fatalf("rows = %+v, want an unmeasured scan(legacy)", doc.Rows)
	}
	if got, ok := labels["scan"]; !ok || got != string(ui.StatusUnmeasured) {
		t.Fatalf("rows = %+v, want the run to say no check ran", doc.Rows)
	}
}

// A language switched off in .lydite/config.yml produces no rows and no
// toolchain: provisioning one would download a compiler nothing invokes.
func TestDisabledLanguageProducesNoUnitsAndNoRows(t *testing.T) {
	file := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: runner.GoTest},
		{Name: "legacy", Dir: "."},
	}}
	cfg := config.Default()
	cfg.Go.Enabled = false

	if units := scanUnits(file, cfg); len(units) != 0 {
		t.Fatalf("units = %+v, want none when the only language is disabled", units)
	}
	cfg.Go.Enabled = true
	units := scanUnits(file, cfg)
	if len(units) != 1 || units[0].Name != "cli" || units[0].Lang != runner.Go {
		t.Fatalf("units = %+v, want just the Go component", units)
	}
}

// The name and never the directory: unique names are enforced and unique
// directories are not, and a scan row and a test row about one component have
// to carry the same token.
func TestLabelledNamesTheComponent(t *testing.T) {
	got := labelled([]executil.Result{{Name: "gosec"}, {Name: "govulncheck"}}, "api")
	if len(got) != 2 || got[0].Name != "gosec(api)" || got[1].Name != "govulncheck(api)" {
		t.Fatalf("labelled = %+v, want each result named for the component", got)
	}
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

// The hole the orphan gate cannot close. A component rooted at `.` covers
// every path in the repository, so a Go component at the root leaves a
// TypeScript directory beside it orphaning nothing while no TypeScript check
// ever runs — and the orphan gate belongs to `lydite test`, which a consumer
// can run scan without. Silence there would be a scan that narrowed itself.
func TestScanWarnsAboutALanguageNoComponentDeclares(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "main.go", "package main\n")
	writeLydite(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := warnUnscanned(context.Background(), &w, dir, file, config.Default())

	if len(got) != 1 || got[0].Lang != runner.TypeScript {
		t.Fatalf("gaps = %+v, want TypeScript alone — Go is declared and covers main.go", got)
	}
	if !strings.Contains(w.String(), component.FileName) {
		t.Errorf("warning = %q, want it to name the file that fixes it", w.String())
	}
}

// A language switched off is an answer, not an oversight: the repository said
// it wants no check over that code, and warning about it would be lydite
// arguing with a decision it was told about.
func TestScanIsSilentAboutADisabledLanguage(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)

	cfg := config.Default()
	cfg.TypeScript.Enabled = false

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, cfg); len(got) != 0 {
		t.Fatalf("found = %v, want nothing when the language is switched off", got)
	}
}

// Outside a git repository there is no file list and therefore no question to
// answer, which is the shape the orphan gate already has for the same case.
func TestScanSaysNothingOutsideAGitRepository(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "web/app.ts", "export const x = 1;\n")

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("found = %v, want nothing outside a repository", got)
	}
	if w.Len() != 0 {
		t.Errorf("warning = %q, want silence", w.String())
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

// The wardnet shape. Two Go modules, one declared: the language is covered, so
// a check keyed on languages alone says nothing while the second module is
// scanned by no one. Detection used to find both, so this is exactly the
// silent narrowing the declaration must not introduce.
func TestScanWarnsAboutAModuleNoComponentCovers(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: wctl\n    dir: wctl\n    runner: go-test\n")
	writeLydite(t, dir, "wctl/go.mod", "module wctl\n\ngo 1.26\n")
	writeLydite(t, dir, "wctl/main.go", "package main\n")
	writeLydite(t, dir, "sdk/wardnet-go/go.mod", "module sdk\n\ngo 1.26\n")
	writeLydite(t, dir, "sdk/wardnet-go/client.go", "package sdk\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := warnUnscanned(context.Background(), &w, dir, file, config.Default())

	if len(got) != 1 || got[0].Lang != runner.Go {
		t.Fatalf("gaps = %+v, want the undeclared Go module reported", got)
	}
	if !slices.Equal(got[0].Files, []string{"sdk/wardnet-go/client.go"}) {
		t.Fatalf("files = %v, want only the module no component covers", got[0].Files)
	}
	if !strings.Contains(w.String(), "sdk/wardnet-go/client.go") {
		t.Errorf("warning = %q, want it to name an example file", w.String())
	}
}

// An exclude is the repository's reviewable statement that a path is claimed
// by no component, which is the same statement this warning asks for. It does
// not narrow what gets scanned — nothing scans these files either way.
func TestAnExcludeSilencesTheUnscannedWarning(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n"+
			"excludes: [\"vendor-fixtures/**\"]\n")
	writeLydite(t, dir, "main.go", "package main\n")
	writeLydite(t, dir, "vendor-fixtures/src/lib.rs", "pub fn x() {}\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want none when the path is excluded", got)
	}
}

// Containment is not enough for Go, and this is the case that proves it. A
// nested go.mod starts a separate module that the enclosing module's package
// graph excludes, so `./...` at the root never compiles it and neither gosec
// nor govulncheck sees it — while a component rooted at `.` contains every
// path in the repository. Verified against the tools before it was encoded:
// the same G306 in both modules is reported once.
func TestScanWarnsAboutANestedModuleUnderARootComponent(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeLydite(t, dir, "sdk/go.mod", "module sdk\n\ngo 1.26\n")
	writeLydite(t, dir, "sdk/client.go", "package sdk\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := warnUnscanned(context.Background(), &w, dir, file, config.Default())

	if len(got) != 1 || !slices.Equal(got[0].Files, []string{"sdk/client.go"}) {
		t.Fatalf("gaps = %+v, want only the nested module's file — main.go is in the component's own module", got)
	}
}

// The same shape with one module: everything is in the component's module, so
// there is nothing to say. Without this the rule above could report every Go
// file in a perfectly ordinary repository.
func TestOneModuleUnderARootComponentIsSilent(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeLydite(t, dir, "internal/svc/svc.go", "package svc\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want silence in a single-module repository", got)
	}
}

// A go.mod under testdata/ is a fixture, not a project — the go command
// ignores those directories when resolving packages, so the enclosing module
// does not scan them and neither does anything else. Treating one as a module
// boundary would warn about an ordinary Go repository layout on every run.
func TestATestdataModuleIsNotAModuleBoundary(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeLydite(t, dir, "testdata/broken/go.mod", "module broken\n\ngo 1.26\n")
	writeLydite(t, dir, "testdata/broken/x.go", "package broken\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want silence: a testdata module is a fixture", got)
	}
}

// Every opt-out taken together is a different fact from any one of them. A
// language switched off produces no row, deliberately — but a run where that
// is true of every declared component and of Semgrep produced no rows at all,
// and an empty document renders as `verdict: pass`: the green of a scan that
// never happened, which is what the status exists to prevent.
func TestAScanThatRanNothingSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, config.FileName, "go:\n  enabled: false\nsemgrep:\n  enabled: false\n")
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

// A component declaring a raw command has a component declared for it, and
// scan already reports it unmeasured with the reason. Warning as well would
// tell its author to declare what they have declared, and the only way to
// silence it would be an exclude that also drops those files from the orphan
// gate.
func TestARawCommandComponentsSourceIsNotWarnedAbout(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: legacy\n    dir: legacy\n    command: [\"make\", \"check\"]\n")
	writeLydite(t, dir, "legacy/main.go", "package main\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want none: scan already reports scan(legacy) unmeasured", got)
	}
}

// The .js family is the extension of build output, configuration and tooling
// glue in every ecosystem: a Go repository with a docs/theme.js is an ordinary
// Go repository, not one with unscanned TypeScript in it. The orphan gate is
// silent about that file — a component rooted at `.` claims it — so warning
// would fire on ordinary work with an exclude as the only way to stop it.
func TestAStrayJavaScriptFileIsNotAnUnscannedCodebase(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeLydite(t, dir, "docs/theme.js", "module.exports = {};\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want silence for a stray .js", got)
	}

	// A .ts beside it is a different claim, and still reported.
	writeLydite(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)
	file, err = component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := warnUnscanned(context.Background(), &w, dir, file, config.Default())
	if len(got) != 1 || !slices.Equal(got[0].Files, []string{"web/app.ts"}) {
		t.Fatalf("gaps = %+v, want the .ts alone", got)
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
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\n")
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
	if !slices.Equal(checks, []string{"gosec(api)", "govulncheck(api)"}) {
		t.Fatalf("checks = %v, want gosec and govulncheck once each for the shared directory", checks)
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
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\n")
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

// A Go component declared at a subdirectory of a single-module repository is
// scanned exactly as it should be — `gosec ./...` run there is inside that
// module — so asking whether its directory *held* a go.mod would report its
// own files as scanned by nobody, naming a component already declared.
func TestAComponentInsideASingleModuleRepositoryIsNotAGap(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n  - name: api\n    dir: services/api\n    runner: go-test\n")
	writeLydite(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	writeLydite(t, dir, "services/api/main.go", "package main\n\nfunc main() {}\n")
	gitInit(t, dir)

	var w bytes.Buffer
	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := warnUnscanned(context.Background(), &w, dir, file, config.Default()); len(got) != 0 {
		t.Fatalf("gaps = %+v, want none: the component's files are in the module gosec runs over", got)
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
// command, with Semgrep off, would otherwise produce a document full of amber
// rows and `verdict: pass` having executed nothing at all.
func TestARunOfOnlyUnmeasuredRowsIsNotAPass(t *testing.T) {
	dir := t.TempDir()
	writeLydite(t, dir, component.FileName,
		"components:\n"+
			"  - name: legacy\n    dir: .\n    command: [\"make\", \"check\"]\n"+
			"  - name: tools\n    dir: .\n    command: [\"make\", \"tools\"]\n")
	writeLydite(t, dir, config.FileName, "semgrep:\n  enabled: false\n")

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
