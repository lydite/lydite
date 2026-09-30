package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	teststages "lydite/lydite/internal/stages/test"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// The plain variant is the fast path, and the only one `lydite test` wants.
func TestInvocationIsThePlainVariant(t *testing.T) {
	inv, err := invocation(component.Component{Runner: runner.GoTest, Args: []string{"-race", "./..."}}, runner.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(append([]string{inv.Name}, inv.Args...), " "); got != "go test -race ./..." {
		t.Errorf("invocation = %q", got)
	}
	if inv.CoverageReport != "" {
		t.Error("the plain variant must not claim a coverage report")
	}
}

// A command is run as written: it opts out of the derived variants, so
// nothing here may add to it.
func TestInvocationRunsACommandAsWritten(t *testing.T) {
	inv, err := invocation(component.Component{Command: []string{"make", "test"}}, runner.Plain)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Name != "make" || len(inv.Args) != 1 || inv.Args[0] != "test" {
		t.Errorf("invocation = %q %v", inv.Name, inv.Args)
	}
}

// A map's iteration order is not one a failure can be reproduced from.
func TestEnvIsSorted(t *testing.T) {
	got := env(component.Component{Env: map[string]string{"B": "2", "A": "1", "C": "3"}})
	want := []string{"A=1", "B=2", "C=3"}
	if len(got) != len(want) {
		t.Fatalf("env = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env = %v, want %v", got, want)
		}
	}
}

func TestTestRunsADeclaredComponent(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
`)
	out, err := runTestCmd(t, root)
	if err != nil {
		t.Fatalf("test failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "test(fixture)") || !strings.Contains(out, "passed") {
		t.Errorf("report = %q, want a passing row for the component", out)
	}
}

func TestTestFailsWhenAComponentsSuiteFails(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
`)
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	out, err := runTestCmd(t, root)
	var exit ui.ExitError
	if err == nil || !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("want exit 1, got %v\n%s", err, out)
	}
}

// --component selects, and naming one that does not exist is an error rather
// than a run of nothing that reports a pass.
func TestComponentFlagSelects(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
  - name: other
    dir: mod
    runner: go-test
`)
	out, err := runTestCmd(t, root, "--component", "other")
	if err != nil {
		t.Fatalf("test failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "test(fixture)") {
		t.Errorf("report = %q, want only the selected component", out)
	}
	if _, err := runTestCmd(t, root, "--component", "ghost"); err == nil {
		t.Error("selecting an undeclared component must be an error")
	}
}

// --json promises stdout carries a document and nothing else, including on
// the path where a repository has declared no components at all.
//
// The tree is not a git repository, so the orphan gate reports unmeasured and
// the verdict below is about the empty declaration alone. In a real
// repository declaring nothing, every source file is an orphan and the run
// fails — which is the gate working, not a contradiction of this test.
func TestNoComponentsIsReportedThroughTheReport(t *testing.T) {
	out, err := runTestCmd(t, t.TempDir(), "--json")
	if err != nil {
		t.Fatalf("test failed: %v\n%s", err, out)
	}
	var doc struct {
		Verdict string `json:"verdict"`
		Rows    []struct {
			Status string `json:"status"`
			Value  string `json:"value"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	if doc.Verdict != "pass" {
		t.Errorf("verdict = %q, want a pass: an empty declaration is not itself a failure", doc.Verdict)
	}
	var declared bool
	for _, r := range doc.Rows {
		if strings.Contains(r.Value, "no components declared") {
			declared = true
			if r.Status != string(ui.StatusUnmeasured) {
				t.Errorf("status = %q, want %q", r.Status, ui.StatusUnmeasured)
			}
		}
	}
	if !declared {
		t.Errorf("rows = %+v, want one saying no components are declared", doc.Rows)
	}
}

// A tree that is not a git repository leaves the orphan gate with no way to
// know which files the repository contains, and it says so rather than
// passing. A gate that did not run must never read as one that did.
func TestOrphanGateOutsideAGitRepositoryIsUnmeasured(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components:\n  - name: fixture\n    dir: .\n    runner: go-test\n")
	out, err := runTestCmd(t, root, "--json")
	if err == nil {
		t.Log("the fixture component itself may pass or fail; only the orphan row matters here")
	}
	row := jsonRowByLabel(t, out, "orphans")
	if row.Status != string(ui.StatusUnmeasured) {
		t.Errorf("status = %q, want %q — an unrunnable gate is not a passing one", row.Status, ui.StatusUnmeasured)
	}
	if !strings.Contains(row.Value, "git") {
		t.Errorf("value = %q, want it to name the missing repository", row.Value)
	}
}

// runTestCmd returns stdout alone, which is the report and — under --json —
// the document.
//
// Separate buffers, and that is the point rather than tidiness. Merging them
// made every assertion here pass on output that mixes the two, so a diagnostic
// landing on stdout would be invisible to the tests whose whole subject is the
// document. `lydite test` legitimately writes to stderr: toolchain
// provisioning notes, unused-exclude warnings, and every coverage warning.
func runTestCmd(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	out, _, err := runTestCmdStreams(t, root, extra...)
	return out, err
}

// runTestCmdStreams is runTestCmd with stderr as well, for a test whose subject
// is a diagnostic rather than the report.
func runTestCmdStreams(t *testing.T, root string, extra ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"test", "--dir", root, "--no-color"}, extra...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// runRecordCmd lands what a `lydite test` run in root left in its report
// directory, which is the step that writes to the lydite branch.
//
// Two commands, because `lydite test` writes nothing there: measuring runs the
// repository's suites and recording holds a token that can push, and the
// tests exercise the same split a workflow does.
func runRecordCmd(t *testing.T, root string, extra ...string) (string, string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{
		"test", "record", "--dir", root, "--no-color",
		"--reports", filepath.Join(root, runner.ReportDir),
	}, extra...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// fixtureRepo is a scan root holding one buildable Go module with a passing
// test, plus the declaration handed in.
func fixtureRepo(t *testing.T, declaration string) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, component.FileName, declaration)
	write(t, root, "mod/go.mod", "module fixture\n\ngo 1.26\n")
	write(t, root, "mod/fixture.go", "package fixture\n\n// Foo is what the fixture's test exercises.\nfunc Foo() int { return 1 }\n")
	write(t, root, "mod/fixture_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\tif Foo() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	return root
}

// The shape .github/assert-proving-ground.py reads. That script is the only
// thing standing between the orphan gate silently breaking and a green
// proving-ground job, and it matches on the row's value prefix and on detail
// carrying bare paths — so a rewording of either is a contract change, and
// this is where it is caught rather than in another repository's CI.
func TestOrphanRowCarriesTheCountAndThePaths(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".lydite/components.yml": "components:\n  - name: cli\n    dir: cli\n    runner: go-test\n",
		"cli/main.go":            "package main\n",
		"scripts/seed.ts":        "export const s = 1\n",
	})
	out, err := runTestCmd(t, root, "--json", "--component", "cli")
	if err == nil {
		t.Error("an orphan must fail the run")
	}
	row := jsonRowByLabel(t, out, "orphans")
	if row.Status != string(ui.StatusFail) {
		t.Errorf("status = %q, want %q", row.Status, ui.StatusFail)
	}
	if !strings.HasPrefix(row.Value, "1 ") {
		t.Errorf("value = %q, want it to start with the orphan count", row.Value)
	}
	var named bool
	for _, d := range row.Detail {
		if d == "scripts/seed.ts" {
			named = true
		}
	}
	if !named {
		t.Errorf("detail = %v, want a bare path naming the orphan", row.Detail)
	}
}

