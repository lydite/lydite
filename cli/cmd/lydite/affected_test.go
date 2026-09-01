package main

import (
	"os/exec"
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
	skipped := jsonRowByLabel(t, out, "a")
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
		if r := jsonRowByLabel(t, out, name); r.Status != "unmeasured" {
			t.Errorf("%s status = %q, want unmeasured", name, r.Status)
		}
	}
	// And no component reports a pass, which is what a reader scanning
	// glyphs would otherwise take for a complete run.
	if strings.Contains(out, "\"status\": \"pass\",\n      \"label\": \"a\"") {
		t.Error("a component that never ran reported a pass")
	}
}

func TestAffectedRefusesTheComponentFlag(t *testing.T) {
	root := affectedRepo(t)
	commitChange(t, root, "moda/x.go", "package moda\n\n// Foo is unchanged in shape.\nfunc Foo() int { return 1 }\n")

	_, err := runTestCmd(t, root, "--affected", "--component", "a", "--json")
	if err == nil {
		t.Fatal("--affected with --component was accepted; two narrowing mechanisms compose silently")
	}
	if !strings.Contains(err.Error(), "both narrow the run") {
		t.Errorf("error = %v, want it to name the conflict", err)
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
