package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// The plain variant is the fast path, and the only one `lydite test` wants.
func TestInvocationIsThePlainVariant(t *testing.T) {
	inv, err := invocation(component.Component{Runner: runner.GoTest, Args: []string{"-race", "./..."}})
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
	inv, err := invocation(component.Component{Command: []string{"make", "test"}})
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
		t.Errorf("verdict = %q, want a pass: declaring nothing is not a failure", doc.Verdict)
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

func runTestCmd(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"test", "--dir", root, "--no-color"}, extra...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
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

// A JavaScript suite run without its node_modules fails at import, naming the
// tests rather than the absent dependencies.
func TestAComponentWhoseInstallFailsDoesNotRunItsSuite(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/package.json", `{"name":"web"}`)
	// The install is the typescript.install override, so the row under test
	// is lydite's attribution of a failed install rather than any package
	// manager's behaviour — internal/nodedeps covers the detection.
	cfg := config.Default()
	cfg.TypeScript.Install = "exit 3"
	row := runComponent(context.Background(), root, component.Component{
		Name: "web", Dir: "web", Runner: runner.Vitest,
	}, cfg, false, false)
	if row.Status != ui.StatusFail {
		t.Fatalf("status = %q, want a failure", row.Status)
	}
	if row.Value != "not prepared" {
		t.Errorf("value = %q, want the preparation named rather than the suite", row.Value)
	}
	if !strings.Contains(strings.Join(row.Detail, " "), config.FileName) {
		t.Errorf("detail = %v, want the override named as the way out", row.Detail)
	}
}

// A Go component has nothing to install, so nothing runs ahead of its suite.
func TestAGoComponentHasNoPreparationStep(t *testing.T) {
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	if r.Prepare != nil {
		t.Error("go-test declares a preparation step it does not need")
	}
}

// Teardown undoes what setup did, so it has to run on the path where setup
// failed halfway — a half-applied migration is exactly what needs undoing.
func TestTeardownRunsWhenSetupFails(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	marker := filepath.Join(root, "torn-down")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Setup:    []string{"exit 7"},
		Teardown: []string{"touch " + marker},
	}, config.Default(), false, false)
	if row.Status != ui.StatusFail || row.Value != "setup failed" {
		t.Fatalf("row = %+v, want the setup named as the failure", row)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("teardown must run even when setup failed")
	}
}

// Leftover data makes the next run depend on the last one.
func TestTeardownRunsWhenTheSuiteFails(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	marker := filepath.Join(root, "torn-down")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"touch " + marker},
	}, config.Default(), false, false)
	if row.Status != ui.StatusFail || row.Value != "failed" {
		t.Fatalf("row = %+v, want the suite named as the failure", row)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("teardown must run when the suite failed")
	}
}

// A teardown that fails has left state behind for the next run to inherit,
// so it fails a component that otherwise passed.
func TestAFailingTeardownFailsAPassingComponent(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}, config.Default(), false, false)
	if row.Status != ui.StatusFail || row.Value != "teardown failed" {
		t.Fatalf("row = %+v, want the teardown named", row)
	}
}

// It never masks a failure that already happened: the earlier one is what
// the reader has to act on.
func TestAFailingTeardownDoesNotMaskAFailingSuite(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}, config.Default(), false, false)
	if row.Value != "failed" {
		t.Errorf("value = %q, want the suite failure to survive", row.Value)
	}
}

// Setup runs before the suite, not alongside it.
func TestSetupRunsBeforeTheSuite(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		// The suite reads what setup wrote, so it can only pass if the
		// ordering holds.
		Setup: []string{"echo 1 > setup-ran"},
		Args:  []string{"-run", "TestFoo", "./..."},
	}, config.Default(), false, false)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v", row)
	}
	if _, err := os.Stat(filepath.Join(root, "mod", "setup-ran")); err != nil {
		t.Error("setup must run in the component directory, before the suite")
	}
}

// A component declaring no services needs no runtime, so a repository without
// one runs on a machine with no container engine at all.
func TestAComponentWithNoServicesNeedsNoRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := fixtureRepo(t, "components: []\n")
	stop, _, ok := services(context.Background(), root, "test(fixture)", component.Component{Name: "fixture", Dir: "mod"}, false, openLog(root, "fixture", false))
	if !ok {
		t.Fatal("a component with no compose block must not be probed for a runtime")
	}
	stop()
}

// The cause has to be next to the verdict: a reader looking at a red row must
// not have to scroll past another component's container lifecycle to find out
// what happened, which is what a real CI log does to them.
func TestAFailingComponentCarriesTheCauseAndTheLog(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"the cause\") }\n")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}, config.Default(), false, false)

	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v", row)
	}
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "the cause") {
		t.Errorf("detail = %q, want the failing output under the row", detail)
	}
	if row.Log == "" {
		t.Fatal("a failing row must name where the whole output is")
	}
	if !strings.Contains(detail, row.Log) {
		t.Errorf("detail = %q, want it to name the log at %q", detail, row.Log)
	}
	body, err := os.ReadFile(filepath.Join(root, row.Log))
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	if !strings.Contains(string(body), "the cause") {
		t.Errorf("log = %q, want the whole output captured", body)
	}
}

// Everything is captured, always — under --json the terminal carries a
// document and nothing else, so the log is the only place the output exists.
func TestAPassingComponentStillCapturesItsOutput(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	row := runComponent(context.Background(), root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}, config.Default(), false, false)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v", row)
	}
	if row.Log == "" {
		t.Fatal("a passing row must still name its log")
	}
	if _, err := os.Stat(filepath.Join(root, row.Log)); err != nil {
		t.Errorf("log not written: %v", err)
	}
	// But it stays out of the prose: a path on every line of a clean run is
	// noise nobody asked for.
	if len(row.Detail) != 0 {
		t.Errorf("detail = %v, want a passing row to carry none", row.Detail)
	}
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
