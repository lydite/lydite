package run

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/flaky"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// flakyProbeRepo is a repository whose one component holds the flakyprobe
// fixture: the merge-base's tree at origin/main, and HEAD's — which declares
// three tests the base does not — on the branch.
func flakyProbeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	write(t, root, component.FileName,
		"components:\n  - name: probe\n    dir: probe\n    runner: go-test\n    args: [\"./...\"]\n")
	copyTree(t, filepath.Join("..", "..", "flaky", "testdata", "flakyprobe", "base"), root, "probe")
	gitIn(t, origin, "init", "--quiet", "--bare", "-b", "main")
	gitIn(t, root, "init", "--quiet", "-b", "main")
	gitIn(t, root, "config", "user.email", "t@example.com")
	gitIn(t, root, "config", "user.name", "t")
	gitIn(t, root, "remote", "add", "origin", origin)
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "--quiet", "-m", "base")
	gitIn(t, root, "push", "--quiet", "origin", "main")
	copyTree(t, filepath.Join("..", "..", "flaky", "testdata", "flakyprobe", "head"), root, "probe")
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

// A change that introduces no test pays nothing and says so, which is the
// common case and must not read like a gate that could not run.
//
// A tree that is its own merge-base is that case at its widest: there is no
// change at all, which is what the default branch runs on every push.
func TestTheFlakyGateSaysWhenAChangeIntroducesNoTest(t *testing.T) {
	root := flakyProbeRepo(t)
	gitIn(t, root, "push", "--quiet", "origin", "HEAD:refs/heads/tip")

	row := examineComponent(t, root, "tip", component.Component{
		Name: "probe", Dir: "probe", Runner: runner.GoTest,
	})
	if row.Status != ui.StatusPass || row.Value != "no new tests" {
		t.Errorf("flaky(probe) = %+v, want a pass saying the change introduced none", row)
	}
}

// A component's gate is about the tests it introduced. Handed the whole diff,
// every component would rerun every other component's new tests and report
// them under its own name.
func TestAComponentsGateSeesOnlyItsOwnChangedPaths(t *testing.T) {
	changed := []string{"moda/x_test.go", "modb/x_test.go", "modb/deep/y_test.go", "README.md"}
	got := componentPaths("modb", changed)
	want := []string{"modb/x_test.go", "modb/deep/y_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("componentPaths(modb) = %v, want %v", got, want)
	}
	// A component rooted at the scan root owns every path in the change, and a
	// prefix match would drop them all.
	if got := componentPaths(".", changed); len(got) != len(changed) {
		t.Errorf("componentPaths(.) = %v, want the whole change", got)
	}
	// Names are not prefixes: `modb-tools` is another component.
	if got := componentPaths("modb", []string{"modb-tools/x_test.go"}); len(got) != 0 {
		t.Errorf("componentPaths(modb) = %v, want nothing from a component whose name starts alike", got)
	}
}

// A repository that asked for the gate and got silence anywhere must be able
// to see where. jest is the one runner with a reason of its own — it ships no
// JUnit reporter and lydite will install none into the workspace it is about
// to gate. A raw `command:` opts out of the derived variants the same way, and
// a runner lydite has never heard of says so generically.
func TestTheFlakyGateNamesWhatItCannotExamine(t *testing.T) {
	root := gitRepo(t, map[string]string{"web/package.json": `{"name":"web"}`})
	for _, tc := range []struct {
		name string
		c    component.Component
		want string
	}{
		{
			name: "a jest component",
			c:    component.Component{Name: "web", Dir: "web", Runner: runner.Jest},
			want: "jest has no JUnit output lydite will install",
		},
		{
			name: "a raw command",
			c:    component.Component{Name: "web", Dir: "web", Command: []string{"make", "test"}},
			want: "raw command",
		},
		{
			name: "an unknown runner",
			c:    component.Component{Name: "web", Dir: "web", Runner: "bogus"},
			want: "no runner lydite knows",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := examineComponent(t, root, "HEAD", tc.c)
			if row.Status != ui.StatusUnmeasured {
				t.Fatalf("row = %+v, want unmeasured: a gate that could not run is not one that passed", row)
			}
			if !strings.Contains(row.Value, tc.want) {
				t.Errorf("value = %q, want it to name %q", row.Value, tc.want)
			}
		})
	}
}

