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
	{Package: ".", Name: "TestDeterministicNew", Path: "probe_test.go", Line: 23},
	{Package: ".", Name: "TestFlakyNew", Path: "probe_test.go", Line: 32},
	{Package: ".", Name: "TestNewSubtests", Path: "probe_test.go", Line: 44},
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
		Root: dir, Dir: ".", Run1: run1, Env: env, Log: &log,
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
	got, err := Rerun(t.Context(), probeTests, Options{Root: dir, Dir: ".", Run1: run1, Env: env})
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
		Root: dir, Dir: ".", Env: env,
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
	absent := []Test{{Package: ".", Name: "TestNotThere", Path: "probe_test.go", Line: 1}}
	got, err := Rerun(t.Context(), absent, Options{
		Root: dir, Dir: ".", Env: env,
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
		Root: dir, Dir: ".",
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
			test := Test{Package: ".", Name: "TestX"}
			got := compare(test,
				map[string]junit.Outcome{"TestX": tc.one},
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
		map[string]junit.Outcome{"TestX": junit.Fail},
		map[string]junit.Outcome{"TestX": junit.Fail}, "", "")
	if got.Verdict != Agreed {
		t.Errorf("two failures = %s, want %s", got.Verdict, Agreed)
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

// One invocation per package holding a new test — not one per test, and not
// one for the whole component.
func TestOnePackageIsRerunOnce(t *testing.T) {
	got := packages([]Test{
		{Package: "a", Name: "TestOne"},
		{Package: "a", Name: "TestTwo"},
		{Package: "b", Name: "TestThree"},
	})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("packages = %v, want [a b]", got)
	}
	if names := namesIn([]Test{{Package: "a", Name: "TestOne"}, {Package: "b", Name: "TestTwo"}}, "a"); len(names) != 1 {
		t.Errorf("namesIn = %v, want TestOne alone", names)
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
	if err := r.Prepare(context.Background(), inv, dir, "", executil.Env{}, io.Discard); err != nil {
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
		Test{Package: ".", Name: "TestGhost", Path: "probe_test.go", Line: 1})
	got, err := Rerun(t.Context(), ghost, Options{
		Root: dir, Dir: ".", Env: env,
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

// relPackage is asked for a pattern only when a package directory is a real
// scan-root-relative path, but the check exists because nothing upstream of
// it enforces that: an absolute one cannot be made relative to the
// component's own relative directory, and filepath.Rel says so rather than
// producing a pattern go test would misread.
func TestRelPackageNamesAPathItCannotRelate(t *testing.T) {
	if _, err := relPackage(".", "/etc/passwd"); err == nil {
		t.Fatal("relPackage accepted a package an absolute path could not be made relative to a relative directory")
	}
}

// Rerun asks relPackage the same question for real, and refuses to build an
// invocation over a package it could not locate rather than guessing a
// pattern from the raw directory.
func TestRerunRefusesAPackageItCannotLocate(t *testing.T) {
	_, err := Rerun(t.Context(), []Test{{Package: "/etc", Name: "TestX", Path: "x_test.go", Line: 1}},
		Options{Root: t.TempDir(), Dir: "."})
	if err == nil {
		t.Fatal("Rerun accepted a package it could not locate inside the component")
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
	_, why := rerunPackage(context.Background(), dir, runner.Invocation{JUnitReport: "junit.xml"}, Options{})
	if !strings.Contains(why, "could not be removed") {
		t.Errorf("why = %q, want the leftover report named as the cause", why)
	}
}