// An exclude clears an orphan, and the run passes. The other half of the
// same contract: a gate that can only fail is one nobody can satisfy.
func TestAnExcludeClearsAnOrphan(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".lydite/components.yml": "components:\n  - name: cli\n    dir: cli\n    runner: go-test\nexcludes: [\"scripts/**\"]\n",
		"cli/main.go":            "package main\n",
		"scripts/seed.ts":        "export const s = 1\n",
	})
	out, _ := runTestCmd(t, root, "--json", "--component", "cli")
	row := jsonRowByLabel(t, out, "orphans")
	if row.Status != string(ui.StatusPass) {
		t.Errorf("status = %q, want %q — the exclude covers the only orphan", row.Status, ui.StatusPass)
	}
}

// gitRepo writes the files and initialises a repository, because the orphan
// gate reads the file list from git.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		write(t, root, rel, body)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "-A"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// jsonRowByLabel finds one row in the report document, so a test asserts on
// the row it is about rather than on the whole report's shape — which every
// gate added to the command would otherwise change.
func jsonRowByLabel(t *testing.T, out, label string) struct {
	Status string   `json:"status"`
	Label  string   `json:"label"`
	Value  string   `json:"value"`
	Detail []string `json:"detail"`
} {
	t.Helper()
	type row = struct {
		Status string   `json:"status"`
		Label  string   `json:"label"`
		Value  string   `json:"value"`
		Detail []string `json:"detail"`
	}
	var doc struct {
		Rows []row `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	for _, r := range doc.Rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", label, doc.Rows)
	return row{}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An install that resolved no workspace root ran nothing at all, and a report
// that mentioned it only by staying silent is indistinguishable from one whose
// workspace was installed.
// Both shapes lydite installs for: the component that named a JavaScript
// runner, and the one that named a raw command from a directory holding a
// package.json.
func TestAComponentWhoseInstallResolvedNoRootIsUnmeasured(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    component.Component
	}{
		{name: "runner", c: nodeComponent()},
		{name: "command", c: nodeCommandComponent()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureRepo(t, "components: []\n")
			write(t, root, "web/package.json", `{"name":"web"}`)
			rep := ui.NewReport("test")
			runSuites(context.Background(), root, []component.Component{tc.c}, config.Default(), 1, rep)

			note := rowByLabel(t, rep, "install(web)")
			if note.Status != ui.StatusUnmeasured || note.Value != "not installed" {
				t.Errorf("row = %+v, want an unmeasured install rather than the silence of one that ran", note)
			}
			if !strings.Contains(strings.Join(note.Detail, " "), "lockfile") {
				t.Errorf("detail = %v, want what was looked for and not found", note.Detail)
			}
			// The suite runs anyway: a component whose dependencies are in place
			// by some other means passes, and the row claims only that lydite did
			// not put them there.
			if suite := rowByLabel(t, rep, "test(web)"); suite.Status != ui.StatusPass {
				t.Errorf("row = %+v, want the suite to have run", suite)
			}

			var text bytes.Buffer
			if err := rep.WriteText(&text, false); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text.String(), "install(web)") {
				t.Errorf("report = %q, want the install named in the text grammar too", text.String())
			}
			// Anything automated reads the document and never the terminal.
			var doc bytes.Buffer
			if err := rep.WriteJSON(&doc); err != nil {
				t.Fatal(err)
			}
			if got := jsonRowByLabel(t, doc.String(), "install(web)"); got.Status != string(ui.StatusUnmeasured) {
				t.Errorf("status = %q, want %q", got.Status, ui.StatusUnmeasured)
			}
		})
	}
}

// A package manager asked to run a script in an uninstalled workspace installs
// that workspace itself, racing lydite's own install of the same root over one
// node_modules tree. The component that named a command goes through the same
// coalescing as the one that named a runner, so the root is installed once for
// both.
func TestACommandComponentSharesOneInstallWithItsRunnerSibling(t *testing.T) {
	brand := component.Component{Name: "brand", Dir: "brand", Command: []string{"sh", "-c", "exit 0"}}
	for _, tc := range []struct {
		name       string
		components []component.Component
		suites     []string
	}{
		{name: "alone", components: []component.Component{brand}, suites: []string{"test(brand)"}},
		{
			name:       "beside a runner",
			components: []component.Component{nodeComponent(), brand},
			suites:     []string{"test(web)", "test(brand)"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureRepo(t, "components: []\n")
			write(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
			write(t, root, "web/package.json", `{"name":"web"}`)
			write(t, root, "brand/package.json", `{"name":"brand"}`)
			runs := recordingStub(t, "pnpm")

			rep := ui.NewReport("test")
			runSuites(context.Background(), root, tc.components, config.Default(), 2, rep)

			if got := stubRuns(t, runs); got != 1 {
				t.Errorf("pnpm ran %d times, want one install for the root every component resolves", got)
			}
			for _, label := range tc.suites {
				if suite := rowByLabel(t, rep, label); suite.Status != ui.StatusPass {
					t.Errorf("row = %+v, want the suite to have run", suite)
				}
			}
			// The install ran, so no component takes a row about one that did not.
			for _, r := range rep.Rows() {
				if strings.HasPrefix(r.Label, "install(") {
					t.Errorf("row = %+v, want no install row: the install ran", r)
				}
			}
		})
	}
}

// A command component resolving a workspace root but holding no package.json
// of its own is not one of that workspace's packages, so its suite runs
// without lydite ever attempting an install for it — the same root a sibling
// package resolves is not enough on its own to join.
func TestACommandComponentWithNoPackageJSONInstallsNothing(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	write(t, root, "mod/main.go", "package main\n\nfunc main() {}\n")
	runs := recordingStub(t, "pnpm")

	mod := component.Component{Name: "mod", Dir: "mod", Command: []string{"sh", "-c", "exit 0"}}
	rep := ui.NewReport("test")
	runSuites(context.Background(), root, []component.Component{mod}, config.Default(), 1, rep)

	if got := stubRuns(t, runs); got != 0 {
		t.Errorf("pnpm ran %d time(s), want no install: mod holds no package.json, so it is not one of the workspace's packages", got)
	}
	if suite := rowByLabel(t, rep, "test(mod)"); suite.Status != ui.StatusPass {
		t.Errorf("row = %+v, want the suite to have run", suite)
	}
}

// recordingStub puts a program of the given name ahead of any real one on
// PATH, leaving one file per invocation in the returned directory. Nothing
// here runs a real package manager: an install that reaches the network tests
// the machine it runs on.
func recordingStub(t *testing.T, name string) string {
	t.Helper()
	bin, runs := t.TempDir(), t.TempDir()
	// The sleep holds the first install open long enough for a concurrent
	// second one to reach the lock rather than find the work already done.
	script := "#!/bin/sh\nmktemp " + filepath.Join(runs, "run.XXXXXX") + " >/dev/null\nsleep 0.2\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- a stub the test is about to execute
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return runs
}

// stubRuns counts the files recordingStub's program left behind.
func stubRuns(t *testing.T, runs string) int {
	t.Helper()
	entries, err := os.ReadDir(runs)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// An override still takes a context row naming where it runs, whether it
// goes on to succeed or fail — that fact is not available from the
// component's own row, which only reports the suite.
func TestAnOverrideTakesAContextRowNamingWhereItRuns(t *testing.T) {
	// The install is the typescript.install override, so what each case
	// exercises is lydite's attribution rather than any package manager's
	// behaviour — internal/nodedeps covers the detection.
	for _, tc := range []struct {
		name, install, value string
		status               ui.Status
	}{
		{name: "succeeds", install: "true", value: "passed", status: ui.StatusPass},
		{name: "fails", install: "exit 3", value: "not prepared", status: ui.StatusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixtureRepo(t, "components: []\n")
			write(t, root, "web/package.json", `{"name":"web"}`)
			cfg := config.Default()
			cfg.TypeScript.Install = tc.install

			rep := ui.NewReport("test")
			runSuites(context.Background(), root, []component.Component{nodeComponent()}, cfg, 1, rep)

			install := rowByLabel(t, rep, "install(web)")
			if install.Status != ui.StatusContext {
				t.Errorf("row = %+v, want a context row naming where the override runs", install)
			}
			if suite := rowByLabel(t, rep, "test(web)"); suite.Status != tc.status || suite.Value != tc.value {
				t.Errorf("row = %+v, want %q %q", suite, tc.status, tc.value)
			}
		})
	}
}

// nodeComponent is a JavaScript component whose suite is a command of its own,
// so a row about its install is not also a row about whether a package manager
// is on the machine running these tests.
func nodeComponent() component.Component {
	return component.Component{
		Name: "web", Dir: "web", Runner: runner.Vitest,
		Command: []string{"sh", "-c", "exit 0"},
	}
}

// nodeCommandComponent is the same package driven by a raw command instead of
// a runner: it names no language, and the package.json its directory holds is
// what says its dependencies are lydite's to install.
func nodeCommandComponent() component.Component {
	return component.Component{
		Name: "web", Dir: "web",
		Command: []string{"sh", "-c", "exit 0"},
	}
}

// A Go component's preparation is read off the invocation, not off the variant
// that named it, so the plain suite mutation runs once per mutant installs
// nothing while the instrumented one fetches the wrapper it goes through.
//
// The same rule cargo-llvm-cov's install follows, and for the same reason: a
// second list of which variants need what is right until a variant changes and
// only one of the two is updated.
func TestAGoComponentPreparesOnlyWhatItsInvocationRuns(t *testing.T) {
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	if r.Prepare == nil {
		t.Fatal("go-test declares no preparation step, so its instrumented suite runs a wrapper nothing installed")
	}
	plain, ok := r.Build(runner.Plain, nil)
	if !ok {
		t.Fatal("go-test builds no plain variant")
	}
	// `go` is on PATH or the component could not have been built at all, so
	// the plain variant must prepare nothing — an install here is one mutation
	// would pay for before every mutant.
	if err := r.Prepare(context.Background(), plain, t.TempDir(), "", "", executil.Env{}, io.Discard); err != nil {
		t.Errorf("the plain variant ran a preparation step: %v", err)
	}
}

// A component declaring no services needs no runtime, so a repository without
// one runs on a machine with no container engine at all.
func TestAComponentWithNoServicesNeedsNoRuntime(t *testing.T) {
	// PATH is replaced rather than prepended to: leaving the real one behind
	// keeps docker and podman resolvable, so a planner that probed anyway
	// would find one and the assertion below would hold either way.
	t.Setenv("PATH", t.TempDir())
	root := fixtureRepo(t, "components: []\n")
	plans := planComponents(context.Background(), root, []component.Component{{Name: "fixture", Dir: "mod", Runner: runner.GoTest}}, "test", false)
	if len(plans) != 1 || !plans[0].ready {
		t.Fatalf("a component with no compose block must not be probed for a runtime: %+v", plans)
	}
	defer plans[0].log.Close()
	stop, _, ok := startServices(context.Background(), plans[0], "test(fixture)")
	if !ok {
		t.Fatal("a component with no services must start none")
	}
	stop()
}

// A component failing must not reprint its whole suite under the row.
func TestTailIsBounded(t *testing.T) {
	var lines []string
	for i := range 500 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := tail(strings.Join(lines, "\n"))
	if len(got) != tailLines {
		t.Fatalf("tail returned %d lines, want %d", len(got), tailLines)
	}
	// The last lines, since that is where a runner's summary and the failure
	// above it are.
	if got[len(got)-1] != "line 499" {
		t.Errorf("tail ends at %q, want the end of the output", got[len(got)-1])
	}
}

func TestTailOfNothingIsNothing(t *testing.T) {
	if got := tail(""); got != nil {
		t.Errorf("tail(\"\") = %v, want nothing", got)
	}
	if got := tail("\n\n"); got != nil {
		t.Errorf("tail of blank lines = %v, want nothing", got)
	}
}

func TestResolveConcurrency(t *testing.T) {
	for _, tc := range []struct {
		flag    string
		want    int
		wantErr bool
	}{
		{flag: "4", want: 4},
		{flag: "1", want: 1},
		// A slot for every component: the scheduler never admits more than it
		// was given, so this needs no count to resolve against and can be
		// checked before any work happens.
		{flag: "max", want: math.MaxInt},
		{flag: "0", wantErr: true},
		{flag: "-2", wantErr: true},
		// Refused rather than defaulted: a typo that silently ran anyway
		// would have lydite ignore something the caller said.
		{flag: "all", wantErr: true},
	} {
		got, err := resolveConcurrency(tc.flag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveConcurrency(%q) = %d, want an error", tc.flag, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("resolveConcurrency(%q) = %d, %v; want %d", tc.flag, got, err, tc.want)
		}
	}
}

// Rows are in declaration order, never completion order: a reader diffing two
// runs depends on it, and ordering by whichever finished first would put this
// run's timing into the document.
//
// The names are deliberately not in alphabetical order, so a report that had
// been sorted rather than kept in place fails this too.
func TestRowsAreInDeclarationOrder(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	var declared []component.Component
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		write(t, root, name+"/go.mod", "module "+name+"\n\ngo 1.26\n")
		write(t, root, name+"/x_test.go", "package "+name+"\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")
		declared = append(declared, component.Component{Name: name, Dir: name, Runner: runner.GoTest})
	}

	rep := ui.NewReport("test")
	runSuites(context.Background(), root, declared, config.Default(), 3, rep)

	var got []string
	for _, r := range rep.Rows() {
		if strings.HasPrefix(r.Label, "test(") {
			got = append(got, r.Label)
		}
	}
	want := []string{"test(charlie)", "test(alpha)", "test(bravo)"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

// depends_on is an invalidation edge and never a build-order one. The
// scheduler must not read it: lydite passes no artifact between components, so
// ordering them would cost parallelism to express a claim their author never
// made.
//
// The assertion is that the edge contributes nothing the scheduler could
// serialise on. That two items with no conflict then genuinely run at once is
// internal/scheduler's own test, which forces the overlap with a barrier.
func TestDependsOnDoesNotSerialise(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "sdk/go.mod", "module sdk\n\ngo 1.26\n")
	write(t, root, "cli/go.mod", "module cli\n\ngo 1.26\n")
	declared := []component.Component{
		{Name: "sdk", Dir: "sdk", Runner: runner.GoTest},
		{Name: "cli", Dir: "cli", Runner: runner.GoTest, DependsOn: []string{"sdk"}},
	}
	plans := planComponents(context.Background(), root, declared, "test", false)
	var items []scheduler.Item
	for _, p := range plans {
		defer p.log.Close()
		items = append(items, itemFor(p))
	}
	if got := scheduler.Conflicts(items); len(got) != 0 {
		t.Fatalf("Conflicts = %v, want none: a depends_on edge is not a port", got)
	}
}

// A component the run never reached reports that it did not run, rather than
// being dropped. A truncated run that omitted rows would read as a complete
// run over fewer components, and a check that could not run must never read as
// one that did.
func TestComponentsNotReachedAreReportedUnmeasured(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	declared := []component.Component{
		{Name: "a", Dir: "mod", Runner: runner.GoTest},
		{Name: "b", Dir: "mod", Runner: runner.GoTest},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rep := ui.NewReport("test")
	runSuites(ctx, root, declared, config.Default(), 2, rep)

	seen := 0
	for _, r := range rep.Rows() {
		if !strings.HasPrefix(r.Label, "test(") {
			continue
		}
		seen++
		if r.Status != ui.StatusUnmeasured || r.Value != "not run" {
			t.Errorf("row = %+v, want an unmeasured `not run`", r)
		}
	}
	if seen != len(declared) {
		t.Fatalf("%d component rows, want %d: a component that never ran must still be reported", seen, len(declared))
	}
	// The truncation has to be in the document, not only in the process exit
	// code: anything automated reads --json and never the terminal, so a run
	// publishing "verdict": "pass" having tested nothing is a PR comment
	// rendering a green gate. The component rows stay unmeasured — they did
	// not fail, they did not run — and the schedule row is what fails.
	schedule := rowByLabel(t, rep, "schedule")
	if schedule.Status != ui.StatusFail {
		t.Errorf("schedule row = %+v, want a failure: the run did not start every component", schedule)
	}
	if !strings.Contains(schedule.Value, "0 of 2") {
		t.Errorf("schedule value = %q, want how many of the components actually started", schedule.Value)
	}
	if rep.Verdict() != ui.VerdictFail || rep.ExitCode() != 1 {
		t.Errorf("verdict = %q, exit = %d; want a truncated run to publish a failure",
			rep.Verdict(), rep.ExitCode())
	}
}

// The row says what the scheduler did, because the observed concurrency is
// what separates a scheduler that ran from one that only claims to.
func TestScheduleRowNamesTheContendedPorts(t *testing.T) {
	row := scheduleRow(context.Background(), scheduler.Outcome{
		MaxConcurrent: 3,
		Started:       4,
		Conflicts:     []scheduler.Conflict{{A: "go/api", B: "rust", On: "port 5432"}},
	}, 4, 4)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v", row)
	}
	if !strings.Contains(row.Value, "max 3 concurrent") {
		t.Errorf("value = %q, want the observed concurrency", row.Value)
	}
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "go/api and rust serialised on port 5432") {
		t.Errorf("detail = %q, want the contended pair named", detail)
	}
}

// runSuites runs declared through the run stage the way `lydite test
// --no-coverage` does, with this command's own logs and no flaky gate, and adds
// the section it produces to rep.
func runSuites(ctx context.Context, root string, declared []component.Component, cfg config.Config, limit int, rep *ui.Report) []measurement {
	// The run stage reports every failure through its rows and returns no
	// error of its own.
	out, _ := teststages.Run(ctx, teststages.RunIn{
		Dir: root, Decl: component.File{Components: declared}, Own: declared, Selected: declared,
		Config: cfg, Concurrency: limit, Logs: componentLogs,
	})
	for _, r := range out.Rows {
		rep.Add(r)
	}
	return out.Measurements
}

func rowByLabel(t *testing.T, rep *ui.Report, label string) ui.Row {
	t.Helper()
	for _, r := range rep.Rows() {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no %q row in %v", label, rep.Rows())
	return ui.Row{}
}

// Watching a hang is the one thing --stream exists for, and a suite that has
// printed no newline yet is exactly the case: holding its line until the
// process is killed withholds the output somebody turned the flag on to see.
func TestAnUnterminatedLineIsShownAnyway(t *testing.T) {
	var got []byte
	w := &prefixWriter{prefix: "web | ", delay: time.Millisecond}
	// emit writes to stderr in production; the test reads what it formatted
	// rather than capturing the process's stderr, which no other test could
	// then share.
	done := make(chan struct{})
	w.onEmit = func(line []byte) {
		got = append(append(got, line...), '\n')
		select {
		case <-done:
		default:
			close(done)
		}
	}

	if _, err := w.Write([]byte("running 412 tests...")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("an unterminated line was never shown: a hanging suite prints nothing")
	}
	if string(got) != "running 412 tests...\n" {
		t.Fatalf("got %q", got)
	}
}

// A line that arrives in pieces is shown once, whole, rather than split at
// whatever boundary the child happened to flush at.
func TestACompletedLineIsNotSplit(t *testing.T) {
	var lines []string
	// A deadline long enough that the assertion is about the newline and not
	// about how fast this loop ran: every other test here depends on
	// synchronisation rather than timing, and one that did not would go flaky
	// on a loaded runner years from now.
	w := &prefixWriter{prefix: "web | ", delay: time.Hour}
	w.onEmit = func(line []byte) { lines = append(lines, string(line)) }
	for _, chunk := range []string{"ok  ", "web ", "(cached)\n"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if len(lines) != 1 || lines[0] != "ok  web (cached)" {
		t.Fatalf("lines = %q, want one whole line", lines)
	}
}

// The deadline runs from when the pending line began, not from the last write.
// A runner redrawing a progress display writes continuously without ever
// emitting a newline, and a deadline restarted on each write would never
// expire — nothing would reach the terminal, and the buffer would hold every
// byte of it.
func TestContinuousOutputWithNoNewlineIsStillShown(t *testing.T) {
	emitted := make(chan []byte, 8)
	w := &prefixWriter{prefix: "web | ", delay: 20 * time.Millisecond}
	w.onEmit = func(line []byte) { emitted <- append([]byte(nil), line...) }

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = w.Write([]byte("\rdownloading"))
			time.Sleep(time.Millisecond)
		}
	}()

	select {
	case line := <-emitted:
		if len(line) == 0 {
			t.Fatal("emitted an empty line")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a continuously written line was never shown: the deadline is measuring silence, not the line's age")
	}
}

// A deadline armed for a line that has since been completed does not belong to
// the line waiting now. Keeping it fires early and splits the new line, which
// is the mid-line split buffering exists to prevent.
//
// The defect shows as an emission that must not happen, observed on a channel
// rather than by comparing two sleeps: a stale deadline fires 200ms after the
// second line starts, and a correct one not for a further 800ms, so neither
// bound is close to the scheduling slack of a loaded machine.
func TestTheDeadlineFollowsThePendingLine(t *testing.T) {
	const delay = time.Second
	emitted := make(chan string, 4)
	w := &prefixWriter{prefix: "web | ", delay: delay}
	w.onEmit = func(line []byte) { emitted <- string(line) }

	if _, err := w.Write([]byte("PASS")); err != nil {
		t.Fatal(err)
	}
	// Well inside the first line's deadline, so it is still pending when the
	// write below completes it.
	time.Sleep(800 * time.Millisecond)
	select {
	case line := <-emitted:
		t.Fatalf("emitted %q before its deadline", line)
	default:
	}

	if _, err := w.Write([]byte(" ok\nrunning batch 2 of 9")); err != nil {
		t.Fatal(err)
	}
	if line := <-emitted; line != "PASS ok" {
		t.Fatalf("emitted %q, want the completed line", line)
	}

	// A deadline kept from the first line fires 200ms from here; the second
	// line's own runs for a full second.
	select {
	case line := <-emitted:
		t.Fatalf("emitted %q: the line that just started was split by the previous line's deadline", line)
	case <-time.After(500 * time.Millisecond):
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never appeared: the component never started", path)
}

// A run cancelled once every component had started kills each suite mid-flight,
// so each exits non-zero. Reporting those as test failures blames a CI job
// timeout on the repository's tests, in the document the PR comment reads —
// and the schedule row would pass, because nothing was left unstarted.
func TestAKilledSuiteIsNotReportedAsAFailure(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/slow_test.go",
		"package fixture\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) { time.Sleep(30 * time.Second) }\n")
	// setup runs only after the scheduler has admitted the component, so the
	// marker it writes is the signal that this run is genuinely under way —
	// where waiting on the log would not be, since planning creates that file
	// before the scheduler has looked at the context at all.
	started := filepath.Join(root, "started")
	declared := []component.Component{{
		Name: "slow", Dir: "mod", Runner: runner.GoTest,
		Setup: []string{"touch " + started},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	rep := ui.NewReport("test")
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSuites(ctx, root, declared, config.Default(), 1, rep)
	}()
	waitForFile(t, started)
	cancel()
	<-done

	row := rowByLabel(t, rep, "test(slow)")
	if row.Status != ui.StatusUnmeasured || row.Value != "not completed" {
		t.Errorf("row = %+v, want the killed suite reported as unmeasured, not as a test failure", row)
	}
	schedule := rowByLabel(t, rep, "schedule")
	if schedule.Status != ui.StatusFail {
		t.Errorf("schedule = %+v, want a failure: the run was cut short", schedule)
	}
	if rep.ExitCode() != 1 {
		t.Errorf("exit = %d, want 1", rep.ExitCode())
	}
}

// A row built during planning is final before the run begins, and is the one
// actionable error such a run produced. An interrupt must not replace it with a
// sentence about an interrupt that had nothing to do with it.
//
// The interrupt has to land while a component is genuinely running, so a second
// component holds the run open until the broken one's row has already been
// decided.
func TestAPlanningFailureSurvivesAnInterrupt(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/compose.yaml", "services:\n  db:\n    image: postgres\n")
	write(t, root, "slow/go.mod", "module slow\n\ngo 1.26\n")
	write(t, root, "slow/slow_test.go",
		"package slow\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) { time.Sleep(30 * time.Second) }\n")

	started := filepath.Join(root, "started")
	declared := []component.Component{
		{
			Name: "broken", Dir: "mod", Runner: runner.GoTest,
			Compose: component.Compose{File: "./compose.yaml", Up: []string{"ghost"}},
		},
		{
			Name: "slow", Dir: "slow", Runner: runner.GoTest,
			Setup: []string{"touch " + started},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	rep := ui.NewReport("test")
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSuites(ctx, root, declared, config.Default(), 2, rep)
	}()
	waitForFile(t, started)
	cancel()
	<-done

	row := rowByLabel(t, rep, "test(broken)")
	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v, want the declaration error kept: the interrupt did not cause it", row)
	}
	if !strings.Contains(strings.Join(row.Detail, " "), "ghost") {
		t.Errorf("detail = %v, want the undeclared service still named", row.Detail)
	}
}

// An interrupt during the suites still renders the report and writes its
// document. The run's own rows say what was cut short — a failing schedule row,
// the unfinished component not completed — and the flow stopping before the
// gates after the run must not discard them in favour of a bare error.
func TestAnInterruptedRunStillRendersItsReport(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/slow_test.go",
		"package fixture\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestSlow(t *testing.T) { time.Sleep(30 * time.Second) }\n")
	// setup runs only once the scheduler has admitted the component, so the
	// marker is the signal that a suite is genuinely under way.
	started := filepath.Join(root, "started")
	write(t, root, component.FileName, fmt.Sprintf(`components:
  - name: slow
    dir: mod
    runner: go-test
    setup: ["touch %s"]
`, started))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"test", "--dir", root, "--no-color", "--json"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	waitForFile(t, started)
	cancel()
	err := <-done

	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("err = %v, want the report's own exit 1\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}
	if schedule := jsonRowByLabel(t, out.String(), "schedule"); schedule.Status != "fail" ||
		!strings.HasPrefix(schedule.Value, "interrupted after") {
		t.Errorf("schedule = %+v, want a failure saying the run was interrupted", schedule)
	}
	if row := jsonRowByLabel(t, out.String(), "test(slow)"); row.Status != "unmeasured" || row.Value != "not completed" {
		t.Errorf("test(slow) = %+v, want the killed suite not completed", row)
	}
	if _, err := os.Stat(filepath.Join(reportsDir(root), documentName("test"))); err != nil {
		t.Errorf("the report document was not written: %v", err)
	}
}

// A run cut short before any suite ran has no rows saying so, and a report
// holding only the declaration's gates would read as a pass. It is an error.
func TestARunCancelledBeforeItsSuitesIsAnError(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"test", "--dir", root, "--no-color"})
	err := cmd.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled\n%s", err, out.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want no report", out.String())
	}
}