// There is no "new" without a merge-base, so the whole row is unmeasured and
// names the cause — never a pass over a shallow checkout.
func TestAnUnresolvableMergeBaseLeavesTheFlakyRowUnmeasured(t *testing.T) {
	root := flakyProbeRepo(t)
	row := examineComponent(t, root, "0000000000000000000000000000000000000000", component.Component{
		Name: "probe", Dir: "probe", Runner: runner.GoTest,
	})
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured", row)
	}
	if !strings.Contains(row.Value, "merge-base") {
		t.Errorf("value = %q, want the unresolvable revision named as the cause", row.Value)
	}
}

// The merge-base resolves and the change introduces real tests, but the
// invocation names no JUnit report at all — the shape a raw variant that was
// never asked for the gate would leave behind. There is no first outcome to
// read, so the row says that rather than guessing one.
func TestExamineNamesAnInvocationThatWroteNoReport(t *testing.T) {
	root := flakyProbeRepo(t)
	row, found := examineWithInvocation(t, root, "", component.Component{
		Name: "probe", Dir: "probe", Runner: runner.GoTest,
	}, runner.Invocation{})
	if row.Status != ui.StatusUnmeasured || len(found) != 0 {
		t.Fatalf("row = %+v, findings = %+v; want unmeasured and no claim", row, found)
	}
	if !strings.Contains(row.Value, "no test report") {
		t.Errorf("value = %q, want the missing report named as the cause", row.Value)
	}
}

// The merge-base resolves and the change introduces real tests, and the
// invocation names a report — but the suite's own run did not write it. The
// gate's first outcome is missing, not zero, and the row says so rather than
// reading a passing suite's silence as agreement.
func TestExamineNamesASuiteReportThatCannotBeRead(t *testing.T) {
	root := flakyProbeRepo(t)
	row, found := examineWithInvocation(t, root, "", component.Component{
		Name: "probe", Dir: "probe", Runner: runner.GoTest,
	}, runner.Invocation{JUnitReport: "nothing-wrote-this.xml"})
	if row.Status != ui.StatusUnmeasured || len(found) != 0 {
		t.Fatalf("row = %+v, findings = %+v; want unmeasured and no claim", row, found)
	}
	if !strings.Contains(row.Value, "could not be read") {
		t.Errorf("value = %q, want the unreadable report named as the cause", row.Value)
	}
}

// A changed test file that does not parse cannot be told to declare a name or
// not, so the whole component is unmeasured rather than silently missing
// whatever the broken file would have contributed.
func TestExamineNamesATestFileThatDoesNotParse(t *testing.T) {
	root := flakyProbeRepo(t)
	commitChange(t, root, "probe/broken_test.go", "package flakyprobe\n\nfunc TestBroken(t *testing.T) {\n")
	row, found := examineWithInvocation(t, root, "", component.Component{
		Name: "probe", Dir: "probe", Runner: runner.GoTest,
	}, runner.Invocation{JUnitReport: "absent.xml"})
	if row.Status != ui.StatusUnmeasured || len(found) != 0 {
		t.Fatalf("row = %+v, findings = %+v; want unmeasured and no claim", row, found)
	}
	if !strings.Contains(row.Value, "could not be read") {
		t.Errorf("value = %q, want the parse failure named as the cause", row.Value)
	}
}

// A component whose suite never started still takes a row: its new tests
// were not examined, and a gate that could not run must never render as one
// that passed by simply having no row at all.
func TestReportNamesAComponentWhoseSuiteNeverRan(t *testing.T) {
	g := NewFlakyGate(t.Context(), t.TempDir(), "", true)
	rows, found := g.Report([]component.Component{{Name: "never-ran", Runner: runner.GoTest}})
	if len(rows) != 1 || len(found) != 0 {
		t.Fatalf("rows = %+v, findings = %+v; want one row and no claim", rows, found)
	}
	row := rows[0]
	if row.Label != FlakyLabel("never-ran") || row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured: the gate never examined this component", row)
	}
	if !strings.Contains(row.Value, "did not run") {
		t.Errorf("value = %q, want the reason named", row.Value)
	}
}

