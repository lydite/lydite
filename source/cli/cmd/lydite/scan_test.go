package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

// The "auto" path needs an origin to resolve against, so it is left to the
// integration surface; what is pinned here is that nothing reaches git as the
// caller wrote it.
//
// A SEMGREP_APP_TOKEN does not short-circuit this. The base has a second
// reader — a finding's anchor, which decides whether a claim becomes a review
// thread — and a token says only that `semgrep ci` scopes itself.
func TestResolveDiffBase(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), repo, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	run("init", "-b", "main", ".")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "one")
	head := strings.TrimSpace(executil.RunQuiet(context.Background(), repo, "git", "rev-parse", "HEAD").Output)

	t.Run("unset means scan everything", func(t *testing.T) {
		got, err := resolveDiffBase(context.Background(), repo, "", "")
		if err != nil || got != "" {
			t.Errorf("resolveDiffBase(\"\") = %q, %v, want \"\", nil", got, err)
		}
	})

	t.Run("a ref resolves to the commit it names", func(t *testing.T) {
		got, err := resolveDiffBase(context.Background(), repo, "main", "")
		if err != nil {
			t.Fatalf("resolveDiffBase(\"main\") returned %v", err)
		}
		if got != head {
			t.Errorf("resolveDiffBase(\"main\") = %q, want the SHA %q — a tool must be given the commit, not the caller's string", got, head)
		}
	})

	t.Run("a token does not take the base away — the anchor reads it too", func(t *testing.T) {
		t.Setenv(semgrep.AppTokenEnv, "tok")
		got, err := resolveDiffBase(context.Background(), repo, "main", "")
		if err != nil || got != head {
			t.Errorf("resolveDiffBase(\"main\") = %q, %v, want the SHA", got, err)
		}
	})

	// The anchor hands this base to `git diff <base>..HEAD`, where a value
	// beginning with `-` is a position git parses as an option:
	// `--diff-base --output=/tmp/x` would make git write the diff to a path of
	// the caller's choosing.
	t.Run("an option-shaped base is refused rather than handed to git", func(t *testing.T) {
		for _, base := range []string{"--output=/tmp/pwned", "-x", "--upload-pack=touch /tmp/pwned"} {
			got, err := resolveDiffBase(context.Background(), repo, base, "")
			if err == nil {
				t.Errorf("resolveDiffBase(%q) = %q, want an error", base, got)
			}
			// No base alongside the error. A caller that reads the value
			// before the error must scan everything rather than diff against
			// a string git refused.
			if got != "" {
				t.Errorf("resolveDiffBase(%q) = %q beside its error, want no base", base, got)
			}
		}
	})

	t.Run("a ref that names no commit is refused", func(t *testing.T) {
		got, err := resolveDiffBase(context.Background(), repo, "no/such/ref", "")
		if err == nil {
			t.Error("resolveDiffBase accepted a ref that names no commit")
		}
		if got != "" {
			t.Errorf("resolveDiffBase = %q beside its error, want no base", got)
		}
	})
}