// stdout carries the report and nothing else. `lydite test` provisions
// toolchains, warns about unused excludes and warns about every coverage gap,
// and all of it goes to stderr — under --json a single diagnostic on stdout
// makes the document unparseable, which is what anything automated reads.
func TestDiagnosticsStayOffStdout(t *testing.T) {
	root := fixtureRepo(t, "components:\n  - name: mod\n    dir: mod\n    runner: go-test\n    args: [\"./...\"]\n")
	out, errOut, err := runTestCmdStreams(t, root, "--json")
	if err != nil {
		t.Fatalf("run: %v\nstdout: %s\nstderr: %s", err, out, errOut)
	}
	var doc struct {
		Rows []struct{ Label string } `json:"rows"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &doc); jsonErr != nil {
		t.Fatalf("stdout is not a JSON document (%v):\n%s", jsonErr, out)
	}
	if len(doc.Rows) == 0 {
		t.Error("the document carries no rows")
	}
	// The diagnostics this run does produce went somewhere, and it was not
	// stdout. Toolchain provisioning always says what it resolved, so this
	// run has one — without checking for it the test would pass on a run that
	// printed nothing anywhere, proving nothing about where output goes.
	if !strings.Contains(errOut, "go:") {
		t.Errorf("stderr = %q, want the toolchain note this run produces", errOut)
	}
}

// A component's declared PATH has to reach the child. Composed as a variable
// beside lydite's own, it would always be the earlier of two PATH entries and
// the child would drop it — with nothing in argv or the log to show for it,
// which is the same invisible duplicate-key failure the composition exists to
// prevent.
func TestAComponentsDeclaredPathReachesTheChild(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{
		"PATH": "/declared/bin",
		"FOO":  "bar",
	}}
	tc := &toolchain.Env{PathDirs: []string{"/resolved/bin"}}
	inv := runner.Invocation{PathDirs: []string{"/pinned/bin"}}

	got := childEnv(tc, c, inv)

	var paths []string
	for _, kv := range got {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			paths = append(paths, v)
		}
	}
	if len(paths) != 1 {
		t.Fatalf("env = %q, want exactly one PATH entry", got)
	}
	sep := string(os.PathListSeparator)
	// The pinned runner first, then what lydite resolved, then the inherited
	// path, and only then what the component declared. A component may extend
	// the path its suite runs with; it may not choose which `go` or `cargo`
	// lydite itself launches, which is what a declared directory ahead of the
	// inherited one would do now that the program is resolved against the
	// environment the child is given.
	want := "/pinned/bin" + sep + "/resolved/bin" + sep + "/usr/bin" + sep + "/declared/bin"
	if paths[0] != want {
		t.Errorf("PATH = %q, want %q", paths[0], want)
	}
	if !slices.Contains(got, "FOO=bar") {
		t.Errorf("env = %q, want the component's other variables untouched", got)
	}
}

// The boundary the ordering exists to hold. lydite resolves a program against
// the environment it hands the child, so a declared directory placed ahead of
// the inherited PATH would let a scanned repository choose which `go` lydite
// runs — it ships `ci-bin/go`, declares `env: {PATH: ci-bin}`, and lydite
// installs gosec with it on a runner whose own toolchain was just verified.
func TestAComponentCannotShadowTheToolchainLyditeResolved(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{"PATH": "ci-bin"}}

	got := childEnv(nil, c, runner.Invocation{})
	var path string
	for _, kv := range got {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	entries := filepath.SplitList(path)
	if len(entries) != 2 || entries[0] != "/usr/bin" || entries[1] != "ci-bin" {
		t.Fatalf("PATH = %q, want the declared directory behind the inherited one", path)
	}
}

// Two different people's software is installed in prepare, and each gets its
// own environment. The repository's dependencies are installed with what the
// repository declared — its registry, its token. lydite's pinned runners are
// not: `cargo install` reads CARGO_HOME, CARGO_REGISTRIES_*, CARGO_NET_* and
// RUSTC_WRAPPER, so a declared environment reaching it would choose where
// lydite's own cargo-nextest comes from, and the result is cached beyond the
// run.
func TestPrepareInstallsLyditesRunnersWithoutTheRepositorysEnvironment(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{
		"CARGO_REGISTRIES_CRATES_IO_INDEX": "http://127.0.0.1:1",
		"SQLX_OFFLINE":                     "true",
	}}
	tc := &toolchain.Env{Vars: []string{"RUSTUP_TOOLCHAIN=1.96"}}

	env := executil.Env{Check: childEnv(tc, c, runner.Invocation{}), Install: tc.Environ()}

	if !slices.Contains(env.Check, "SQLX_OFFLINE=true") {
		t.Errorf("check env = %q, want what the repository declared", env.Check)
	}
	for _, kv := range env.Install {
		if strings.HasPrefix(kv, "CARGO_REGISTRIES_") || strings.HasPrefix(kv, "SQLX_OFFLINE=") {
			t.Fatalf("install env carries %q from the scanned repository", kv)
		}
	}
	if !slices.Contains(env.Install, "RUSTUP_TOOLCHAIN=1.96") {
		t.Errorf("install env = %q, want lydite's own resolved toolchain", env.Install)
	}
}

// lydite states which toolchain builds a component's code; the repository
// states how its own code builds. A component declaring GOTOOLCHAIN: auto
// would otherwise cancel the GOTOOLCHAIN=local pinAmbientGo exists to set,
// reinstating the `go install` downgrade that made govulncheck reject the
// source it was pointed at.
func TestAComponentCannotCancelTheResolvedToolchain(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	c := component.Component{Name: "svc", Env: map[string]string{"GOTOOLCHAIN": "auto"}}
	tc := &toolchain.Env{Vars: []string{"GOTOOLCHAIN=local"}}

	got := childEnv(tc, c, runner.Invocation{})

	// Last wins, so lydite's has to be the last one present.
	last := ""
	for _, kv := range got {
		if v, ok := strings.CutPrefix(kv, "GOTOOLCHAIN="); ok {
			last = v
		}
	}
	if last != "local" {
		t.Fatalf("GOTOOLCHAIN = %q, want lydite's resolved value to win", last)
	}
}

// The gate end to end over the probe ADR 0039 decides on: the suite runs, the
// tests the change introduced are rerun in a process of their own, and the one
// whose two outcomes disagree fails the gate and lands on its own declaration.
//
// TestFlakyNew passes in the process that finds no marker and fails in the one
// that does, so run 1 and run 2 disagree about it deterministically — a probe
// that flaked at random would make the test proving this gate the gate's own
// first false positive.
func TestTheFlakyGateFailsANewTestThatDisagreesWithItself(t *testing.T) {
	root := flakyProbeRepo(t)
	out, err := runTestCmd(t, root, "--json", "--no-coverage", "--gate-flaky")
	if err == nil {
		t.Fatalf("a new test that answered twice and differently passed the run:\n%s", out)
	}
	// The suite itself passed: run 1 is the process that leaves the marker, so
	// the only red row is the gate's.
	if got := jsonRowByLabel(t, out, "test(probe)"); got.Status != string(ui.StatusPass) {
		t.Fatalf("test(probe) = %+v, want a passing suite: the disagreement is the gate's finding, not the suite's", got)
	}
	row := jsonRowByLabel(t, out, "flaky(probe)")
	if row.Status != string(ui.StatusFail) {
		t.Fatalf("flaky(probe) = %+v, want a failure", row)
	}
	if !strings.Contains(row.Value, "1 of 3 new test(s)") {
		t.Errorf("flaky(probe) = %q, want one of the three new tests named as the disagreement", row.Value)
	}
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "TestFlakyNew: run 1 passed, run 2 failed") {
		t.Errorf("detail = %q, want both outcomes of the test that disagreed", detail)
	}
	// The tests that agreed take no line of their own: the row says what it
	// established, and two runs that agreed established nothing to act on.
	// They are in the rerun's argv, which is one invocation for the package.
	for _, name := range []string{"TestDeterministicNew", "TestNewSubtests"} {
		if strings.Contains(detail, name+":") || strings.Contains(detail, name+" was") {
			t.Errorf("detail = %q, want nothing claimed about %s", detail, name)
		}
	}
	// The test the change did not introduce is never rerun and never named:
	// the gate's budget is the new tests alone.
	if strings.Contains(out, "TestPreExistingStable") {
		t.Errorf("the report mentions a test the change did not introduce:\n%s", out)
	}

	found := jsonFindings(t, out)
	if len(found) != 1 {
		t.Fatalf("findings = %+v, want one per disagreement", found)
	}
	f := found[0]
	// The declaration's own line, so ADR 0031 puts the claim on the line the
	// author just wrote rather than in a comment about the file.
	if f.Gate != "flaky" || f.Path != "probe/probe_test.go" || f.Line != 32 {
		t.Errorf("finding = %+v, want it on the func TestFlakyNew line", f)
	}
	if f.Site != "probe TestFlakyNew" {
		t.Errorf("site = %q, want the package directory and the test's name", f.Site)
	}
	if !strings.Contains(f.Message, "TestFlakyNew") || !strings.Contains(f.Message, "disagreed") {
		t.Errorf("message = %q, want the test named and the claim stated", f.Message)
	}
	// The rerun's argv, so the author reproduces it with one command rather
	// than being told their test is haunted.
	if d := strings.Join(f.Detail, "\n"); !strings.Contains(d, "-count=1") || !strings.Contains(d, "TestFlakyNew") {
		t.Errorf("finding detail = %q, want the rerun that reproduces it", d)
	}
}

// The flag is never inferred, and a run that was not asked to gate says so
// rather than leaving the row out: a section that quietly disappears is
// indistinguishable from a concern that passed.
func TestWithoutTheFlagEveryComponentTakesAContextRow(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
`)
	out, err := runTestCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("test failed: %v\n%s", err, out)
	}
	row := jsonRowByLabel(t, out, "flaky(fixture)")
	if row.Status != string(ui.StatusContext) {
		t.Errorf("flaky(fixture) = %+v, want a context row: nothing was gated", row)
	}
	if !strings.Contains(row.Value, "--gate-flaky") {
		t.Errorf("value = %q, want the flag that would gate it named", row.Value)
	}
	if len(jsonFindings(t, out)) != 0 {
		t.Errorf("a run that gated nothing published findings:\n%s", out)
	}
}

