package main

import (
	"os/exec"
	"path/filepath"

	"lydite/lydite/internal/component"
	"strings"
	"testing"
)

// affectedRepo is a scan root with a real origin to resolve a merge-base
// against, two buildable components and one dependency edge. The diff is made
// by committing, never by handing the command a path list: a fixture list of
// paths agrees with whatever the code does, and every question here is about
// what git actually reports.
func affectedRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	mod := func(name string) {
		write(t, root, name+"/go.mod", "module "+name+"\n\ngo 1.26\n")
		write(t, root, name+"/x.go", "package "+name+"\n\n// Foo is what the test exercises.\nfunc Foo() int { return 1 }\n")
		write(t, root, name+"/x_test.go", "package "+name+"\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\tif Foo() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	}
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: b\n    dir: modb\n    runner: go-test\n    args: [\"./...\"]\n"+
			"    depends_on: [a]\n    watch: [\"docs/spec.json\"]\n")
	mod("moda")
	mod("modb")
	write(t, root, "docs/spec.json", "{}\n")
	write(t, root, "README.md", "hello\n")

	run(origin, "init", "--quiet", "--bare", "-b", "main")
	run(root, "init", "--quiet", "-b", "main")
	run(root, "config", "user.email", "t@example.com")
	run(root, "config", "user.name", "t")
	run(root, "remote", "add", "origin", origin)
	run(root, "add", "-A")
	run(root, "commit", "--quiet", "-m", "base")
	run(root, "push", "--quiet", "origin", "main")
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

func commitChange(t *testing.T, root, rel, body string) {
	t.Helper()
	if rel != "" {
		write(t, root, rel, body)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "--quiet", "--allow-empty", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestAffectedRunsOnlyTheChangedComponent(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "moda/x.go", "package moda\n\n// Foo is what the test exercises.\nfunc Foo() int { return 1 }\n\n// Bar is new.\nfunc Bar() {}\n")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if got := jsonRowByLabel(t, out, "select"); got.Value != "2 of 2 affected" {
		t.Errorf("select = %q, want b pulled in by depends_on", got.Value)
	}
	// b is selected through the edge, not by a path.
	sel := jsonRowByLabel(t, out, "select")
	if !strings.Contains(strings.Join(sel.Detail, "\n"), "b: depends on a") {
		t.Errorf("detail = %v, want b named as a dependency of a", sel.Detail)
	}
}

// A watched path selects its watcher and nothing its watcher depends on:
// docs/spec.json is b's input, and a does not watch it.
func TestAffectedSelectsAWatcherAlone(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "docs/spec.json", "{\"v\":2}\n")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if got := jsonRowByLabel(t, out, "select"); got.Value != "1 of 2 affected" {
		t.Errorf("select = %q, want only the watcher", got.Value)
	}
	// The deselected component is present and marked, which is what lets a
	// reader tell "not affected" from "not declared".
	skipped := jsonRowByLabel(t, out, "test(a)")
	if skipped.Status != "unmeasured" || skipped.Value != "not affected" {
		t.Errorf("a = %+v, want an unmeasured 'not affected' row", skipped)
	}
}

// The posture, end to end: a path under no component widens rather than
// narrowing, so nothing goes untested because a rule had not learned it.
func TestAffectedWidensOnAnUnrecognisedPath(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "README.md", "changed\n")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if got := jsonRowByLabel(t, out, "select"); got.Value != "2 of 2 affected" {
		t.Errorf("select = %q, want every component", got.Value)
	}
}

// The whole risk of this feature: a run that tested nothing must not render
// like a run that tested everything and passed. The distinction is the row's
// status, so a consumer reads it without parsing prose.
func TestAffectedOnAnEmptyDiffIsUnmeasuredNotPassed(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "", "")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "select")
	if got.Status != "unmeasured" {
		t.Errorf("select status = %q, want unmeasured — nothing was gated", got.Status)
	}
	if got.Value != "0 of 2 affected" {
		t.Errorf("select = %q, want the count", got.Value)
	}
	for _, name := range []string{"a", "b"} {
		if r := jsonRowByLabel(t, out, "test("+name+")"); r.Status != "unmeasured" {
			t.Errorf("%s status = %q, want unmeasured", name, r.Status)
		}
	}
	// And no component reports a pass, which is what a reader scanning
	// glyphs would otherwise take for a complete run.
	if strings.Contains(out, "\"status\": \"pass\",\n      \"label\": \"a\"") {
		t.Error("a component that never ran reported a pass")
	}
}