// `semgrep ci` derives its own diff base from the CI environment, so passing
// --baseline-commit on top of that is redundant. The rule is Semgrep's alone,
// and lives where Semgrep is invoked rather than where the base is resolved.
func TestSemgrepBase(t *testing.T) {
	cases := []struct {
		name, appToken, base, want string
	}{
		{"no token: Semgrep gets the base", "", "origin/release", "origin/release"},
		{"a token: semgrep ci scopes itself", "tok", "origin/release", ""},
		{"no base to give", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(semgrep.AppTokenEnv, tc.appToken)
			if got := semgrepBase(tc.base); got != tc.want {
				t.Errorf("semgrepBase(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

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
	err := report(cmd, ui.NewReport("scan"), t.TempDir(), nil, []executil.Result{
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
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), nil, []executil.Result{
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
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), nil, []executil.Result{
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
	_ = report(cmd, ui.NewReport("scan"), t.TempDir(), nil, []executil.Result{
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
	got := labelled([]executil.Result{{Name: "gosec"}, {Name: "govulncheck"}}, "api", "services/api")
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
// ones a thread carries.
func TestACheckSFindingsReachTheDocumentNamingTheirRow(t *testing.T) {
	rep := ui.NewReport("scan")
	record(rep, t.TempDir(), nil, []executil.Result{{
		Name: "biome(cli)", Err: errors.New("failed"),
		Findings: []finding.Finding{{Gate: "biome", Component: "cli", Path: "a.ts", Line: 3,
			Message: "a finding", Site: "one", Anchor: finding.AnchorLine}},
	}})
	found := rep.Findings()
	if len(found) != 1 {
		t.Fatalf("the check's claim did not reach the document: %+v", found)
	}
	if found[0].Row != "biome(cli)" {
		t.Errorf("the claim does not name the row that made it: %q", found[0].Row)
	}
	if rows := rep.Rows(); len(rows) != 1 || rows[0].Label != "biome(cli)" {
		t.Errorf("the row went missing with it: %+v", rows)
	}
}

// A component's checks report paths relative to the component, and every
// other producer names a file from the scan root: one file named from two
// roots is two claims, and only one of them can be anchored.
func TestAComponentSFindingsAreRebasedOntoTheScanRoot(t *testing.T) {
	got := labelled([]executil.Result{{
		Name:     "biome",
		Findings: []finding.Finding{{Gate: "biome", Path: "src/a.ts", Line: 3}},
	}}, "web", "source/web")
	if got[0].Findings[0].Path != "source/web/src/a.ts" {
		t.Errorf("the path was not rebased: %q", got[0].Findings[0].Path)
	}
	if got[0].Findings[0].Component != "web" {
		t.Errorf("the claim does not name its component: %q", got[0].Findings[0].Component)
	}
}

// A scanner's claim on a line the change touched is a review thread on that
// line. Without an anchor every claim lydite makes about a repository's
// security lands in the standing comment, whatever the change did.
func TestAScanSClaimOnAChangedLineIsAnchoredToIt(t *testing.T) {
	rep := ui.NewReport("scan")
	changed := map[string][]int{"source/cli/a.go": {3, 4}}
	record(rep, t.TempDir(), changed, labelled([]executil.Result{{
		Name: "gosec", Err: errors.New("failed"),
		Findings: []finding.Finding{
			{Gate: "gosec", Path: "a.go", Line: 3, Message: "on a changed line", Site: "one"},
			{Gate: "gosec", Path: "a.go", Line: 40, Message: "elsewhere in a changed file", Site: "two"},
			{Gate: "gosec", Path: "b.go", Line: 1, Message: "in a file the change never touched", Site: "three"},
		},
	}}, "cli", "source/cli"))

	got := map[string]finding.Anchor{}
	for _, f := range rep.Findings() {
		got[f.Message] = f.Anchor
	}
	want := map[string]finding.Anchor{
		"on a changed line":                  finding.AnchorLine,
		"elsewhere in a changed file":        finding.AnchorFile,
		"in a file the change never touched": finding.AnchorNowhere,
	}
	for message, wantAnchor := range want {
		if got[message] != wantAnchor {
			t.Errorf("%q anchored %q, want %q", message, got[message], wantAnchor)
		}
	}
}

// A scan with no --diff-base reaches no change at all, so every claim it makes
// belongs in the standing comment rather than on a line of somebody's pull
// request. It is the shape `lydite-baseline.yml` runs on main.
func TestAScanOverAWholeRepositoryAnchorsNothing(t *testing.T) {
	rep := ui.NewReport("scan")
	record(rep, t.TempDir(), nil, []executil.Result{{
		Name: "gosec(cli)", Err: errors.New("failed"),
		Findings: []finding.Finding{{Gate: "gosec", Path: "a.go", Line: 3, Message: "a claim", Site: "one"}},
	}})
	for _, f := range rep.Findings() {
		if f.Anchor != finding.AnchorNowhere {
			t.Errorf("anchor = %q, want the claim unanchorable", f.Anchor)
		}
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

// licenceConfig is a configuration stating a policy the way a repository does:
// permissive enough to pass the probe's dually licensed module and to reject both of its
// copyleft ones.
func licenceConfig() config.Config {
	cfg := config.Default()
	cfg.Licence.Policy.Allow = []string{"Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MIT"}
	return cfg
}

// goProbeFiles is the captured Go probe module, as the files one commit is made
// of, rooted at prefix. Its dependency set covers strong copyleft, weak
// copyleft and a module carrying two licence files.
func goProbeFiles(t *testing.T, prefix string) map[string]string {
	t.Helper()
	tree := fixture.Tree(t, filepath.Join("..", "..", "internal", "licence", "testdata", "goprobe"))
	entries, err := os.ReadDir(tree)
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(tree, e.Name()))
		if err != nil {
			t.Fatalf("reading the probe: %v", err)
		}
		out[path.Join(prefix, e.Name())] = string(data)
	}
	return out
}

// The base a Go component is compared against is the set its own build
// compiles at the merge-base, read in the checked-out tree rather than from a
// stored figure: an entry written by a lydite that computed no licences reads
// back as the empty set, and the delta on the day of the upgrade is then the
// absolute set.
func TestTheGoLicenceBaseReadsTheModuleAtTheMergeBase(t *testing.T) {
	files := goProbeFiles(t, "api")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := goLicenceBase(context.Background(), tree, "api", nil, licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	want := []string{"github.com/hashicorp/go-version", "github.com/juju/errors"}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, want) {
		t.Fatalf("base set = %v, want %v — the probe's copyleft modules, read under the stated policy", got, want)
	}
}

// The gate's whole shape for a Go component: a module the merge-base did not
// carry is a set every pair of which the change introduced, the row fails, and
// each claim is anchored to what the change touched — without which every claim
// reaches the review surface at no anchor at all.
func TestTheGoLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/README.md": "the component this change adds a module to\n"},
		goProbeFiles(t, "api"))
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	c := component.Component{Name: "api", Dir: "api"}
	// Line 7 of the probe's manifest is the require naming github.com/juju/errors.
	changed := map[string][]int{"api/go.mod": {7}}
	recordGoLicence(context.Background(), &rep, tree, c, filepath.Join(root, "api"), nil, licenceConfig(), changed)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusFail {
		t.Fatalf("rows = %+v, want one failing licence row", rows)
	}
	if rows[0].Label != "licence(api)" || !strings.Contains(rows[0].Value, "introduced") {
		t.Fatalf("row = %+v, want the component's licence row naming what the change introduced", rows[0])
	}
	found := rep.Findings()
	if len(found) != 2 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	var anchors []finding.Anchor
	for _, f := range found {
		if f.Path != "api/go.mod" {
			t.Errorf("claim located at %q, want the component's manifest from the scan root", f.Path)
		}
		anchors = append(anchors, f.Anchor)
	}
	if !slices.Contains(anchors, finding.AnchorLine) {
		t.Fatalf("anchors = %v, want the claim on the line the change touched anchored to it", anchors)
	}
	if slices.Contains(anchors, finding.AnchorNowhere) {
		t.Fatalf("anchors = %v, want every claim in a file the change touched anchored to it at least", anchors)
	}
}

// A component whose own dependencies could not be enumerated has had nothing
// decided about it, and a red row would ask its author to answer for a claim
// the gate never made.
func TestTheGoLicenceRowIsUnmeasuredWhereTheDependenciesCouldNotBeRead(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/README.md": "no module here\n"},
		map[string]string{"api/README.md": "no module here either\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	recordGoLicence(context.Background(), &rep, tree, component.Component{Name: "api", Dir: "api"},
		filepath.Join(root, "api"), nil, licenceConfig(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured {
		t.Fatalf("rows = %+v, want one unmeasured licence row", rows)
	}
	if len(rep.Findings()) != 0 {
		t.Fatalf("claims = %+v, want none from a gate that decided nothing", rep.Findings())
	}
}

// A Rust component governed by neither document has had no licence check run at
// all, and the row says which document is missing rather than rendering the
// green of a check that ran and found nothing.
func TestTheRustLicenceRowNamesTheDocumentThatDecidedNothing(t *testing.T) {
	var rep ui.Report
	dir := t.TempDir()
	recordRustLicence(context.Background(), &rep, newLicenceBaseTree(dir, ""),
		component.Component{Name: "svc", Dir: "."}, dir, executil.Env{}, config.Default(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusContext {
		t.Fatalf("rows = %+v, want one context licence row", rows)
	}
	if !strings.Contains(rows[0].Value, config.FileName) || !strings.Contains(rows[0].Value, rust.DenyConfigFile) {
		t.Fatalf("value = %q, want both files an edit could go in named", rows[0].Value)
	}
}

// cargo-deny not being reachable is a gate that could not run, which is amber
// and names what failed — never the pass of a component whose crates nothing
// read.
func TestTheRustLicenceRowIsUnmeasuredWhereCargoDenyCouldNotRun(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"svc/Cargo.lock": "version = 4\n"},
		map[string]string{"svc/Cargo.lock": "version = 4\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())
	// A cache directory that cannot even be named, so nothing is installed and
	// nothing on the machine is run.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	var rep ui.Report
	recordRustLicence(context.Background(), &rep, tree, component.Component{Name: "svc", Dir: "svc"},
		filepath.Join(root, "svc"), executil.Env{}, licenceConfig(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured {
		t.Fatalf("rows = %+v, want one unmeasured licence row", rows)
	}
	if len(rows[0].Detail) == 0 {
		t.Fatalf("row = %+v, want the reason the component could not be read", rows[0])
	}
}

// denyProbeLock is the captured Rust probe's lockfile, as the one file a
// component's licence gate locates its claims in.
func denyProbeLock(t *testing.T) string {
	t.Helper()
	tree := fixture.Tree(t, filepath.Join("..", "..", "internal", "licence", "testdata", "denyprobe"))
	data, err := os.ReadFile(filepath.Join(tree, "Cargo.lock"))
	if err != nil {
		t.Fatalf("reading the probe's lockfile: %v", err)
	}
	return string(data)
}

// cargoDenyStub puts a captured cargo-deny run in the version-keyed tool cache,
// so the gate runs a real invocation against a real stream with nothing
// installed and nothing fetched. capture names the stream and the file holding
// the status it exited with.
func cargoDenyStub(t *testing.T, capture string) {
	t.Helper()
	home := t.TempDir()
	// Both, because os.UserCacheDir reads XDG_CACHE_HOME on Linux and
	// $HOME/Library/Caches on macOS.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	pin, err := os.ReadFile(filepath.Join("..", "..", "internal", "rust", "cargo-deny-pin", "Cargo.toml"))
	if err != nil {
		t.Fatalf("reading the pin: %v", err)
	}
	bin, err := (cargotool.Tool{Name: "cargo-deny", Version: cargotool.MustPinnedVersion(pin, "cargo-deny")}).Binary()
	if err != nil {
		t.Fatalf("locating the cached binary: %v", err)
	}
	dir := filepath.Dir(bin)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}
	stream, err := os.ReadFile(filepath.Join("..", "..", "internal", "licence", "testdata", capture))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream"), stream, 0o600); err != nil {
		t.Fatalf("writing the stream: %v", err)
	}
	status, err := os.ReadFile(filepath.Join("..", "..", "internal", "licence", "testdata",
		strings.TrimSuffix(capture, filepath.Ext(capture))+".exit"))
	if err != nil {
		t.Fatalf("reading the fixture's exit status: %v", err)
	}
	// cargo-deny writes its NDJSON to stderr, and exits non-zero on a check it
	// failed — both of which the gate reads.
	script := "#!/bin/sh\ncat \"$(dirname \"$0\")/stream\" >&2\nexit " + strings.TrimSpace(string(status)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a stub the test is about to execute
		t.Fatalf("writing the stub: %v", err)
	}
}

// The gate's whole shape for a Rust component: a lockfile the merge-base did
// not carry is a set every crate of which the change introduced, the row fails
// and names the document that decided it, and each claim is anchored to what
// the change touched — without which every claim reaches the review surface at
// no anchor at all.
func TestTheRustLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"svc/README.md": "the component this change adds a lockfile to\n"},
		map[string]string{"svc/Cargo.lock": denyProbeLock(t)})
	cargoDenyStub(t, "deny-licenses-rejected.ndjson")
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	// Line 12 of the probe's lockfile is the stanza naming cbindgen, the crate
	// the captured run rejected.
	changed := map[string][]int{"svc/Cargo.lock": {12}}
	recordRustLicence(context.Background(), &rep, tree, component.Component{Name: "svc", Dir: "svc"},
		filepath.Join(root, "svc"), executil.Env{}, licenceConfig(), changed)

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
	found := rep.Findings()
	if len(found) != 1 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	if found[0].Path != "svc/Cargo.lock" {
		t.Errorf("claim located at %q, want the component's lockfile from the scan root", found[0].Path)
	}
	if found[0].Anchor != finding.AnchorLine {
		t.Errorf("anchor = %q, want %q — the claim is on the line the change touched", found[0].Anchor, finding.AnchorLine)
	}
}

// A component's own deny.toml is evaluated whole and absolutely, so a failing
// row counts every crate it rejected rather than what this change introduced —
// and a passing one still reads as the pass it is, never as a count of nothing.
func TestARustComponentsOwnPolicyCountsEveryCrateItRejected(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		status  ui.Status
		says    string
	}{
		{"rejected", "deny-licenses-rejected.ndjson", ui.StatusFail, "1 non-conforming licence(s)"},
		{"allowed", "deny-licenses-allowed.ndjson", ui.StatusPass, "passed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := fixture.Tree(t, filepath.Join("..", "..", "internal", "licence", "testdata", "denyprobe"))
			// A configuration of the component's own, which is what makes the
			// component rather than lydite the document that decided.
			writeLydite(t, dir, rust.DenyConfigFile, "[licenses]\nallow = [\"MIT\"]\n")
			cargoDenyStub(t, c.capture)

			var rep ui.Report
			recordRustLicence(context.Background(), &rep, newLicenceBaseTree(dir, ""),
				component.Component{Name: "svc", Dir: "."}, dir, executil.Env{}, config.Default(), nil)

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

// tsProbeFiles is a captured TypeScript probe tree, as the files one commit is
// made of, rooted at prefix.
func tsProbeFiles(t *testing.T, probe, prefix string) map[string]string {
	t.Helper()
	tree := fixture.Tree(t, filepath.Join("..", "..", "internal", "typescript", "testdata", probe))
	entries, err := os.ReadDir(tree)
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(tree, e.Name()))
		if err != nil {
			t.Fatalf("reading the probe: %v", err)
		}
		out[path.Join(prefix, e.Name())] = string(data)
	}
	return out
}

// The gate's whole shape for a TypeScript component: a lockfile the merge-base
// did not carry is a set every dependency of which the change introduced, the
// row fails, and each claim is anchored to what the change touched — without
// which every claim reaches the review surface at no anchor at all.
func TestTheTypeScriptLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/README.md": "the component this change adds a manifest to\n"},
		tsProbeFiles(t, "npmprobe", "web"))
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	// Line 10 of the probe's manifest is the dependency naming lightningcss,
	// which the stated policy rejects under MPL-2.0.
	changed := map[string][]int{"web/package.json": {10}}
	recordTypeScriptLicence(context.Background(), &rep, tree, component.Component{Name: "web", Dir: "web"},
		filepath.Join(root, "web"), licenceConfig(), changed)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusFail {
		t.Fatalf("rows = %+v, want one failing licence row", rows)
	}
	if rows[0].Label != "licence(web)" || !strings.Contains(rows[0].Value, "introduced") {
		t.Fatalf("row = %+v, want the component's licence row naming what the change introduced", rows[0])
	}
	found := rep.Findings()
	// lightningcss, the two sharp-libvips builds and the entry stating no
	// licence at all: every dependency of the probe the allow-list rejects.
	if len(found) != 4 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	var direct, transitive int
	for _, f := range found {
		if f.Path != "web/package.json" {
			t.Errorf("claim located at %q, want the component's manifest from the scan root", f.Path)
		}
		if f.Line > 0 {
			direct++
			if f.Anchor != finding.AnchorLine {
				t.Errorf("%q anchored %q, want %q — it is on the manifest line the change touched", f.Message, f.Anchor, finding.AnchorLine)
			}
			continue
		}
		transitive++
		// A package the manifest names on no line reaches the change nowhere,
		// however much of that manifest the change edited: an anchor here would
		// put a transitive dependency's claim on a line whose edit does nothing
		// about it.
		if f.Anchor != finding.AnchorNowhere {
			t.Errorf("%q anchored %q, want %q — it is reached only transitively", f.Message, f.Anchor, finding.AnchorNowhere)
		}
	}
	if direct != 1 || transitive != 3 {
		t.Fatalf("claims = %d direct and %d transitive, want lightningcss located and the other three not", direct, transitive)
	}
}

// A dependency the merge-base already carried is not this change's to answer
// for, however many of them the allow-list rejects: the row passes and makes no
// claim at all.
func TestTheTypeScriptLicenceGatePassesWhereTheBaseCarriedTheSameLockfile(t *testing.T) {
	files := tsProbeFiles(t, "npmprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	recordTypeScriptLicence(context.Background(), &rep, tree, component.Component{Name: "web", Dir: "web"},
		filepath.Join(root, "web"), licenceConfig(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusPass {
		t.Fatalf("rows = %+v, want one passing licence row", rows)
	}
	if len(rep.Findings()) != 0 {
		t.Fatalf("claims = %+v, want none: the base carried every pair", rep.Findings())
	}
}

// yarn states no dependency licence in its lockfile and scan runs no install to
// produce one, so a component with no installed tree beside it has had nothing
// decided about it. Amber naming what was missing, never the green of a gate
// whose dependencies nothing read.
func TestTheTypeScriptLicenceRowIsUnmeasuredWhereNoLicenceSourceExists(t *testing.T) {
	files := tsProbeFiles(t, "yarnprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	recordTypeScriptLicence(context.Background(), &rep, tree, component.Component{Name: "web", Dir: "web"},
		filepath.Join(root, "web"), licenceConfig(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusUnmeasured {
		t.Fatalf("rows = %+v, want one unmeasured licence row", rows)
	}
	if len(rows[0].Detail) == 0 {
		t.Fatalf("row = %+v, want the reason the component could not be read", rows[0])
	}
	if len(rep.Findings()) != 0 {
		t.Fatalf("claims = %+v, want none from a gate that decided nothing", rep.Findings())
	}
}

// A repository that stated no policy gets the row that names the file an edit
// would go in — not the amber of a manager whose licences could not be read,
// which is an answer to a question nobody asked here.
func TestTheTypeScriptLicenceRowIsNotConfiguredWithoutAPolicy(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("..", "..", "internal", "typescript", "testdata", "yarnprobe"))

	var rep ui.Report
	recordTypeScriptLicence(context.Background(), &rep, newLicenceBaseTree(dir, ""),
		component.Component{Name: "web", Dir: "."}, dir, config.Default(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusContext {
		t.Fatalf("rows = %+v, want one context licence row", rows)
	}
	if !strings.Contains(rows[0].Value, config.FileName) {
		t.Fatalf("value = %q, want the file a policy is stated in named on it", rows[0].Value)
	}
}

// The base a TypeScript component is compared against is the set its own
// lockfile resolved at the merge-base, read in the checked-out tree rather than
// from a stored figure: an entry written by a lydite that computed no licences
// reads back as the empty set, and the delta on the day of the upgrade is then
// the absolute set.
func TestTheTypeScriptLicenceBaseReadsTheLockfileAtTheMergeBase(t *testing.T) {
	files := tsProbeFiles(t, "npmprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := typescriptLicenceBase(context.Background(), tree, "web", licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	want := []string{"@img/sharp-libvips-darwin-arm64", "@img/sharp-libvips-linux-x64", "lightningcss", "unlicensed-probe"}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, want) {
		t.Fatalf("base set = %v, want %v — the probe's rejected dependencies, read under the stated policy", got, want)
	}
}

// What has to be at the base for the component to have been there is its
// manifest, and not the one lockfile that states licences.
//
// A yarn component declares no package-lock.json in any tree, so gating on one
// would make every base read as a component the change adds — a measured empty
// set, against which every dependency the repository already shipped is
// introduced, and a red row on a change that touched none of them.
func TestTheTypeScriptLicenceBaseIsTheManifestAndNotTheNpmLockfile(t *testing.T) {
	files := tsProbeFiles(t, "yarnprobe", "web")
	// An installed tree, the only licence source a yarn component has, and one
	// both sides of the comparison carry.
	files["web/node_modules/lightningcss/package.json"] = `{"name":"lightningcss","version":"1.33.0","license":"MPL-2.0"}`
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	var rep ui.Report
	recordTypeScriptLicence(context.Background(), &rep, tree, component.Component{Name: "web", Dir: "web"},
		filepath.Join(root, "web"), licenceConfig(), nil)

	rows := rep.Rows()
	if len(rows) != 1 || rows[0].Status != ui.StatusPass {
		t.Fatalf("rows = %+v, want one passing licence row: the base carried the same installed tree", rows)
	}
}

// A component with no manifest at the merge-base is one this change adds, which
// is a measured empty set and never an unmeasured base: nothing failed, there
// was nothing there.
func TestTheTypeScriptLicenceBaseIsEmptyWhereTheComponentWasNotThere(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/README.md": "the component this change adds a manifest to\n"},
		tsProbeFiles(t, "npmprobe", "web"))
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := typescriptLicenceBase(context.Background(), tree, "web", licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured || base.Set().Len() != 0 {
		t.Fatalf("base = %q holding %d, want a measured empty set for a component the base did not carry", base.State(), base.Set().Len())
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

// The reason a base could not be built reaches the caller, and the tree inside
// it never does: a directory answered beside a reason would be read as the
// checkout that did not happen, and every component measured against it.
func TestABaseWorktreeThatWouldNotCheckOutAnswersNoTree(t *testing.T) {
	root, _ := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, strings.Repeat("0123456789", 4))
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason == "" {
		t.Fatal("a merge-base that does not exist checked out")
	}
	if dir != "" {
		t.Fatalf("dir = %q, want none beside a reason", dir)
	}
	// Decided once: the second component asks the same question and is answered
	// from what the first attempt recorded.
	if again, sameReason := tree.open(context.Background()); again != "" || sameReason != reason {
		t.Fatalf("second open = %q/%q, want the first attempt's answer", again, sameReason)
	}
}

// The checkout succeeding answers the scan root inside the worktree, which is
// what every component's base is located under.
func TestABaseWorktreeAnswersTheScanRootInsideIt(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason != "" {
		t.Fatalf("checking out the merge-base: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "api", depsManifest)); err != nil {
		t.Fatalf("the base tree holds no manifest at %s: %v", dir, err)
	}
}

// A worktree with nowhere to be created answers no tree either, and names the
// step that failed rather than the checkout that was never reached: a directory
// answered beside a reason is read as the checkout that did not happen, and
// every component is measured against it.
func TestABaseWorktreeWithNoTemporaryDirectoryAnswersNoTree(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	// A file where the temporary directory belongs, so nothing can be created
	// under it.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)

	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason == "" {
		t.Fatal("a worktree with nowhere to live was created")
	}
	if dir != "" {
		t.Fatalf("dir = %q, want none beside a reason", dir)
	}
	if !strings.Contains(reason, "no temporary directory") {
		t.Errorf("reason = %q, want the step that failed", reason)
	}
	if strings.Contains(reason, shortSHA(baseSHA)) {
		t.Errorf("reason = %q, want it distinct from a merge-base that would not check out", reason)
	}
}

// A worktree that cannot be created is a base nothing can be measured against,
// and it says so rather than falling through to a measured empty set that fails
// every component over dependencies it already shipped.
func TestABaseWorktreeThatCannotBeCreatedIsUnmeasured(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	// A file where the temporary directory belongs, so nothing can be created
	// under it.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)

	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())
	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Unmeasured {
		t.Fatalf("base state = %q, want unmeasured", base.State())
	}
	if !strings.Contains(base.Reason(), "temporary directory") {
		t.Errorf("reason = %q, want the step that failed", base.Reason())
	}
}

// licenceBaseRepo is a repository with two commits: the tree the merge-base
// holds, then the tree the change made of it. It answers the repository root
// and the merge-base SHA.
func licenceBaseRepo(t *testing.T, base, head map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(context.Background(), root, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
		return strings.TrimSpace(r.Output)
	}
	git("init", "-b", "main", ".")
	commit := func(files map[string]string) string {
		for name, body := range files {
			writeLydite(t, root, name, body)
		}
		git("add", "-A")
		// --allow-empty, because a change that alters no manifest is a tree the
		// gate has to answer for too: it is the case that must pass.
		git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "tree")
		return git("rev-parse", "HEAD")
	}
	baseSHA := commit(base)
	commit(head)
	return root, baseSHA
}

// readDeps stands in for a language's own reader: a manifest whose lines are
// `<package> <licence>`, so what a tree's non-conforming set is can be stated
// rather than measured by a toolchain. What is under test is which tree the
// reader was pointed at, which is the same question whatever reads it.
func readDeps(dir string) (licence.Set, error) {
	body, err := os.ReadFile(filepath.Join(dir, depsManifest))
	if err != nil {
		return licence.Set{}, err
	}
	var set licence.Set
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		set.Add(licence.Dependency{Package: fields[0], Licence: fields[1]})
	}
	return set, nil
}

// depsManifest is the file readDeps reads, and the file whose absence at the
// base means the component was not there.
const depsManifest = "deps"

// worktrees is how many working trees the repository has registered, which is
// one — its own — until a base is checked out.
func worktrees(t *testing.T, root string) int {
	t.Helper()
	r := executil.RunQuiet(context.Background(), root, "git", "worktree", "list", "--porcelain")
	if !r.Ok() {
		t.Fatalf("git worktree list: %v\n%s", r.Err, r.Stderr)
	}
	return strings.Count(r.Output, "worktree ")
}

// permissivePolicy is a stated policy, which is all Compare asks of one: the
// language reader has already rejected what the set carries.
func permissivePolicy() licence.Policy { return licence.NewPolicy([]string{"MIT"}) }

// packagesOf names what a verdict is about, in the order the comparison put
// them.
func packagesOf(pairs []licence.Dependency) []string {
	out := make([]string, 0, len(pairs))
	for _, d := range pairs {
		out = append(out, d.Package)
	}
	return out
}

// The whole of what the gate is: the set at the merge-base, not the set at the
// head. A base pointed at the wrong tree reads as no manifest at all, which is
// a measured empty set — and every dependency the repository already shipped
// then reads as one this change introduced.
func TestTheLicenceGateFailsOnThePairTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, []string{"golden"}) {
		t.Fatalf("base set = %v, want the one pair the merge-base carried", got)
	}

	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	got := licence.Compare(permissivePolicy(), current, base)
	if got.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want fail — the change added a pair the merge-base did not carry", got.Verdict)
	}
	if names := packagesOf(got.Pairs); !slices.Equal(names, []string{"copyleft"}) {
		t.Fatalf("introduced = %v, want copyleft alone — golden was grandfathered", names)
	}
}

// The other half of the same gate. A pair already at the merge-base is one the
// change did not introduce, however non-conforming it is.
func TestTheLicenceGatePassesAChangeThatIntroducesNoPair(t *testing.T) {
	deps := map[string]string{"api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"}
	root, baseSHA := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	got := licence.Compare(permissivePolicy(), current, base)
	if got.Verdict != licence.VerdictPass {
		t.Fatalf("verdict = %q (%s), want pass — both pairs were already at the merge-base", got.Verdict, got.Reason)
	}
}

// A component this change adds has no manifest at the base, which is a measured
// empty set and not an unmeasured base: nothing failed, there was nothing
// there. Reporting `unmeasured` would take the gate off the one change that
// brings a whole dependency set with it.
func TestAComponentAbsentFromTheMergeBaseMeasuresAnEmptySet(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/app.ts": "export const x = 1;\n"},
		map[string]string{"api/" + depsManifest: "copyleft GPL-3.0-only\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured: the component was not there", base.State(), base.Reason())
	}
	if got := base.Set().Len(); got != 0 {
		t.Fatalf("base holds %d pair(s), want none", got)
	}
	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if got := licence.Compare(permissivePolicy(), current, base); got.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want fail — every pair a new component carries is one the change introduces", got.Verdict)
	}
}

// A worktree holds the whole repository and the scan root may sit below it. A
// base located from the worktree root instead finds no manifest, calls that an
// empty set, and every dependency the component already had reads as new.
func TestTheLicenceBaseLocatesAComponentThroughTheScanRootPrefix(t *testing.T) {
	repo, baseSHA := licenceBaseRepo(t,
		map[string]string{"source/api/" + depsManifest: "golden MIT\n"},
		map[string]string{"source/api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"})
	root := filepath.Join(repo, "source")
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured under the scan root's prefix", base.State(), base.Reason())
	}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, []string{"golden"}) {
		t.Fatalf("base set = %v, want the pair the merge-base carried below source/", got)
	}
}

// One commit, one checkout. A worktree per component extracts the identical
// merge-base once per declaration, and nothing in a passing report says it
// happened.
func TestOneWorktreeServesEveryComponentsLicenceBase(t *testing.T) {
	deps := map[string]string{
		"api/" + depsManifest: "golden MIT\n",
		"web/" + depsManifest: "silver MIT\n",
	}
	root, baseSHA := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d before any component asked for a base, want the repository's own alone", got)
	}
	for _, dir := range []string{"api", "web"} {
		if base := tree.set(context.Background(), dir, depsManifest, readDeps); base.State() != licence.Measured {
			t.Fatalf("%s base state = %q (%s), want it measured", dir, base.State(), base.Reason())
		}
	}
	if got := worktrees(t, root); got != 2 {
		t.Fatalf("worktrees = %d, want one shared base beside the repository's own", got)
	}

	tree.close(context.Background())
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d after the scan, want the base removed — a registered worktree pointing at nothing trips every later `git worktree add`", got)
	}
}

// A scan whose components ask for no base pays for no checkout: no diff base at
// all is the shape `lydite scan` on `main` has, and it gates nothing.
func TestARunWithNoDiffBaseChecksOutNoWorktree(t *testing.T) {
	root, _ := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, "")
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.NoBase {
		t.Fatalf("base state = %q, want no base: the run was given no diff base", base.State())
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d, want no base checked out for a run that compares against nothing", got)
	}
}