// A run that selected nothing still renders a flaky row per component it is
// responsible for, the same way it renders one per suite: a section that
// quietly disappeared would be indistinguishable from one that examined
// everything and found no new test.
func TestGateFlakyReportsEvenWhenNothingWasSelected(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "", "")

	out, err := runTestCmd(t, root, "--affected", "--gate-flaky", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, name := range []string{"a", "b"} {
		row := jsonRowByLabel(t, out, flakyLabel(name))
		if row.Status != string(ui.StatusUnmeasured) {
			t.Errorf("flaky(%s) = %+v, want unmeasured: the component's suite never ran", name, row)
		}
	}
}

// flakyProbeRepo is a repository whose one component holds the flakyprobe
// fixture: the merge-base's tree at origin/main, and HEAD's — which declares
// three tests the base does not — on the branch.
func flakyProbeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	write(t, root, component.FileName,
		"components:\n  - name: probe\n    dir: probe\n    runner: go-test\n    args: [\"./...\"]\n")
	copyTree(t, filepath.Join("..", "..", "internal", "flaky", "testdata", "flakyprobe", "base"), root, "probe")
	gitIn(t, origin, "init", "--quiet", "--bare", "-b", "main")
	gitIn(t, root, "init", "--quiet", "-b", "main")
	gitIn(t, root, "config", "user.email", "t@example.com")
	gitIn(t, root, "config", "user.name", "t")
	gitIn(t, root, "remote", "add", "origin", origin)
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "--quiet", "-m", "base")
	gitIn(t, root, "push", "--quiet", "origin", "main")
	copyTree(t, filepath.Join("..", "..", "internal", "flaky", "testdata", "flakyprobe", "head"), root, "probe")
	commitChange(t, root, "", "")

	// The pinned wrapper is what makes a plain Go suite write the report the
	// gate reads its first outcomes from, and fetching it is the one thing
	// here that needs a network on a cold cache.
	inv, _ := runner.GoJUnitPlain(nil)
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	if err := r.Prepare(context.Background(), inv, filepath.Join(root, "probe"), "", "", executil.Env{}, io.Discard); err != nil {
		t.Skipf("the pinned test wrapper is not installed and could not be fetched: %v", err)
	}
	return root
}