// The two flags compose: --component says what this job is responsible for,
// --affected says which of those need running. A shard runs both, and the
// whole of what makes a fold possible is that it then reports its own
// components and nothing about anybody else's.
func TestAffectedAndComponentCompose(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "modb/x.go", "package modb\n\n// Foo is unchanged in shape.\nfunc Foo() int { return 2 }\n")

	out, err := runTestCmd(t, root, "--affected", "--component", "a", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	// a is this run's responsibility and the change did not touch it, so it
	// is reported as not affected rather than dropped.
	if got := jsonRowByLabel(t, out, "test(a)"); got.Status != "unmeasured" || got.Value != "not affected" {
		t.Errorf("test(a) = %+v, want an unmeasured 'not affected' row", got)
	}
	// b is affected and is somebody else's shard. A row about it here is the
	// defect: the fold would then hold two answers under one label.
	for _, label := range []string{"test(b)", "coverage(b)", "patch(b)"} {
		if strings.Contains(out, "\""+label+"\"") {
			t.Errorf("%s appears in a run responsible only for a", label)
		}
	}
	// The select row is computed over the whole declaration, so every shard
	// writes the same one and the fold can collapse them.
	if got := jsonRowByLabel(t, out, "select"); got.Value != "1 of 2 affected" {
		t.Errorf("select = %q, want the count over the whole declaration", got.Value)
	}
	// No figure over the repository: this run measured part of it.
	if strings.Contains(out, "coverage(repo)") {
		t.Error("a narrowed run published a figure about the repository")
	}
}

// An unresolvable merge-base refuses. Falling back to everything would be safe
// but would make the optimisation stop happening with no symptom but a slow
// job; falling back to nothing is the failure the feature exists to avoid.
func TestAffectedWithoutAMergeBaseIsAnError(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".lydite/components.yml": "components:\n  - name: a\n    dir: moda\n    runner: go-test\n",
		"moda/go.mod":            "module moda\n\ngo 1.26\n",
		"moda/x.go":              "package moda\n",
	})
	out, err := runTestCmd(t, root, "--affected", "--json")
	if err == nil {
		t.Fatalf("a repository with no origin selected something anyway:\n%s", out)
	}
	if !strings.Contains(err.Error(), "merge-base") {
		t.Errorf("error = %v, want it to name the merge-base", err)
	}
}

// A watch pattern covering no file is a component that will not run when its
// input changes. It fails, unlike an unused exclude, which only warns.
func TestWatchPatternCoveringNoFileFails(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".lydite/components.yml": "components:\n  - name: a\n    dir: moda\n    runner: go-test\n    watch: [\"docs/openapi.json\"]\n",
		"moda/go.mod":            "module moda\n\ngo 1.26\n",
		"moda/x.go":              "package moda\n",
	})
	out, _ := runTestCmd(t, root, "--json")
	got := jsonRowByLabel(t, out, "watch")
	if got.Status != "fail" {
		t.Fatalf("watch = %+v, want a failing row", got)
	}
	if !strings.Contains(strings.Join(got.Detail, "\n"), "docs/openapi.json") {
		t.Errorf("detail = %v, want the pattern named", got.Detail)
	}
}