// A base that could not be built gates nothing and says so — on every
// component's row, with the same reason, because the checkout it names was
// attempted once. Falling through to a measured empty set would fail each of
// them over dependencies nobody in the change chose.
func TestABaseThatWillNotCheckOutIsUnmeasuredForEveryComponent(t *testing.T) {
	deps := map[string]string{
		"api/" + depsManifest: "golden MIT\n",
		"web/" + depsManifest: "silver MIT\n",
	}
	root, _ := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, strings.Repeat("0123456789", 4))
	defer tree.close(context.Background())

	var reasons []string
	for _, dir := range []string{"api", "web"} {
		base := tree.set(context.Background(), dir, depsManifest, readDeps)
		if base.State() != licence.Unmeasured {
			t.Fatalf("%s base state = %q, want unmeasured: the merge-base would not check out", dir, base.State())
		}
		if base.Reason() == "" {
			t.Fatalf("%s base names no reason, want the step that failed", dir)
		}
		reasons = append(reasons, base.Reason())
	}
	if reasons[0] != reasons[1] {
		t.Fatalf("reasons = %q, want one answer decided once for the whole scan", reasons)
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d, want no registered base after a checkout that failed", got)
	}
}

// The names reported are the ones the child actually reads, so the two cases
// where the composition disagrees with the declaration are pinned here: a
// declared PATH is folded into lydite's own entry rather than set, and a key
// the resolved toolchain also sets is cancelled by composing last. A component
// that composed nothing says nothing at all — a warning for every component is
// a warning nobody reads.
func TestDeclaredEnvNamesWhatWasComposed(t *testing.T) {
	cases := []struct {
		name     string
		c        component.Component
		composed []string
		want     []string
	}{
		{
			name:     "no declaration at all",
			c:        component.Component{Name: "cli"},
			composed: []string{"PATH=/usr/bin"},
		},
		{
			name:     "an empty declaration is no declaration",
			c:        component.Component{Name: "cli", Env: map[string]string{}},
			composed: []string{"PATH=/usr/bin"},
		},
		{
			name:     "a declared variable is named",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOVULNDB": "https://db.example", "GOFLAGS": "-tags x"}},
			composed: []string{"GOFLAGS=-tags x", "GOVULNDB=https://db.example"},
			want:     []string{"GOFLAGS", "GOVULNDB"},
		},
		{
			name:     "an empty value is still a declaration",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOFLAGS": ""}},
			composed: []string{"GOFLAGS="},
			want:     []string{"GOFLAGS"},
		},
		{
			name:     "a declared PATH is the extension it is",
			c:        component.Component{Name: "cli", Env: map[string]string{"PATH": "ci-bin"}},
			composed: []string{"PATH=/usr/bin" + string(os.PathListSeparator) + "ci-bin"},
			want:     []string{"PATH (appended after lydite's own)"},
		},
		{
			name:     "a key the toolchain composes last never reached the check",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOTOOLCHAIN": "auto"}},
			composed: []string{"GOTOOLCHAIN=auto", "GOTOOLCHAIN=local"},
			want:     []string{"GOTOOLCHAIN (overridden by the resolved toolchain)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := declaredEnvNames(tc.c, tc.composed); !slices.Equal(got, tc.want) {
				t.Errorf("declaredEnvNames = %q, want %q", got, tc.want)
			}
			var buf bytes.Buffer
			warnDeclaredEnv(&buf, tc.c, tc.composed)
			if (buf.Len() == 0) != (len(tc.want) == 0) {
				t.Errorf("warnDeclaredEnv wrote %q for %d composed name(s)", buf.String(), len(tc.want))
			}
		})
	}
}

