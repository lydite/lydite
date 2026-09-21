package flaky

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// probeTests is what NewTests reports over the flakyprobe fixture, which
// TestTheProbeAtTwoRevisions asserts against the two revisions. Restated here
// so the rerun can be exercised without a git repository standing behind it.
var probeTests = []Test{
	{Scope: ".", Name: "TestDeterministicNew", Path: "probe_test.go", Line: 23},
	{Scope: ".", Name: "TestFlakyNew", Path: "probe_test.go", Line: 32},
	{Scope: ".", Name: "TestNewSubtests", Path: "probe_test.go", Line: 44},
}

// TestTwoRunsOfTheProbeDisagreeAboutOneTest is the gate end to end over the
// fixture ADR 0039 decides on: a suite run that writes a report, a filtered
// rerun in a process of its own, and the comparison of the two.
//
// TestFlakyNew passes in the process that finds no marker and fails in the one
// that does, so the two runs disagree about it and agree about everything else
// — including TestNewSubtests, whose parent record aggregates the children it
// names at runtime.
func TestTwoRunsOfTheProbeDisagreeAboutOneTest(t *testing.T) {
	dir, env := probe(t)
	run1 := suiteRun(t, dir, env)
	if got := run1["TestFlakyNew"]; got != junit.Pass {
		t.Fatalf("run 1 recorded TestFlakyNew as %v, and the probe passes in the process that leaves the marker", got)
	}

	var log bytes.Buffer
	got, err := Rerun(t.Context(), probeTests, Options{
		Root: dir, Dir: ".", Run1: run1, Env: env, Log: &log, Build: goRerun(nil),
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	want := map[string]Verdict{
		"TestDeterministicNew": Agreed,
		"TestFlakyNew":         Disagreed,
		"TestNewSubtests":      Agreed,
	}
	if len(got) != len(want) {
		t.Fatalf("Rerun returned %d results over %d tests", len(got), len(probeTests))
	}
	for _, r := range got {
		if r.Verdict != want[r.Test.Name] {
			t.Errorf("%s: %s, want %s (run 1 %v, run 2 %v, %s)",
				r.Test.Name, r.Verdict, want[r.Test.Name], show(r.Run1), show(r.Run2), r.Why)
		}
		if r.Command == "" {
			t.Errorf("%s carries no rerun command, so a finding cannot say how to reproduce it", r.Test.Name)
		}
	}

	// The disagreement carries both outcomes and the argv, which is the whole
	// of what the finding tells its author.
	flaky := result(t, got, "TestFlakyNew")
	if show(flaky.Run1) != "passed" || show(flaky.Run2) != "failed" {
		t.Errorf("TestFlakyNew: run 1 %v, run 2 %v, want passed then failed", show(flaky.Run1), show(flaky.Run2))
	}
	if !strings.Contains(flaky.Command, "-count=1") || !strings.Contains(flaky.Command, "TestFlakyNew") {
		t.Errorf("the rerun command %q names neither the filter nor -count=1", flaky.Command)
	}

	// The filter matched what it named. A `go test -run` that matches nothing
	// prints `ok` and reports a pass, so the rerun that ran the five cases —
	// three tests and the two subtests one of them names — is the evidence the
	// pattern was not a no-op.
	if !strings.Contains(log.String(), "DONE 5 tests") {
		t.Errorf("the rerun's output does not say it ran five cases:\n%s", log.String())
	}
	if strings.Contains(log.String(), "TestPreExistingStable") {
		t.Errorf("the rerun ran a test that is not new:\n%s", log.String())
	}
}

// A test the change did not introduce is never rerun and never compared. It is
// the whole point of the gate's budget, and the reason the comparison is
// driven by NewTests' output rather than by run 1's report.
func TestATestThatIsNotNewIsNeverConsidered(t *testing.T) {
	dir, env := probe(t)
	run1 := suiteRun(t, dir, env)
	if _, ok := run1["TestPreExistingStable"]; !ok {
		t.Fatal("run 1's report does not record TestPreExistingStable, so this test proves nothing")
	}
	got, err := Rerun(t.Context(), probeTests, Options{Root: dir, Dir: ".", Run1: run1, Env: env, Build: goRerun(nil)})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	for _, r := range got {
		if r.Test.Name == "TestPreExistingStable" {
			t.Errorf("the gate examined %s, which the change did not introduce", r.Test.Name)
		}
	}
}

// -count=1 is what makes the rerun a second sample. Without it the gate's argv
// is, run after run, the same cacheable command, and Go replays the earlier
// result rather than executing anything — the report it writes is the first
// run's own output, which is a tautology the gate would render as determinism.
//
// Proved by running the invocation twice rather than by reading its argv: a
// flag asserted only in a string comparison is one a reader can remove without
// anything going red. The flaky test is left out of the filter deliberately —
// it writes a file, and a run that writes is never cached, so the two that
// make the point are the ones that are.
func TestTheRerunDefeatsTheTestCache(t *testing.T) {
	dir, env := probe(t)
	names := []string{"TestDeterministicNew", "TestNewSubtests"}
	inv, ok := runner.GoRerun(nil, ".", names)
	if !ok {
		t.Fatal("GoRerun supplied no invocation")
	}
	for _, pass := range []string{"first", "second"} {
		got, out := execute(t, dir, env, inv)
		if strings.Contains(out, "(cached)") {
			t.Errorf("the %s rerun was served from the test cache, so it sampled nothing:\n%s", pass, out)
		}
		if got["TestDeterministicNew"] != junit.Pass {
			t.Errorf("the %s rerun recorded %v for TestDeterministicNew", pass, got)
		}
	}

	// The same invocation with the flag removed, which is what a change
	// "tidying" it away would leave. It is the control: without it, the
	// assertion above would hold over a machine whose cache is off and prove
	// nothing.
	uncounted := inv
	uncounted.Args = without(inv.Args, "-count=1")
	execute(t, dir, env, uncounted)
	if _, out := execute(t, dir, env, uncounted); !strings.Contains(out, "(cached)") {
		t.Errorf("a rerun without -count=1 was not cached, so nothing here proves the flag is what defeats it:\n%s", out)
	}
}

// A new test run 1's report does not record is unmeasured and says why.
// Decided by absence from the report rather than by a parser guessing at build
// tags, TestMain, or a package the component's argv excludes.
func TestATestAbsentFromRunOneIsUnmeasured(t *testing.T) {
	dir, env := probe(t)
	got, err := Rerun(t.Context(), probeTests, Options{
		Root: dir, Dir: ".", Env: env, Build: goRerun(nil),
		Run1: map[string]junit.Outcome{"TestDeterministicNew": junit.Pass},
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	r := result(t, got, "TestNewSubtests")
	if r.Verdict != Unmeasured {
		t.Errorf("TestNewSubtests: %s, want %s", r.Verdict, Unmeasured)
	}
	if !strings.Contains(r.Why, "run 1") {
		t.Errorf("the reason %q does not name the run whose report is missing it", r.Why)
	}
	if r.Run1 != nil {
		t.Errorf("an outcome was recorded for a run whose report holds none: %v", show(r.Run1))
	}
	if r.Run2 == nil {
		t.Error("the rerun's own outcome was dropped, and it is what the author can still see")
	}
}

// A test the rerun's report does not record is unmeasured too, and a `go test`
// that ran nothing is exactly the shape that would otherwise read as a pass.
func TestATestAbsentFromTheRerunIsUnmeasured(t *testing.T) {
	dir, env := probe(t)
	absent := []Test{{Scope: ".", Name: "TestNotThere", Path: "probe_test.go", Line: 1}}
	got, err := Rerun(t.Context(), absent, Options{
		Root: dir, Dir: ".", Env: env, Build: goRerun(nil),
		Run1: map[string]junit.Outcome{"TestNotThere": junit.Pass},
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if r := got[0]; r.Verdict != Unmeasured || r.Why == "" {
		t.Errorf("a test the rerun never ran = %s (%q), want %s with a reason", r.Verdict, r.Why, Unmeasured)
	}
}

// A rerun whose report cannot be read measures nothing, and says so per test
// rather than reporting the tests as agreeing. The component directory is not
// a Go module here, so the rerun fails before it writes anything.
func TestARerunThatWroteNoReportIsUnmeasured(t *testing.T) {
	dir := t.TempDir()
	got, err := Rerun(t.Context(), probeTests, Options{
		Root: dir, Dir: ".", Build: goRerun(nil),
		Run1: map[string]junit.Outcome{"TestFlakyNew": junit.Pass},
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	for _, r := range got {
		if r.Verdict != Unmeasured {
			t.Errorf("%s: %s, want %s", r.Test.Name, r.Verdict, Unmeasured)
		}
		if !strings.Contains(r.Why, "report") {
			t.Errorf("%s: the reason %q does not name the report that is missing", r.Test.Name, r.Why)
		}
	}
}

// Nothing new is nothing to run, and never an invocation whose filter matches
// nothing.
func TestNoNewTestRunsNothing(t *testing.T) {
	got, err := Rerun(t.Context(), nil, Options{Root: t.TempDir(), Dir: "."})
	if err != nil || got != nil {
		t.Errorf("Rerun over no tests = (%+v, %v), want (nil, nil)", got, err)
	}
}

// The comparison of two known outcomes, which is the rule the gate rests on: a
// skip is an outcome, so skipped in one run and run in the other disagrees,
// and skipped in both is an agreement that examined nothing.
func TestWhatTwoOutcomesEstablish(t *testing.T) {
	for _, tc := range []struct {
		name string
		one  junit.Outcome
		two  junit.Outcome
		want Verdict
	}{
		{"both passed", junit.Pass, junit.Pass, Agreed},
		{"both failed", junit.Fail, junit.Fail, Agreed},
		{"passed then failed", junit.Pass, junit.Fail, Disagreed},
		{"failed then passed", junit.Fail, junit.Pass, Disagreed},
		{"skipped then passed", junit.Skip, junit.Pass, Disagreed},
		{"passed then skipped", junit.Pass, junit.Skip, Disagreed},
		{"skipped then failed", junit.Skip, junit.Fail, Disagreed},
		{"both skipped", junit.Skip, junit.Skip, Skipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test := Test{Scope: ".", Name: "TestX"}
			got := compare(test,
				Options{Run1: map[string]junit.Outcome{"TestX": tc.one}},
				map[string]junit.Outcome{"TestX": tc.two},
				"", "gotestsum ...")
			if got.Verdict != tc.want {
				t.Errorf("%v then %v = %s, want %s", tc.one, tc.two, got.Verdict, tc.want)
			}
			if got.Run1 == nil || got.Run2 == nil || *got.Run1 != tc.one || *got.Run2 != tc.two {
				t.Errorf("the outcomes recorded are %v and %v, want %v and %v",
					show(got.Run1), show(got.Run2), tc.one, tc.two)
			}
		})
	}
}

// Two failures agree, and the gate says nothing about them: the suite row
// already failed on that test, and a second red row whose remedy is to fix the
// first is one defect reported twice.
func TestATestThatFailedTwiceAgrees(t *testing.T) {
	got := compare(Test{Name: "TestX"},
		Options{Run1: map[string]junit.Outcome{"TestX": junit.Fail}},
		map[string]junit.Outcome{"TestX": junit.Fail}, "", "")
	if got.Verdict != Agreed {
		t.Errorf("two failures = %s, want %s", got.Verdict, Agreed)
	}
}

// One invocation per scope holding a new test — not one per test, and not one
// for the whole component where the language groups more finely than that.
func TestOneScopeIsRerunOnce(t *testing.T) {
	got := scopes([]Test{
		{Scope: "a", Name: "TestOne"},
		{Scope: "a", Name: "TestTwo"},
		{Scope: "b", Name: "TestThree"},
	})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("scopes = %v, want [a b]", got)
	}
	names := runnableIn([]Test{{Scope: "a", Name: "TestOne"}, {Scope: "b", Name: "TestTwo"}}, "a", Options{})
	if len(names) != 1 || names[0].Name != "TestOne" {
		t.Errorf("runnableIn = %+v, want TestOne alone", names)
	}
}

// goRerun is the Build closure a Go component's gate supplies, over the
// component directories these tests use: the scope's own pattern, spelled the
// way cmd/go requires, and the names the scope contributes.
func goRerun(args []string) func(string, []Test) (runner.Invocation, bool) {
	return func(scope string, tests []Test) (runner.Invocation, bool) {
		pattern := "./" + scope
		if scope == "." {
			pattern = "."
		}
		names := make([]string, 0, len(tests))
		for _, t := range tests {
			names = append(names, t.Name)
		}
		return runner.GoRerun(args, pattern, names)
	}
}

// probe materialises the flakyprobe fixture at HEAD and composes the
// environment its runs need, skipping when the pinned wrapper cannot be
// installed — the one thing here that needs a network on a cold cache.
func probe(t *testing.T) (dir string, env []string) {
	t.Helper()
	dir = fixture.Tree(t, filepath.Join("testdata", "flakyprobe", "head"))
	inv, _ := runner.GoJUnitPlain(nil)
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("no go-test runner")
	}
	if err := r.Prepare(context.Background(), inv, dir, "", "", executil.Env{}, io.Discard); err != nil {
		t.Skipf("the pinned test wrapper is not installed and could not be fetched: %v", err)
	}
	return dir, toolchain.Compose(inv.PathDirs, nil)
}

// suiteRun is run 1: the plain variant through the wrapper, which is what
// asking for the gate makes a run do whichever variant it ran.
func suiteRun(t *testing.T, dir string, env []string) map[string]junit.Outcome {
	t.Helper()
	inv, ok := runner.GoJUnitPlain(nil)
	if !ok {
		t.Fatal("GoJUnitPlain supplied no invocation")
	}
	outcomes, _ := execute(t, dir, env, inv)
	return outcomes
}

// execute runs one invocation and reads the report it wrote, answering its
// output beside it. A non-zero exit is not a failure here: `go test` writes a
// valid report for a suite whose tests failed, and that is the run this gate
// most wants to read.
func execute(t *testing.T, dir string, env []string, inv runner.Invocation) (map[string]junit.Outcome, string) {
	t.Helper()
	report := filepath.Join(dir, filepath.FromSlash(inv.JUnitReport))
	if err := os.MkdirAll(filepath.Dir(report), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(report); err != nil {
		t.Fatal(err)
	}
	res := executil.RunOutput(t.Context(), dir, env, io.Discard, inv.Name, inv.Args...)
	out, err := junit.ReadOutcomesFile(report)
	if err != nil {
		t.Fatalf("reading %s: %v\n%s", inv.JUnitReport, err, res.Output)
	}
	return out, res.Output
}

// result is one test's result, failing the test when the gate returned none
// for it.
func result(t *testing.T, results []Result, name string) Result {
	t.Helper()
	for _, r := range results {
		if r.Test.Name == name {
			return r
		}
	}
	t.Fatalf("no result for %s", name)
	return Result{}
}

// show names an outcome a report may not have recorded at all.
func show(o *junit.Outcome) string {
	if o == nil {
		return "absent"
	}
	return o.String()
}

// without is argv with one argument removed, which is what dropping a flag
// from the rerun would leave behind.
func without(args []string, drop string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a != drop {
			out = append(out, a)
		}
	}
	return out
}

// A name neither report records, from a rerun that otherwise succeeded, is
// unmeasured for a reason distinct from either report alone missing it: this
// is a name that never existed anywhere, and the gate says so rather than
// picking one report's absence to blame.
func TestATestNeitherReportRecordsIsUnmeasured(t *testing.T) {
	dir, env := probe(t)
	ghost := append(append([]Test{}, probeTests...),
		Test{Scope: ".", Name: "TestGhost", Path: "probe_test.go", Line: 1})
	got, err := Rerun(t.Context(), ghost, Options{
		Root: dir, Dir: ".", Env: env, Build: goRerun(nil),
		Run1: map[string]junit.Outcome{"TestDeterministicNew": junit.Pass, "TestNewSubtests": junit.Pass},
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	r := result(t, got, "TestGhost")
	if r.Verdict != Unmeasured || r.Why != "neither run's report records it" {
		t.Errorf("TestGhost = %s (%q), want %s naming that neither report holds it", r.Verdict, r.Why, Unmeasured)
	}
	if r.Run1 != nil || r.Run2 != nil {
		t.Errorf("an outcome was recorded for a test neither report ran: run1=%v run2=%v", show(r.Run1), show(r.Run2))
	}
}

// A scope the caller's own builder refuses is an error and never a run: the
// builder is what knows whether a scope can be addressed at all — a package
// outside the component, a nextest invocation with nowhere to stage its tool
// config — and running an invocation it declined to build would filter for
// nothing and report a pass.
func TestRerunRefusesAScopeTheBuilderWillNotAddress(t *testing.T) {
	_, err := Rerun(t.Context(), []Test{{Scope: "/etc", Name: "TestX", Path: "x_test.go", Line: 1}},
		Options{Root: t.TempDir(), Dir: ".", Build: func(string, []Test) (runner.Invocation, bool) {
			return runner.Invocation{}, false
		}})
	if err == nil {
		t.Fatal("Rerun ran a scope its builder supplied no invocation for")
	}
}

// A report left by an earlier rerun is removed before the run, but a
// directory sitting at the report's own path is not a report — os.Remove
// refuses a non-empty directory, and rerunPackage says why rather than
// silently measuring nothing.
func TestARerunPackageWhoseReportPathIsAnOccupiedDirectoryIsUnmeasured(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "junit.xml")
	if err := os.Mkdir(report, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(report, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, why := rerunScope(context.Background(), dir, runner.Invocation{JUnitReport: "junit.xml"}, Options{})
	if !strings.Contains(why, "could not be removed") {
		t.Errorf("why = %q, want the leftover report named as the cause", why)
	}
}

// A name new in two nextest binaries is two new tests, examined
// independently: the identity is the classname and the name together, read
// back out of run 1's own report rather than computed from the crate's layout.
//
// Over ADR 0041's captures, `shared_name` runs in `nextestprobe::a` and
// `nextestprobe::b`. One of them disagrees here and the other does not, and
// neither answer is allowed to stand for both — a name-keyed map merges them
// and a flake in one binary is masked by a pass in the other.
func TestANameInTwoNextestBinariesIsTwoTests(t *testing.T) {
	run1 := captured(t, "nextest-suite.xml")
	run2 := failed(t, capture(t, "nextest-rerun.xml"), "nextestprobe::b", "shared_name")
	dir := t.TempDir()

	var filtered []string
	got, err := Rerun(t.Context(), []Test{
		{Scope: ".", Name: "shared_name", Path: "tests/a.rs", Line: 2},
		{Scope: ".", Name: "shared_name", Path: "tests/b.rs", Line: 2},
		{Scope: ".", Name: "tests::nested::doubles_deeper", Path: "src/lib.rs", Line: 18},
	}, Options{
		Root: dir, Dir: ".", Identity: ByClassAndName, Run1: run1,
		Build: staged(t, run2, &filtered),
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}

	want := map[string]Verdict{
		"nextestprobe::a shared_name":                Agreed,
		"nextestprobe::b shared_name":                Disagreed,
		"nextestprobe tests::nested::doubles_deeper": Agreed,
	}
	if len(got) != len(want) {
		t.Fatalf("Rerun returned %d results, want %d: %+v", len(got), len(want), got)
	}
	for _, r := range got {
		name := r.Test.Classname + " " + r.Test.Name
		if r.Verdict != want[name] {
			t.Errorf("%s: %s, want %s (run 1 %s, run 2 %s, %s)",
				name, r.Verdict, want[name], show(r.Run1), show(r.Run2), r.Why)
		}
	}

	// One declaration per name reached the filter, and the name once: an exact
	// nextest predicate names a test and not a binary, so `test(=shared_name)`
	// already selects both.
	if strings.Join(filtered, ",") != "shared_name,tests::nested::doubles_deeper" {
		t.Errorf("the rerun filtered for %v, want each new name once", filtered)
	}

	// Nothing ties a nextest binary to the file a test is declared in, so a
	// name declared twice anchors both claims to the first declaration rather
	// than guessing at cargo's target naming.
	for _, r := range got {
		if r.Test.Name == "shared_name" && r.Test.Path != "tests/a.rs" {
			t.Errorf("%s anchors to %s, want the first declaration of the name", r.Test.Classname, r.Test.Path)
		}
	}
}

// A vitest classname is the file's own path relative to the component, so a
// title declared in two files anchors each claim to the file it was reported
// in — and the rerun names those files, which is what vitest resolves a
// positional argument as.
func TestATitleInTwoVitestFilesAnchorsToEachFile(t *testing.T) {
	run1 := captured(t, "vitest-probe-suite.xml")
	run2 := failed(t, capture(t, "vitest-probe-rerun.xml"), "libs/probe/src/two.test.ts", "shared title")
	dir := t.TempDir()

	var filtered []string
	got, err := Rerun(t.Context(), []Test{
		{Scope: ".", Name: "shared title", Path: "libs/probe/src/one.test.ts", Line: 15},
		{Scope: ".", Name: "shared title", Path: "libs/probe/src/two.test.ts", Line: 3},
	}, Options{
		Root: dir, Dir: ".", Identity: ByClassAndName, Run1: run1,
		Build: staged(t, run2, &filtered),
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Rerun returned %d results, want one per file the title ran in: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Test.Classname != r.Test.Path {
			t.Errorf("%s anchors to %s, want the file it was reported in", r.Test.Classname, r.Test.Path)
		}
	}
	if v := result(t, got, "shared title").Verdict; v != Agreed {
		t.Errorf("one.test.ts's title = %s, want %s: only the other file's disagreed", v, Agreed)
	}
	two := byClassname(t, got, "libs/probe/src/two.test.ts")
	if two.Verdict != Disagreed {
		t.Errorf("two.test.ts's title = %s (%s), want %s", two.Verdict, two.Why, Disagreed)
	}
}

// A declaration whose name no parser can read is never rerun and never
// guessed at. It has no name to filter for, so it is counted as a test the
// gate went unable to examine — which is the whole of what a static parser can
// say about a `test.each` or an attribute macro.
func TestAnUnreadableDeclarationIsCountedAndNeverRerun(t *testing.T) {
	dir := t.TempDir()
	var filtered []string
	got, err := Rerun(t.Context(), []Test{
		{Scope: ".", Path: "libs/probe/src/one.test.ts", Line: 28, Unreadable: true},
		{Scope: ".", Name: "shared title", Path: "libs/probe/src/two.test.ts", Line: 3},
	}, Options{
		Root: dir, Dir: ".", Identity: ByClassAndName,
		Run1:  captured(t, "vitest-probe-suite.xml"),
		Build: staged(t, capture(t, "vitest-probe-rerun.xml"), &filtered),
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	r := got[0]
	if r.Verdict != Unmeasured || !strings.Contains(r.Why, "could not read") {
		t.Errorf("an unnameable declaration = %s (%q), want %s saying no parser could name it", r.Verdict, r.Why, Unmeasured)
	}
	if strings.Join(filtered, ",") != "shared title" {
		t.Errorf("the rerun filtered for %v, want the named test alone", filtered)
	}
}

// A new name run 1's report records under no classname at all is unmeasured
// and never rerun: there is no key the second report could be read back under,
// so the run would cost a process to establish the same nothing. An
// `#[ignore]` test is exactly that — nextest omits it from the report rather
// than recording it as skipped.
func TestANameRunOneRecordsUnderNoClassnameIsUnmeasured(t *testing.T) {
	dir := t.TempDir()
	var filtered []string
	got, err := Rerun(t.Context(), []Test{
		{Scope: ".", Name: "ignored_by_attribute", Path: "tests/c.rs", Line: 10},
		{Scope: ".", Name: "tests::nested::doubles_deeper", Path: "src/lib.rs", Line: 18},
	}, Options{
		Root: dir, Dir: ".", Identity: ByClassAndName,
		Run1:  captured(t, "nextest-suite.xml"),
		Build: staged(t, capture(t, "nextest-rerun.xml"), &filtered),
	})
	if err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	r := result(t, got, "ignored_by_attribute")
	if r.Verdict != Unmeasured || r.Why != "run 1's report does not record it" {
		t.Errorf("an ignored test = %s (%q), want %s naming run 1's silence", r.Verdict, r.Why, Unmeasured)
	}
	if strings.Join(filtered, ",") != "tests::nested::doubles_deeper" {
		t.Errorf("the rerun filtered for %v, want the test run 1 recorded alone", filtered)
	}
}

// scopeRelative treats an empty scope the same as ".": both mean the rerun
// runs at the component's own root, so a path is already relative to it. The
// paths here are not ones a real caller would pass — Test.Path never carries
// a leading "/" or "./" — but the function's own contract has to hold
// regardless of what any one caller happens to send it, and only a path with
// something to strip can tell "" and "." apart from a scope that expected a
// prefix and did not get it.
func TestScopeRelativeTreatsEmptyAndDotAsTheComponentsOwnRoot(t *testing.T) {
	for _, tc := range []struct {
		name, scope, path, want string
	}{
		{"an empty scope strips nothing", "", "/abs/path.ts", "/abs/path.ts"},
		{"a dot scope strips nothing", ".", "./file.ts", "./file.ts"},
		{"a real scope strips its own prefix", "libs/probe", "libs/probe/src/one.test.ts", "src/one.test.ts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopeRelative(tc.scope, tc.path); got != tc.want {
				t.Errorf("scopeRelative(%q, %q) = %q, want %q", tc.scope, tc.path, got, tc.want)
			}
		})
	}
}

// captured is one of the reports internal/junit holds, read in the key space a
// language whose names collide is compared in.
func captured(t *testing.T, name string) map[string]junit.Outcome {
	t.Helper()
	out, err := junit.ReadOutcomesByClass(strings.NewReader(capture(t, name)))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return out
}

// capture is one captured report's text.
func capture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "junit", "testdata", name)) // #nosec G304 -- a captured report this repository holds
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// failed is a captured report with one test marked failed, which is what the
// same run writes when that test disagrees. Derived from the capture so every
// other field is still the runner's own.
func failed(t *testing.T, doc, classname, name string) string {
	t.Helper()
	for _, open := range []string{
		`<testcase name="` + name + `" classname="` + classname + `"`,
		`<testcase classname="` + classname + `" name="` + escaped(name) + `"`,
	} {
		i := strings.Index(doc, open)
		if i < 0 {
			continue
		}
		j := strings.Index(doc[i:], ">")
		return doc[:i+j+1] + "<failure/>" + doc[i+j+1:]
	}
	t.Fatalf("no testcase for %s %s to fail", classname, name)
	return ""
}

// escaped is a name as the report spells it, where a runner XML-escapes what
// an author wrote.
func escaped(name string) string {
	return strings.ReplaceAll(name, ">", "&gt;")
}

// staged is a Build closure whose invocation writes doc where the rerun's
// report goes, recording what it was asked to filter for. The runners
// themselves are internal/runner's to build; what these cases turn on is what
// the second report holds.
func staged(t *testing.T, doc string, filtered *[]string) func(string, []Test) (runner.Invocation, bool) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "report.xml")
	if err := os.WriteFile(src, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(_ string, tests []Test) (runner.Invocation, bool) {
		seen := map[string]bool{}
		for _, test := range tests {
			if seen[test.Name] {
				continue
			}
			seen[test.Name] = true
			*filtered = append(*filtered, test.Name)
		}
		return runner.Invocation{Name: "cp", Args: []string{src, "rerun.xml"}, JUnitReport: "rerun.xml"}, true
	}
}

// byClassname is one result of a name several classnames hold.
func byClassname(t *testing.T, results []Result, classname string) Result {
	t.Helper()
	for _, r := range results {
		if r.Test.Classname == classname {
			return r
		}
	}
	t.Fatalf("no result under %s", classname)
	return Result{}
}