// outcomeOf names an outcome a report may not have recorded at all: a nil
// pointer is what compare leaves behind for exactly that case, and it is
// named plainly rather than dereferenced.
func TestOutcomeOfNamesAnAbsentOutcome(t *testing.T) {
	if got := outcomeOf(nil); got != "absent" {
		t.Errorf("outcomeOf(nil) = %q, want %q", got, "absent")
	}
}

// Every new test agreeing with itself is the ordinary case, and the row says
// so in the same voice a passing suite does — a count and what was done, not
// a claim about anything that disagreed.
func TestTheFlakyRowPassesWhenEveryNewTestAgrees(t *testing.T) {
	pass := junit.Pass
	c := component.Component{Name: "svc", Dir: "svc"}
	results := []flaky.Result{
		{Test: flaky.Test{Scope: "svc", Name: "TestA", Path: "svc/x_test.go", Line: 3},
			Verdict: flaky.Agreed, Run1: &pass, Run2: &pass},
		{Test: flaky.Test{Scope: "svc", Name: "TestB", Path: "svc/x_test.go", Line: 9},
			Verdict: flaky.Agreed, Run1: &pass, Run2: &pass},
	}
	row, found := flakyRow(FlakyLabel(c.Name), c, results)
	if row.Status != ui.StatusPass {
		t.Fatalf("row = %+v, want a pass: every new test agreed with itself", row)
	}
	if !strings.Contains(row.Value, "2 new test(s), 2 runs each") {
		t.Errorf("value = %q, want the count and what was done", row.Value)
	}
	if len(found) != 0 {
		t.Errorf("findings = %+v, want none: nothing disagreed", found)
	}
}

// The strongest verdict owns the row and the weaker one is still said: a run
// holding both a disagreement and a test nothing could examine fails, with the
// unexamined count in the detail.
func TestTheFlakyRowReportsTheStrongestVerdictAndSaysTheRest(t *testing.T) {
	pass, fail := junit.Pass, junit.Fail
	c := component.Component{Name: "svc", Dir: "svc"}
	results := []flaky.Result{
		{Test: flaky.Test{Scope: "svc", Name: "TestAgrees", Path: "svc/x_test.go", Line: 3},
			Verdict: flaky.Agreed, Run1: &pass, Run2: &pass},
		{Test: flaky.Test{Scope: "svc", Name: "TestDisagrees", Path: "svc/x_test.go", Line: 9},
			Verdict: flaky.Disagreed, Run1: &pass, Run2: &fail, Command: "gotestsum -- -run '^(TestDisagrees)$' -count=1 ."},
		{Test: flaky.Test{Scope: "svc", Name: "TestUnrun", Path: "svc/x_test.go", Line: 15},
			Verdict: flaky.Unmeasured, Why: "run 1's report does not record it"},
	}

	row, found := flakyRow(FlakyLabel(c.Name), c, results)
	if row.Status != ui.StatusFail {
		t.Fatalf("row = %+v, want the disagreement to own the row", row)
	}
	if !strings.Contains(row.Value, "1 of 3") {
		t.Errorf("value = %q, want the disagreement counted against every new test", row.Value)
	}
	detail := strings.Join(row.Detail, "\n")
	if !strings.Contains(detail, "TestUnrun was not examined: run 1's report does not record it") {
		t.Errorf("detail = %q, want the unexamined test still said", detail)
	}
	if len(found) != 1 || found[0].Line != 9 || found[0].Ordinal != 0 {
		t.Fatalf("findings = %+v, want one on the disagreement's declaration", found)
	}

	// Nothing measurable at all is unmeasured rather than a pass over tests
	// nobody looked at.
	row, found = flakyRow(FlakyLabel(c.Name), c, results[2:])
	if row.Status != ui.StatusUnmeasured || len(found) != 0 {
		t.Errorf("row = %+v, findings = %+v; want an unmeasured row and no claim", row, found)
	}

	// One test agreed and one could not be measured, and neither disagreed:
	// a pass here would count the unexamined test among the ones that were,
	// and a gate that examined part of the change must not render as one
	// that examined all of it.
	row, found = flakyRow(FlakyLabel(c.Name), c, []flaky.Result{results[0], results[2]})
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured: an agreement and an unexamined test is not a pass", row)
	}
	if !strings.Contains(row.Value, "1 of 2") {
		t.Errorf("value = %q, want the unexamined test counted against every new test", row.Value)
	}
	if len(found) != 0 {
		t.Errorf("findings = %+v, want none: nothing disagreed", found)
	}

	skippedBoth := flaky.Result{Test: flaky.Test{Scope: "svc", Name: "TestSkippedBoth", Path: "svc/x_test.go", Line: 21},
		Verdict: flaky.Skipped}

	// Every new test skipped in both runs is nothing rerun twice, not a pass:
	// the gate examined none of the change and must say so.
	row, found = flakyRow(FlakyLabel(c.Name), c, []flaky.Result{skippedBoth})
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured: a test skipped in both runs was never actually rerun", row)
	}
	if len(found) != 0 {
		t.Errorf("findings = %+v, want none", found)
	}

	// One test agreed and one was skipped in both runs, and neither
	// disagreed: a pass here would count the skipped test among the ones
	// that actually ran twice.
	row, found = flakyRow(FlakyLabel(c.Name), c, []flaky.Result{results[0], skippedBoth})
	if row.Status != ui.StatusUnmeasured {
		t.Fatalf("row = %+v, want unmeasured: an agreement and a skipped-both test is not a pass", row)
	}
	if !strings.Contains(row.Value, "1 of 2") {
		t.Errorf("value = %q, want the skipped test counted against every new test", row.Value)
	}
	if len(found) != 0 {
		t.Errorf("findings = %+v, want none: nothing disagreed", found)
	}
}

