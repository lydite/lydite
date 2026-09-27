package run

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/test/measure"
	"lydite/lydite/internal/ui"
)

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

// goModule is a scan root holding one buildable Go module at mod/ with a
// passing test.
func goModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "mod/go.mod", "module fixture\n\ngo 1.26\n")
	write(t, root, "mod/fixture.go", "package fixture\n\n// Foo is what the fixture's test exercises.\nfunc Foo() int { return 1 }\n")
	write(t, root, "mod/fixture_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\tif Foo() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	return root
}

// gitRepo writes the files and initialises a repository, for a gate that
// reads what the repository holds from git.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		write(t, root, rel, body)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "-A"}} {
		gitIn(t, root, args...)
	}
	return root
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commitChange writes one file, when rel names one, and commits whatever the
// tree holds.
func commitChange(t *testing.T, root, rel, body string) {
	t.Helper()
	if rel != "" {
		write(t, root, rel, body)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "--quiet", "--allow-empty", "-m", "change")
}

// openFile is the Output a command's opener gives a component: a log file under
// root's report directory, named relative to root. It is closed when the test
// ends.
func openFile(t *testing.T, root, name string) Output {
	t.Helper()
	dir := filepath.Join(root, runner.ReportDir, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.log")
	f, err := os.Create(path) // #nosec G304 -- a log under the test's own temporary directory
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatal(err)
	}
	return Output{W: f, Rel: rel}
}

// nodeCommandComponent is a JavaScript package driven by a raw command: it
// names no language, and the package.json its directory holds is what says its
// dependencies are lydite's to install.
func nodeCommandComponent() component.Component {
	return component.Component{
		Name: "web", Dir: "web",
		Command: []string{"sh", "-c", "exit 0"},
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
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "web", Dir: "web", Runner: runner.Vitest,
	}), cfg, nil, false, nil)
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

// A component that names a raw command over node dependencies fails the same
// way as one that names a runner: the label and detail installNote's silence
// would otherwise leave unreported come from the same Failure call either
// component's install goes through.
func TestACommandComponentWhoseInstallFailsDoesNotRunItsSuite(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web/package.json", `{"name":"web"}`)
	cfg := config.Default()
	cfg.TypeScript.Install = "exit 3"
	row, _ := runComponent(context.Background(), root, planFor(t, root, nodeCommandComponent()), cfg, nil, false, nil)
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

// The note is about the install, so a component nodedeps never installs takes
// none — including the JavaScript one whose workspace root resolves.
func TestNoInstallRowWhereThereIsNothingToSayAboutOne(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	write(t, root, "packages/ui/package.json", `{"name":"ui"}`)
	if _, ok := installNote(root, component.Component{
		Name: "ui", Dir: "packages/ui", Runner: runner.Vitest,
	}, config.Default()); ok {
		t.Error("a component under a workspace root is installed from it, so there is nothing to report")
	}
	if _, ok := installNote(root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}, config.Default()); ok {
		t.Error("a Go component installs no node dependencies, so no lockfile is missing from it")
	}
	// A raw command names no language, so the package.json in its own
	// directory is what decides. `mod` sits under the same workspace root and
	// holds none: whatever it builds, it is not one of that workspace's
	// packages, and an install it never asked for is not withheld from it.
	if _, ok := installNote(root, component.Component{
		Name: "fixture", Dir: "mod", Command: []string{"go", "build", "./..."},
	}, config.Default()); ok {
		t.Error("a command component with no package.json installs nothing, so there is no install to report on")
	}
	if _, ok := installNote(root, component.Component{
		Name: "ui", Dir: "packages/ui", Command: []string{"pnpm", "run", "build"},
	}, config.Default()); ok {
		t.Error("a command component under a workspace root is installed from it, so there is nothing to report")
	}
}

// A typescript.install override still runs somewhere, even with no lockfile
// to detect a workspace root from — and where it runs is exactly the fact
// detection would otherwise have reported, so the row stays.
func TestInstallNoteNamesWhereAnOverrideRuns(t *testing.T) {
	t.Run("resolves a workspace root", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
		write(t, root, "packages/ui/package.json", `{"name":"ui"}`)
		cfg := config.Default()
		cfg.TypeScript.Install = "pnpm install"
		row, ok := installNote(root, component.Component{
			Name: "ui", Dir: "packages/ui", Runner: runner.Vitest,
		}, cfg)
		if !ok {
			t.Fatal("an override that resolves a shared root is a fact worth reporting, not silence")
		}
		if row.Status != ui.StatusContext {
			t.Errorf("status = %q, want context: this describes where the install runs, not whether it succeeded", row.Status)
		}
		if !strings.Contains(row.Value, "workspace root") {
			t.Errorf("value = %q, want the resolved root named", row.Value)
		}
		if detail := strings.Join(row.Detail, " "); !strings.Contains(detail, "pnpm install") || !strings.Contains(detail, root) {
			t.Errorf("detail = %v, want the override command and the resolved root named", row.Detail)
		}
	})

	t.Run("resolves no root", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "web/package.json", `{"name":"web"}`)
		cfg := config.Default()
		cfg.TypeScript.Install = "true"
		row, ok := installNote(root, component.Component{
			Name: "web", Dir: "web", Runner: runner.Vitest,
		}, cfg)
		if !ok {
			t.Fatal("an override with no lockfile to resolve a root from still runs somewhere, worth naming")
		}
		if row.Status != ui.StatusContext {
			t.Errorf("status = %q, want context: this describes where the install runs, not whether it succeeded", row.Status)
		}
		if !strings.Contains(row.Value, "web") {
			t.Errorf("value = %q, want the component's own directory named", row.Value)
		}
		if detail := strings.Join(row.Detail, " "); !strings.Contains(detail, "true") || !strings.Contains(detail, filepath.Join(root, "web")) {
			t.Errorf("detail = %v, want the override command and its own directory named", row.Detail)
		}
	})
}