// The composition the warning describes is childEnv's, so it is childEnv that
// produces the environment here rather than a hand-written slice: a reordering
// that let a declared GOTOOLCHAIN win would otherwise still be reported as
// cancelled.
func TestDeclaredEnvReadsTheEnvironmentChildEnvComposed(t *testing.T) {
	c := component.Component{Name: "cli", Env: map[string]string{"GOTOOLCHAIN": "auto", "SQLX_OFFLINE": "true", "PATH": "ci-bin"}}
	tc := &toolchain.Env{Vars: []string{"GOTOOLCHAIN=local"}}
	got := declaredEnvNames(c, childEnv(tc, c, runner.Invocation{}))
	want := []string{"GOTOOLCHAIN (overridden by the resolved toolchain)", "SQLX_OFFLINE", "PATH (appended after lydite's own)"}
	if !slices.Equal(got, want) {
		t.Fatalf("declaredEnvNames = %q, want %q", got, want)
	}
}

// Names, never values. A declared value is arbitrary text the repository
// controls, and this line goes to a CI log that is world-readable on a public
// repository — so the value of a variable, and the directories of a declared
// PATH, must never appear however the names are assembled.
func TestDeclaredEnvValuesNeverReachTheWarning(t *testing.T) {
	const secret = "ghp_examplesecretvaluenobodyshouldsee"
	c := component.Component{Name: "cli", Env: map[string]string{
		"NPM_TOKEN":    secret,
		"DATABASE_URL": "postgres://user:" + secret + "@db/app",
		"PATH":         "/opt/" + secret + "/bin",
	}}
	var buf bytes.Buffer
	warnDeclaredEnv(&buf, c, childEnv(&toolchain.Env{}, c, runner.Invocation{}))
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("a declared value reached the warning:\n%s", buf.String())
	}
	for _, want := range []string{"NPM_TOKEN", "DATABASE_URL", "PATH (appended after lydite's own)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("warning is missing %q:\n%s", want, buf.String())
		}
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