// examineComponent is one component's verdict, without the suite run that
// would precede it: the row is decided before anything is rerun for every case
// that cannot be examined at all.
func examineComponent(t *testing.T, root, base string, c component.Component) ui.Row {
	t.Helper()
	row, found := examineWithInvocation(t, root, base, c, runner.Invocation{JUnitReport: "absent.xml"})
	if len(found) != 0 {
		t.Fatalf("findings = %+v, want none: nothing was rerun", found)
	}
	return row
}

// examineWithInvocation is examineComponent with the invocation exposed, for
// the cases that turn on what the suite's own run reported rather than on
// what the component or the merge-base is.
func examineWithInvocation(t *testing.T, root, base string, c component.Component, inv runner.Invocation) (ui.Row, []finding.Finding) {
	t.Helper()
	g := NewFlakyGate(t.Context(), root, base, true)
	return g.examine(t.Context(), root, filepath.Join(root, filepath.FromSlash(c.Dir)), c, inv, nil, openFile(t, root, c.Name))
}

// The gate covers the four runners that write a JUnit report lydite installs
// nothing into the repository to obtain. jest is outside it by decision, an
// unknown runner by absence, and a raw `command:` by opting out of the
// derived variants entirely.
func TestTheFlakyGateGatesTheRunnersItCanExamine(t *testing.T) {
	g := &FlakyGate{requested: true}
	for _, tc := range []struct {
		c    component.Component
		want bool
	}{
		{component.Component{Runner: runner.GoTest}, true},
		{component.Component{Runner: runner.CargoNextest}, true},
		{component.Component{Runner: runner.Vitest}, true},
		{component.Component{Runner: runner.Jest}, false},
		{component.Component{Runner: runner.CargoLLVMCovNextest}, true},
		{component.Component{Runner: "bogus"}, false},
		{component.Component{Runner: runner.Vitest, Command: []string{"make", "test"}}, false},
	} {
		if got := g.gates(tc.c); got != tc.want {
			t.Errorf("gates(%q, command %v) = %v, want %v", tc.c.Runner, tc.c.Command, got, tc.want)
		}
	}
	// The flag is never inferred: a run nobody asked to gate gates nothing.
	if (&FlakyGate{}).gates(component.Component{Runner: runner.GoTest}) {
		t.Error("a run without --gate-flaky gated a component anyway")
	}
}

