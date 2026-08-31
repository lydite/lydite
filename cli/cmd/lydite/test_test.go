package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// A suite executed without the database it declared reports failures that
// name the tests instead of the missing service, and a green run is worse
// still: it would mean the declaration was ignored and nobody was told.
func TestAComponentDeclaringServicesFailsRatherThanRunning(t *testing.T) {
	row := runComponent(context.Background(), t.TempDir(), component.Component{
		Name:    "api",
		Dir:     ".",
		Runner:  runner.GoTest,
		Compose: component.Compose{Up: []string{"db"}},
	}, config.Default())
	if row.Status != ui.StatusFail {
		t.Fatalf("status = %q, want a failure", row.Status)
	}
	if !strings.Contains(strings.Join(row.Detail, " "), "compose services") {
		t.Errorf("detail = %v, want it to name the services", row.Detail)
	}
}

func TestAComponentDeclaringSetupFailsRatherThanRunning(t *testing.T) {
	row := runComponent(context.Background(), t.TempDir(), component.Component{
		Name: "api", Dir: ".", Runner: runner.GoTest, Setup: []string{"make migrate"},
	}, config.Default())
	if row.Status != ui.StatusFail {
		t.Fatalf("status = %q, want a failure", row.Status)
	}
}

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
	if len(doc.Rows) != 1 || doc.Rows[0].Status != string(ui.StatusUnmeasured) {
		t.Errorf("rows = %+v, want one unmeasured row", doc.Rows)
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
	}, cfg)
	if row.Status != ui.StatusFail {
		t.Fatalf("status = %q, want a failure", row.Status)
	}
	if row.Value != "dependencies not installed" {
		t.Errorf("value = %q, want the install named rather than the suite", row.Value)
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