func TestWatchPatternCoveringAFilePasses(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".lydite/components.yml": "components:\n  - name: a\n    dir: moda\n    runner: go-test\n    watch: [\"docs/openapi.json\"]\n",
		"docs/openapi.json":      "{}\n",
		"moda/go.mod":            "module moda\n\ngo 1.26\n",
		"moda/x.go":              "package moda\n",
	})
	out, _ := runTestCmd(t, root, "--json")
	if got := jsonRowByLabel(t, out, "watch"); got.Status != "pass" {
		t.Errorf("watch = %+v, want a pass", got)
	}
}

// Outside a git repository the gate cannot run, and must be visibly distinct
// from one that ran and passed rather than silently equal to it.
func TestWatchGateOutsideAGitRepositoryIsUnmeasured(t *testing.T) {
	root := fixtureRepo(t, "components:\n  - name: mod\n    dir: mod\n    runner: go-test\n    watch: [\"Makefile\"]\n")
	out, _ := runTestCmd(t, root, "--json")
	if got := jsonRowByLabel(t, out, "watch"); got.Status != "unmeasured" {
		t.Errorf("watch = %+v, want unmeasured outside a repository", got)
	}
}

// A gate that could not run must never blame the declaration. A scan root git
// lists no file for — one that is itself ignored, a vendored checkout, a --dir
// pointed at build output — exits zero with an empty listing, and every watch
// pattern would otherwise read as covering nothing.
func TestWatchGateOnAnIgnoredScanRootIsUnmeasured(t *testing.T) {
	root := gitRepo(t, map[string]string{
		".gitignore":                      "vendored/\n",
		"vendored/.lydite/components.yml": "components:\n  - name: a\n    dir: moda\n    runner: go-test\n    watch: [\"Makefile\"]\n",
		"vendored/Makefile":               "all:\n",
		"vendored/moda/go.mod":            "module moda\n\ngo 1.26\n",
		"vendored/moda/x.go":              "package moda\n",
	})
	out, _ := runTestCmd(t, filepath.Join(root, "vendored"), "--json")
	got := jsonRowByLabel(t, out, "watch")
	if got.Status != "unmeasured" {
		t.Errorf("watch = %+v, want unmeasured — the gate saw no files and cannot have an opinion", got)
	}
}

// A run that selected nothing has components declared. Saying otherwise makes
// the report contradict itself about the one thing it exists to state.
func TestAnEmptySelectionIsNotAnEmptyDeclaration(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "", "")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "no components declared") {
		t.Errorf("two components are declared; the report says none are:\n%s", out)
	}
}

// Component names are unique among themselves but share the label namespace
// with every gate row, and nothing forbids a component called "watch". A
// consumer keying rows by label would silently lose one of the two, and the
// gate row is the one that would go.
func TestADeselectedComponentCannotCollideWithAGateRow(t *testing.T) {
	root := affectedRepo(t)
	// Rename component "b" to "watch", which is also a gate's label. The
	// declaration change goes into the base: .lydite/** is an invalidator, so
	// carrying it in the probe commit would widen to every component and the
	// test would assert nothing.
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: watch\n    dir: modb\n    runner: go-test\n    args: [\"./...\"]\n"+
			"    watch: [\"docs/spec.json\"]\n")
	commitChange(t, root, "", "")
	gitIn(t, root, "push", "--quiet", "origin", "HEAD:main")
	commitChange(t, root, "moda/x.go", "package moda\n\n// Foo is what the test exercises.\nfunc Foo() int { return 1 }\n\n// Bar is new, and keeps the suite passing.\nfunc Bar() {}\n")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	// The gate row survives, carrying its own value rather than the
	// component's.
	if got := jsonRowByLabel(t, out, "watch"); got.Status != "pass" || got.Value == "not affected" {
		t.Errorf("watch gate row = %+v, want the gate's own row, not the component's", got)
	}
	if got := jsonRowByLabel(t, out, "test(watch)"); got.Value != "not affected" {
		t.Errorf("test(watch) = %+v, want the deselected component's row", got)
	}
}