// copyTree materialises a committed fixture tree under root/dir.
func copyTree(t *testing.T, src, root, dir string) {
	t.Helper()
	tree := fixture.Tree(t, src)
	entries, err := os.ReadDir(tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(tree, e.Name())) // #nosec G304 -- a fixture tree this test materialised
		if err != nil {
			t.Fatal(err)
		}
		write(t, root, path.Join(dir, e.Name()), string(data))
	}
}

// jsonFindings is the located claims the document carries, which is the
// channel a review thread is opened from.
func jsonFindings(t *testing.T, out string) []finding.Finding {
	t.Helper()
	var doc struct {
		Findings []finding.Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	return doc.Findings
}

// A component declaring no suite takes no toolchain on the test side, whatever
// language it declares: lang: names what it is scanned as, and a suite that
// does not exist has nothing to run under a Go it asked nobody for.
func TestAComponentDeclaringNoSuiteProvisionsNoToolchain(t *testing.T) {
	root := t.TempDir()
	write(t, root, "scripts/package.json", "{}\n")
	got := testUnits(root, []component.Component{
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
		{Name: "gen", Dir: "gen", DeclaredLang: runner.Go},
		{Name: "mod", Dir: "mod", Runner: runner.GoTest},
	})
	want := []toolchain.Unit{{Name: "mod", Lang: runner.Go, Dir: "mod"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("testUnits = %+v, want %+v", got, want)
	}
}

// A raw command implies no language, and one whose own directory holds a
// package.json is resolved through the workspace's Node toolchain: its command
// installs and runs through that Node and its pinned package manager. Lang
// stays empty, so nothing deciding by language reads it as TypeScript.
func TestACommandComponentWithItsOwnPackageJSONIsANodeCommandUnit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/package.json", "{}\n")
	got := testUnits(root, []component.Component{
		{Name: "web", Dir: "web", Command: []string{"pnpm", "test"}, DeclaredLang: runner.Shell},
		{Name: "mod", Dir: "mod", Runner: runner.GoTest},
	})
	want := []toolchain.Unit{
		{Name: "web", Dir: "web", NodeCommand: true},
		{Name: "mod", Lang: runner.Go, Dir: "mod"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("testUnits = %+v, want %+v", got, want)
	}
}

// A command whose own directory holds no package.json needs no toolchain on
// the test side, whatever language it declares: a Go or Rust command runs
// through a go.mod or a Cargo.toml, never a Node workspace, and a package.json
// in a directory above it is not its own.
func TestACommandComponentWithoutItsOwnPackageJSONProvisionsNothing(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", "{}\n")
	write(t, root, "tool/go.mod", "module tool\n\ngo 1.26\n")
	write(t, root, "crate/Cargo.toml", "[package]\nname = \"crate\"\n")
	write(t, root, "make/Makefile", "test:\n")
	write(t, root, "odd/package.json/keep", "")
	got := testUnits(root, []component.Component{
		{Name: "tool", Dir: "tool", Command: []string{"go", "test", "./..."}, DeclaredLang: runner.Go},
		{Name: "crate", Dir: "crate", Command: []string{"cargo", "test"}, DeclaredLang: runner.Rust},
		{Name: "make", Dir: "make", Command: []string{"make", "test"}},
		{Name: "odd", Dir: "odd", Command: []string{"make", "test"}},
	})
	if len(got) != 0 {
		t.Errorf("testUnits = %+v, want none: no command component has a package.json of its own", got)
	}
}

// review's API-surface comparison provisions only the units a component's
// runner implies. A command component beside its own package.json is a
// NodeCommand unit to `lydite test`, and never reaches review's list — even
// with the process sitting where a relative directory would find it.
func TestReviewProvisionsNoNodeCommandUnit(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/package.json", "{}\n")
	t.Chdir(root)
	components := []component.Component{
		{Name: "web", Dir: "web", Command: []string{"pnpm", "test"}},
		{Name: "mod", Dir: "mod", Runner: runner.GoTest},
		{Name: "ui", Dir: "ui", Runner: runner.Vitest},
	}
	got := componentUnits(components)
	want := []toolchain.Unit{
		{Name: "mod", Lang: runner.Go, Dir: "mod"},
		{Name: "ui", Lang: runner.TypeScript, Dir: "ui"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("componentUnits = %+v, want %+v", got, want)
	}
	if units := testUnits(root, components); len(units) != 3 || !units[0].NodeCommand {
		t.Errorf("testUnits = %+v, want the command component as a NodeCommand unit ahead of the two runners", units)
	}
}

// A component declaring no suite is never given to the scheduler, so it takes
// no lock on its directory. Rooted beside a component that runs, a lock would
// serialise that component against nothing — and the schedule row would name
// a pair nobody declared.
func TestAComponentDeclaringNoSuiteTakesNoLock(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	declared := []component.Component{
		{Name: "scripts", Dir: "mod", DeclaredLang: runner.Shell},
		{Name: "fixture", Dir: "mod", Runner: runner.GoTest},
	}
	plans := planComponents(context.Background(), root, declared, "test", false)
	for _, p := range plans {
		defer p.log.Close()
	}
	if plans[0].ready {
		t.Errorf("plan = %+v, want a component declaring no suite planned unrunnable", plans[0])
	}

	rep := ui.NewReport("test")
	ms := runSuites(context.Background(), root, declared, config.Default(), 2, rep)

	schedule := rowByLabel(t, rep, "schedule")
	if !strings.HasPrefix(schedule.Value, "1 component(s)") || strings.Contains(schedule.Value, "serialised") {
		t.Errorf("schedule = %+v, want one component scheduled and no pair serialised", schedule)
	}
	row := rowByLabel(t, rep, testLabel("scripts"))
	if row.Status != ui.StatusUnmeasured || !strings.Contains(row.Value, noSuiteReason) {
		t.Errorf("test(scripts) = %+v, want unmeasured, naming that it declares no suite", row)
	}
	if got := rowByLabel(t, rep, testLabel("fixture")); got.Status != ui.StatusPass {
		t.Errorf("test(fixture) = %+v, want the suite beside it to pass unaffected", got)
	}
	if !ms[0].Unmeasurable || ms[0].Why != noSuiteReason {
		t.Errorf("measurement = %+v, want unmeasurable for the declaration's reason", ms[0])
	}
	if _, err := os.Stat(filepath.Join(root, runner.ReportDir, "scripts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a log directory exists for a component that ran nothing: %v", err)
	}
}

// Unsharded, a component declaring no suite takes every test-side row from its
// declaration: the suite, its flaky rerun, its coverage and its complexity are
// each unmeasured, for a reason distinct from a raw command's — the author
// adds a suite here, where there they swap a command for a runner. The
// component that runs beside it is unaffected.
func TestAnUnshardedRunReportsANoSuiteComponentFromItsDeclaration(t *testing.T) {
	root := fixtureRepo(t, `components:
  - name: fixture
    dir: mod
    runner: go-test
  - name: scripts
    dir: scripts
    lang: shell
`)
	write(t, root, "scripts/install.sh", "#!/bin/sh\necho hi\n")
	write(t, root, config.FileName, "coverage:\n  floor: 10\n")
	out, err := runTestCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("test failed: %v\n%s", err, out)
	}
	for _, label := range []string{testLabel("scripts"), flakyLabel("scripts"), "coverage(scripts)", "crap(scripts)", "floor(scripts)"} {
		row := jsonRowByLabel(t, out, label)
		if row.Status != string(ui.StatusUnmeasured) {
			t.Errorf("%s = %+v, want unmeasured", label, row)
		}
		if !strings.Contains(row.Value, noSuiteReason) || strings.Contains(row.Value, "raw command") {
			t.Errorf("%s value = %q, want the no-suite reason and not a raw command's", label, row.Value)
		}
	}
	if row := jsonRowByLabel(t, out, testLabel("fixture")); row.Status != string(ui.StatusPass) {
		t.Errorf("test(fixture) = %+v, want a pass", row)
	}
	if row := jsonRowByLabel(t, out, "coverage(fixture)"); row.Status != string(ui.StatusContext) {
		t.Errorf("coverage(fixture) = %+v, want its measured figure", row)
	}
	if row := jsonRowByLabel(t, out, "crap(fixture)"); strings.Contains(row.Value, noSuiteReason) {
		t.Errorf("crap(fixture) = %+v, want its own score", row)
	}
}

// A floor row names a floor that is configured. With none configured there is
// no floor to say cannot apply, so a component declaring no suite takes only
// its coverage and complexity rows.
func TestANoSuiteComponentTakesAFloorRowOnlyWhenAFloorIsConfigured(t *testing.T) {
	unset := ui.NewReport("test")
	noSuiteCoverageRows(unset, "scripts", 0)
	var labels []string
	for _, r := range unset.Rows() {
		labels = append(labels, r.Label)
	}
	if want := []string{"coverage(scripts)", "crap(scripts)"}; fmt.Sprint(labels) != fmt.Sprint(want) {
		t.Errorf("rows with no floor = %v, want %v", labels, want)
	}

	set := ui.NewReport("test")
	noSuiteCoverageRows(set, "scripts", 10)
	row := rowByLabel(t, set, "floor(scripts)")
	if row.Status != ui.StatusUnmeasured || !strings.Contains(row.Value, "10.0%") || !strings.Contains(row.Value, noSuiteReason) {
		t.Errorf("floor(scripts) = %+v, want unmeasured, naming the floor and the no-suite reason", row)
	}
}

// A selection holding only components that declare no suite measures nothing,
// so it answers nothing about the repository: a composed coverage row or a
// baseline row there would describe a measurement no component took part in.
func TestASelectionOfOnlyNoSuiteComponentsTakesNoComposedRows(t *testing.T) {
	own := []component.Component{{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell}}
	file := component.File{Components: own}
	rep := ui.NewReport("test")
	addCoverageSection(t, newRootCmd(), rep, teststages.CoverageIn{
		Dir: t.TempDir(), Decl: file, Own: own, Config: config.Default(), Instrument: true,
	})
	var labels []string
	for _, r := range rep.Rows() {
		labels = append(labels, r.Label)
	}
	if want := []string{"coverage(scripts)", "crap(scripts)"}; fmt.Sprint(labels) != fmt.Sprint(want) {
		t.Errorf("rows = %v, want only the declaration's own %v", labels, want)
	}
}

// A run that selected nothing still takes a coverage and complexity row per
// component it is responsible for: a coverage section that disappeared when
// nothing was affected would read as one that measured everything and passed.
func TestCoverageRowsReportEvenWhenNothingWasSelected(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "", "")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, name := range []string{"a", "b"} {
		for _, label := range []string{"coverage(" + name + ")", "crap(" + name + ")"} {
			if row := jsonRowByLabel(t, out, label); row.Status != string(ui.StatusUnmeasured) {
				t.Errorf("%s = %+v, want unmeasured: the component's suite never ran", label, row)
			}
		}
	}
}

// internal/test/run writes the report directory's `.gitignore` itself when it
// clears a report, from its own copy of ignoreReports, since reports.go is where
// this package's lives. The two have to write the same file: a report
// directory whose `.gitignore` depends on which command created it first is
// one git treats differently from run to run.
func TestClearReportIgnoresTheReportDirectoryAsThisPackageDoes(t *testing.T) {
	ours := filepath.Join(t.TempDir(), runner.ReportDir)
	if err := os.MkdirAll(ours, 0o750); err != nil {
		t.Fatal(err)
	}
	ignoreReports(ours)

	dir := t.TempDir()
	if err := clearReport(dir, runner.ReportDir+"/coverage.out"); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(ours, ".gitignore")) // #nosec G304 -- a file this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, runner.ReportDir, ".gitignore")) // #nosec G304 -- a file the call under test just wrote
	if err != nil {
		t.Fatalf("clearing a report left no .gitignore in the report directory: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("clearReport wrote %q, ignoreReports writes %q", got, want)
	}
}

// planComponents hands each plan the componentLog the engine's opener opened
// for that component, looked up by name: the engine plans through an Output,
// and mutation reaches into the log itself — writing to it, closing it — so a
// plan holding some other component's log, or a fresh one, would put a
// component's output under another's name or leave the real file unclosed.
func TestPlanComponentsGivesEachPlanTheLogOpenedForIt(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	declared := []component.Component{
		{Name: "scripts", Dir: "mod", DeclaredLang: runner.Shell},
		{Name: "fixture", Dir: "mod", Runner: runner.GoTest},
		{Name: "other", Dir: "mod", Runner: runner.GoTest},
	}
	plans := planComponents(context.Background(), root, declared, "test", false)
	for _, p := range plans {
		defer p.log.Close()
	}
	if len(plans) != len(declared) {
		t.Fatalf("plans = %+v, want one per component", plans)
	}
	if plans[0].log == nil || plans[0].log.Rel != "" || plans[0].log.out != io.Discard {
		t.Errorf("scripts log = %+v, want one that writes nowhere", plans[0].log)
	}
	for i, name := range []string{"fixture", "other"} {
		p := plans[i+1]
		if p.c.Name != name || !p.ready {
			t.Fatalf("plan %d = %+v, want %s, runnable", i+1, p, name)
		}
		if want := filepath.Join(runner.ReportDir, name, "test.log"); p.log.Rel != want {
			t.Errorf("%s log = %q, want %q", name, p.log.Rel, want)
		}
		if _, err := fmt.Fprintf(p.log.out, "written by %s\n", name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"fixture", "other"} {
		body, err := os.ReadFile(filepath.Join(root, runner.ReportDir, name, "test.log")) // #nosec G304 -- a log this test's run wrote
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "written by "+name+"\n" {
			t.Errorf("%s log = %q, want only what was written through its own plan", name, body)
		}
	}
}
