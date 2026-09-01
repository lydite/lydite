package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
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
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "web", Dir: "web", Runner: runner.Vitest,
	}), cfg)
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
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Setup:    []string{"exit 7"},
		Teardown: []string{"touch " + marker},
	}), config.Default())
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
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"touch " + marker},
	}), config.Default())
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
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}), config.Default())
	if row.Status != ui.StatusFail || row.Value != "teardown failed" {
		t.Fatalf("row = %+v, want the teardown named", row)
	}
}

// It never masks a failure that already happened: the earlier one is what
// the reader has to act on.
func TestAFailingTeardownDoesNotMaskAFailingSuite(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}), config.Default())
	if row.Value != "failed" {
		t.Errorf("value = %q, want the suite failure to survive", row.Value)
	}
}

// Setup runs before the suite, not alongside it.
func TestSetupRunsBeforeTheSuite(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		// The suite reads what setup wrote, so it can only pass if the
		// ordering holds.
		Setup: []string{"echo 1 > setup-ran"},
		Args:  []string{"-run", "TestFoo", "./..."},
	}), config.Default())
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
	// PATH is replaced rather than prepended to: leaving the real one behind
	// keeps docker and podman resolvable, so a planner that probed anyway
	// would find one and the assertion below would hold either way.
	t.Setenv("PATH", t.TempDir())
	root := fixtureRepo(t, "components: []\n")
	plans := planComponents(context.Background(), root, []component.Component{{Name: "fixture", Dir: "mod"}}, false)
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

// The cause has to be next to the verdict: a reader looking at a red row must
// not have to scroll past another component's container lifecycle to find out
// what happened, which is what a real CI log does to them.
func TestAFailingComponentCarriesTheCauseAndTheLog(t *testing.T) {
	root := fixtureRepo(t, "components: []\n")
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"the cause\") }\n")
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}), config.Default())

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
	row := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}), config.Default())
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

// planFor builds the plan runComponent takes. Every component in these tests
// declares no services, so nothing is probed and no stack is loaded.
func planFor(t *testing.T, root string, c component.Component) componentPlan {
	t.Helper()
	log := openLog(root, c.Name, false, len(c.Name))
	t.Cleanup(log.Close)
	return componentPlan{c: c, log: log, ready: true}
}

func TestResolveConcurrency(t *testing.T) {
	for _, tc := range []struct {
		flag     string
		selected int
		want     int
		wantErr  bool
	}{
		{flag: "4", selected: 9, want: 4},
		{flag: "1", selected: 9, want: 1},
		{flag: "max", selected: 9, want: 9},
		// "max" over an empty selection still has to be a usable bound.
		{flag: "max", selected: 0, want: 1},
		{flag: "0", selected: 9, wantErr: true},
		{flag: "-2", selected: 9, wantErr: true},
		// Refused rather than defaulted: a typo that silently ran anyway
		// would have lydite ignore something the caller said.
		{flag: "all", selected: 9, wantErr: true},
	} {
		got, err := resolveConcurrency(tc.flag, tc.selected)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveConcurrency(%q) = %d, want an error", tc.flag, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("resolveConcurrency(%q, %d) = %d, %v; want %d", tc.flag, tc.selected, got, err, tc.want)
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
	runComponents(context.Background(), root, declared, config.Default(), 3, false, rep)

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
	plans := planComponents(context.Background(), root, declared, false)
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
	runComponents(ctx, root, declared, config.Default(), 2, false, rep)

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
	row := scheduleRow(scheduler.Outcome{
		MaxConcurrent: 3,
		Started:       4,
		Conflicts:     []scheduler.Conflict{{A: "go/api", B: "rust", On: "port 5432"}},
	}, 4, 4, 4)
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
func TestTheDeadlineFollowsThePendingLine(t *testing.T) {
	var lines []string
	var mu sync.Mutex
	w := &prefixWriter{prefix: "web | ", delay: 150 * time.Millisecond}
	w.onEmit = func(line []byte) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, string(line))
	}

	// A partial line, then — just before its deadline — a write that
	// completes it and starts another.
	if _, err := w.Write([]byte("PASS")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := w.Write([]byte(" ok\nrunning batch 2 of 9")); err != nil {
		t.Fatal(err)
	}
	// Past the first line's deadline, comfortably short of the second's.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || lines[0] != "PASS ok" {
		t.Fatalf("lines = %q, want only the completed line: the second was split by the first line's deadline", lines)
	}
}