// Asking for the gate makes the plain run write the report run 1's outcomes
// are read from, whichever of the three languages the component is.
func TestAskingForTheGateMakesThePlainRunWriteAReport(t *testing.T) {
	gate := &FlakyGate{requested: true}
	for _, name := range []runner.Name{runner.GoTest, runner.CargoNextest, runner.Vitest} {
		c := component.Component{Runner: name}
		inv, err := invocationFor(c, runner.Plain, gate)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if inv.JUnitReport == "" {
			t.Errorf("%s writes no report under the gate, so there is no first outcome to read", name)
		}
		// A run nobody asked to gate keeps the bare variant: a report written
		// and discarded is a process in the way of the thing being timed.
		if ungated, err := invocationFor(c, runner.Plain, &FlakyGate{}); err != nil || ungated.JUnitReport != "" {
			t.Errorf("%s ungated = %+v (%v), want the plain variant", name, ungated, err)
		}
	}
}

// Each language's second run is built from the scope's own tests, by the
// closure the gate hands internal/flaky — which is what keeps that package
// ignorant of any runner's argv.
func TestEachLanguagesRerunIsBuiltFromItsScopesTests(t *testing.T) {
	rust := []flaky.Test{
		{Scope: ".", Classname: "nextestprobe::a", Name: "shared_name", Path: "tests/a.rs", Line: 2},
		{Scope: ".", Classname: "nextestprobe::b", Name: "shared_name", Path: "tests/a.rs", Line: 2},
	}
	identity, build := flakyRerunner(component.Component{Runner: runner.CargoNextest, Dir: "."})
	if identity != flaky.ByClassAndName {
		t.Errorf("a Rust component is identified %v, want by classname and name: `shared_name` is two tests", identity)
	}
	inv, ok := build(".", rust)
	if !ok {
		t.Skip("no cache directory to stage the rerun's tool config in")
	}
	// One term per name and not per test: an exact nextest predicate names a
	// test and not a binary, so one term already selects both binaries.
	if argv := strings.Join(inv.Args, " "); !strings.Contains(argv, "test(=shared_name)") ||
		strings.Count(argv, "test(=shared_name)") != 1 {
		t.Errorf("the Rust rerun's argv = %q, want one exact term for the name", argv)
	}

	ts := []flaky.Test{
		{Scope: ".", Classname: "src/one.test.ts", Name: "matches ^a (b) [c] + d$", Path: "src/one.test.ts", Line: 19},
		{Scope: ".", Classname: "src/two.test.ts", Name: "shared title", Path: "src/two.test.ts", Line: 3},
	}
	identity, build = flakyRerunner(component.Component{Runner: runner.Vitest, Dir: "."})
	if identity != flaky.ByClassAndName {
		t.Errorf("a TypeScript component is identified %v, want by classname and name", identity)
	}
	inv, ok = build(".", ts)
	if !ok {
		t.Fatal("the TypeScript builder supplied no invocation")
	}
	argv := strings.Join(inv.Args, " ")
	// The files come from the classname, which is the path vitest reported the
	// test under and the shape it resolves a positional argument in.
	for _, file := range []string{"src/one.test.ts", "src/two.test.ts"} {
		if !strings.Contains(argv, file) {
			t.Errorf("the vitest rerun's argv = %q, want it to name %s", argv, file)
		}
	}
	// Every metacharacter escaped: a title is prose, and a pattern built
	// verbatim from one does not match the title it came from.
	if !strings.Contains(argv, `matches \^a \(b\) \[c\] \+ d\$`) {
		t.Errorf("the vitest rerun's argv = %q, want the title's metacharacters escaped", argv)
	}

	identity, build = flakyRerunner(component.Component{Runner: runner.GoTest, Dir: "svc"})
	if identity != flaky.ByName {
		t.Errorf("a Go component is identified %v, want by name: one process runs one package", identity)
	}
	inv, ok = build("svc/internal/x", []flaky.Test{{Scope: "svc/internal/x", Name: "TestOne"}})
	if !ok {
		t.Fatal("the Go builder supplied no invocation")
	}
	if argv := strings.Join(inv.Args, " "); !strings.Contains(argv, "^(TestOne)$") || !strings.Contains(argv, "./internal/x") {
		t.Errorf("the Go rerun's argv = %q, want the anchored filter and the package relative to the component", argv)
	}
	// A package that cannot be located inside the component supplies no
	// invocation, rather than a pattern go test would misread.
	if _, ok := build("/etc", []flaky.Test{{Scope: "/etc", Name: "TestOne"}}); ok {
		t.Error("the Go builder supplied an invocation for a package outside the component")
	}
}