// On the default branch the merge-base is HEAD itself, so a computed
// selection narrows to nothing. ADR 0016 requires that run to be complete —
// a forgotten depends_on edge is caught at merge or never — so --affected
// there runs everything rather than reporting a green run that executed no
// suite at all.
func TestAffectedOnTheDefaultBranchRunsEverything(t *testing.T) {
	root := affectedRepo(t) // leaves HEAD at origin/main

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	got := jsonRowByLabel(t, out, "select")
	if got.Value != "2 of 2 affected" {
		t.Errorf("select = %q, want every component to run on the default branch", got.Value)
	}
	if got.Status != "pass" {
		t.Errorf("select status = %q, want pass", got.Status)
	}
	// The reason is about the run, so it is stated once rather than once per
	// component — which would scale with the declaration and say no more at
	// the twentieth line than at the first.
	if len(got.Detail) != 1 || !strings.Contains(got.Detail[0], "default branch") {
		t.Errorf("detail = %v, want one line naming the default branch", got.Detail)
	}
	for _, name := range []string{"a", "b"} {
		if r := jsonRowByLabel(t, out, "test("+name+")"); r.Status != "pass" {
			t.Errorf("test(%s) = %+v, want it to have run", name, r)
		}
	}
}

// Rows read in the order the declaration is written, so a component selection
// skipped sits where its author put it rather than ahead of everything that
// ran. Three components with only the middle one affected is the arrangement
// that tells the two apart.
func TestRowsAreInDeclarationOrderAcrossSkips(t *testing.T) {
	root := affectedRepo(t)
	write(t, root, "modc/go.mod", "module modc\n\ngo 1.26\n")
	write(t, root, "modc/x.go", "package modc\n\n// Foo is exercised.\nfunc Foo() int { return 1 }\n")
	write(t, root, "modc/x_test.go", "package modc\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\tif Foo() != 1 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n")
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: a\n    dir: moda\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: b\n    dir: modb\n    runner: go-test\n    args: [\"./...\"]\n"+
			"  - name: c\n    dir: modc\n    runner: go-test\n    args: [\"./...\"]\n")
	commitChange(t, root, "", "")
	gitIn(t, root, "push", "--quiet", "origin", "HEAD:main")
	commitChange(t, root, "modb/x.go", "package modb\n\n// Foo is what the test exercises.\nfunc Foo() int { return 1 }\n\n// Bar is new.\nfunc Bar() {}\n")

	out, err := runTestCmd(t, root, "--affected", "--json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "\"test("); i >= 0 {
			order = append(order, strings.Trim(line[i:], "\","))
		}
	}
	if got, want := strings.Join(order, " "), "test(a) test(b) test(c)"; got != want {
		t.Errorf("rows are %q, want declaration order %q", got, want)
	}
}

// A repository that declares no components has nothing to select from, which
// is not the same as a change that selected nothing. Running selection anyway
// produced a select row claiming "no changes against the merge-base" beside a
// row saying no components are declared — two rows contradicting each other
// about the one thing the report exists to state — and paid a git fetch to do
// it, which on a shallow or fork checkout turned a renderable report into a
// hard error before any row was written.
func TestAffectedWithNoComponentsDeclaredSelectsNothingAndFetchesNothing(t *testing.T) {
	root := affectedRepo(t)
	write(t, root, component.FileName, "components: []\n")
	commitChange(t, root, "", "")
	gitIn(t, root, "push", "--quiet", "origin", "HEAD:main")
	commitChange(t, root, "README.md", "changed\n")
	// No remote at all: selection must not reach for one.
	gitIn(t, root, "remote", "remove", "origin")

	out, _ := runTestCmd(t, root, "--affected", "--json")
	if strings.Contains(out, "\"label\": \"select\"") {
		t.Errorf("a declaration with no components produced a select row:\n%s", out)
	}
	if strings.Contains(out, "no changes against the merge-base") {
		t.Errorf("the report claims the diff was empty; README.md changed:\n%s", out)
	}
	if got := jsonRowByLabel(t, out, "test"); !strings.Contains(got.Value, "no components declared") {
		t.Errorf("test row = %+v, want it to name the empty declaration", got)
	}
}