// installsNodeDeps answers for its own input rather than assuming
// component.Load already ran: a component naming neither a runner nor a
// command installs nothing, the same answer a real declaration could never
// produce since validateInvocation requires exactly one of the two.
func TestInstallsNodeDepsAnswersFalseWithNeitherRunnerNorCommand(t *testing.T) {
	if installsNodeDeps(t.TempDir(), component.Component{Name: "empty"}) {
		t.Error("a component naming neither a runner nor a command installs no node dependencies")
	}
}

// Teardown undoes what setup did, so it has to run on the path where setup
// failed halfway — a half-applied migration is exactly what needs undoing.
func TestTeardownRunsWhenSetupFails(t *testing.T) {
	root := goModule(t)
	marker := filepath.Join(root, "torn-down")
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Setup:    []string{"exit 7"},
		Teardown: []string{"touch " + marker},
	}), config.Default(), nil, false, nil)
	if row.Status != ui.StatusFail || row.Value != "setup failed" {
		t.Fatalf("row = %+v, want the setup named as the failure", row)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("teardown must run even when setup failed")
	}
}

// Leftover data makes the next run depend on the last one.
func TestTeardownRunsWhenTheSuiteFails(t *testing.T) {
	root := goModule(t)
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	marker := filepath.Join(root, "torn-down")
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"touch " + marker},
	}), config.Default(), nil, false, nil)
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
	root := goModule(t)
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}), config.Default(), nil, false, nil)
	if row.Status != ui.StatusFail || row.Value != "teardown failed" {
		t.Fatalf("row = %+v, want the teardown named", row)
	}
}

// It never masks a failure that already happened: the earlier one is what
// the reader has to act on.
func TestAFailingTeardownDoesNotMaskAFailingSuite(t *testing.T) {
	root := goModule(t)
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"no\") }\n")
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		Teardown: []string{"exit 4"},
	}), config.Default(), nil, false, nil)
	if row.Value != "failed" {
		t.Errorf("value = %q, want the suite failure to survive", row.Value)
	}
}

// Setup runs before the suite, not alongside it.
func TestSetupRunsBeforeTheSuite(t *testing.T) {
	root := goModule(t)
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
		// The suite reads what setup wrote, so it can only pass if the
		// ordering holds.
		Setup: []string{"echo 1 > setup-ran"},
		Args:  []string{"-run", "TestFoo", "./..."},
	}), config.Default(), nil, false, nil)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v", row)
	}
	if _, err := os.Stat(filepath.Join(root, "mod", "setup-ran")); err != nil {
		t.Error("setup must run in the component directory, before the suite")
	}
}

// The cause has to be next to the verdict: a reader looking at a red row must
// not have to scroll past another component's container lifecycle to find out
// what happened, which is what a real CI log does to them.
func TestAFailingComponentCarriesTheCauseAndTheLog(t *testing.T) {
	root := goModule(t)
	write(t, root, "mod/fail_test.go", "package fixture\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"the cause\") }\n")
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}), config.Default(), nil, false, nil)

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
	root := goModule(t)
	row, _ := runComponent(context.Background(), root, planFor(t, root, component.Component{
		Name: "fixture", Dir: "mod", Runner: runner.GoTest,
	}), config.Default(), nil, false, nil)
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

// planFor builds the plan runComponent takes, writing to a log under root the
// way a command's own opener would. Every component in these tests declares no
// services, so nothing is probed and no stack is loaded.
func planFor(t *testing.T, root string, c component.Component) Plan {
	t.Helper()
	return Plan{C: c, Out: openFile(t, root, c.Name), Ready: true}
}

// A report that was asked for and did not arrive says why. A component
// contributing no test counts is indistinguishable, in a history, from one
// that ran no tests — and the commonest cause is a repository whose own runner
// configuration sent the report somewhere lydite does not look, which is a
// thing its author can fix once they are told.
func TestWithTestCountsSaysWhyAReportIsMissing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "empty.xml", `<?xml version="1.0"?><testsuites tests="0"></testsuites>`)
	write(t, dir, "real.xml", `<testsuites><testsuite><testcase name="a"/><testcase name="b"><failure/></testcase></testsuite></testsuites>`)

	for _, tc := range []struct {
		name   string
		report string
		want   *junit.Counts
		why    string
	}{
		// Silent, not a reason: nothing was expected, so nothing is missing,
		// and a line per such component on every run is how a diagnostic
		// teaches its reader to skim past it.
		{"a runner that writes no report", "", nil, ""},
		{"a report that was never written", "absent.xml", nil, "the test report was not written"},
		// Nought tests is a runner that collected nothing, not a suite that
		// passed everything.
		{"a report holding no test", "empty.xml", nil, "the test report holds no test"},
		{"a report with tests", "real.xml", &junit.Counts{Total: 2, Failed: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := measure.Measurement{Name: "svc"}
			withTestCounts(&m, dir, runner.Invocation{JUnitReport: tc.report})
			if tc.want == nil && m.Tests != nil {
				t.Fatalf("Tests = %+v, want none", m.Tests)
			}
			if tc.want != nil && (m.Tests == nil || *m.Tests != *tc.want) {
				t.Fatalf("Tests = %+v, want %+v", m.Tests, tc.want)
			}
			if tc.why == "" && m.TestsWhy != "" {
				t.Errorf("TestsWhy = %q, want nothing to report", m.TestsWhy)
			}
			if tc.why != "" && !strings.Contains(m.TestsWhy, tc.why) {
				t.Errorf("TestsWhy = %q, want it to say %q", m.TestsWhy, tc.why)
			}
		})
	}
}