// A package below the component directory is addressed relative to it, since
// that is where the rerun runs and what `go test` takes there.
func TestAPackageIsAddressedRelativeToTheComponent(t *testing.T) {
	for _, tc := range []struct{ dir, pkg, want string }{
		{".", ".", "."},
		{".", "pkg", "./pkg"},
		{"source/cli", "source/cli", "."},
		{"source/cli", "source/cli/internal/flaky", "./internal/flaky"},
		{"", "pkg/sub", "./pkg/sub"},
	} {
		got, err := relPackage(tc.dir, tc.pkg)
		if err != nil {
			t.Fatalf("relPackage(%q, %q): %v", tc.dir, tc.pkg, err)
		}
		if got != tc.want {
			t.Errorf("relPackage(%q, %q) = %q, want %q", tc.dir, tc.pkg, got, tc.want)
		}
	}
}

// relPackage is asked for a pattern only when a package directory is a real
// scan-root-relative path, but the check exists because nothing upstream of
// it enforces that: an absolute one cannot be made relative to the
// component's own relative directory, and filepath.Rel says so rather than
// producing a pattern go test would misread.
func TestRelPackageNamesAPathItCannotRelate(t *testing.T) {
	pattern, err := relPackage(".", "/etc/passwd")
	if err == nil {
		t.Fatal("relPackage accepted a package an absolute path could not be made relative to a relative directory")
	}
	if pattern != "" {
		t.Errorf("pattern = %q, want none: an error carries no pattern to be misread as one", pattern)
	}
}

// A row names a test the way its report does, so two same-named tests in one
// component are two lines an author can tell apart — and a declaration no
// parser could name is called by the only thing identifying it, where it sits.
func TestTheFlakyRowNamesATestTheWayItsReportDoes(t *testing.T) {
	pass, fail := junit.Pass, junit.Fail
	c := component.Component{Name: "crate", Dir: "."}
	results := []flaky.Result{
		{Test: flaky.Test{Scope: ".", Classname: "nextestprobe::a", Name: "shared_name", Path: "tests/a.rs", Line: 2},
			Verdict: flaky.Disagreed, Run1: &pass, Run2: &fail, Command: "cargo nextest run --profile rerun"},
		{Test: flaky.Test{Scope: ".", Classname: "nextestprobe::b", Name: "shared_name", Path: "tests/a.rs", Line: 2},
			Verdict: flaky.Disagreed, Run1: &fail, Run2: &pass, Command: "cargo nextest run --profile rerun"},
		{Test: flaky.Test{Scope: ".", Path: "src/one.test.ts", Line: 28, Unreadable: true},
			Verdict: flaky.Unmeasured, Why: "a test is declared here whose name a parser could not read"},
	}
	row, found := flakyRow(FlakyLabel(c.Name), c, results)
	if row.Status != ui.StatusFail || !strings.Contains(row.Value, "2 of 3") {
		t.Fatalf("row = %+v, want both disagreements counted against every new test", row)
	}
	detail := strings.Join(row.Detail, "\n")
	for _, want := range []string{
		"nextestprobe::a shared_name: run 1 passed, run 2 failed",
		"nextestprobe::b shared_name: run 1 failed, run 2 passed",
		"the test declared at src/one.test.ts:28 was not examined",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want %q", detail, want)
		}
	}
	// Two findings on one line of one file, told apart by the binary each ran
	// in: a site shared between them would be one claim published twice.
	if len(found) != 2 {
		t.Fatalf("findings = %+v, want one per disagreement", found)
	}
	if found[0].Site == found[1].Site {
		t.Errorf("both findings claim site %q, and nothing distinguishes the two tests", found[0].Site)
	}
	if found[0].Site != ". nextestprobe::a shared_name" {
		t.Errorf("site = %q, want the scope, the binary and the name", found[0].Site)
	}
}
