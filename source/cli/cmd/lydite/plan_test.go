package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/ui"
)

// planRepo is a declaration whose shards are not the declaration order: `api`
// and `tally` publish one host port under deliberately differently named
// services, `web` publishes another, and `docs` publishes nothing.
func planRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n    compose:\n      file: compose.yml\n"+
			"  - name: web\n    dir: web\n    runner: vitest\n    compose:\n      file: compose.yml\n"+
			"  - name: tally\n    dir: rust\n    runner: cargo-nextest\n    compose:\n      file: compose.yml\n"+
			"  - name: docs\n    dir: docs\n    runner: go-test\n")
	service := func(dir, name string, port int) {
		write(t, root, dir+"/compose.yml",
			fmt.Sprintf("services:\n  %s:\n    image: postgres\n    ports: [\"%d:5432\"]\n"+
				"    healthcheck:\n      test: [\"CMD\", \"true\"]\n", name, port))
	}
	service("go/api", "db", 5432)
	service("web", "cache", 6379)
	service("rust", "postgres", 5432)
	for _, dir := range []string{"go/api", "web", "rust", "docs"} {
		write(t, root, dir+"/.keep", "")
	}
	return root
}

func runPlanCmd(t *testing.T, root string, extra ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"test", "plan", "--dir", root, "--no-color"}, extra...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// Two components that publish one host port belong in one job, where the
// scheduler serialises them. Apart, they are safe only on a runner topology
// that gives every matrix job its own machine — and self-hosted runners
// routinely place several on one host.
func TestAShardIsAConflictGroup(t *testing.T) {
	root := planRepo(t)
	matrix := filepath.Join(t.TempDir(), "matrix.json")
	out, err := runPlanCmd(t, root, "--out", matrix)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	var got []matrixEntry
	data, err := os.ReadFile(matrix) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("matrix is not JSON: %v\n%s", err, data)
	}
	want := []matrixEntry{
		// Ordered by the declaration position of its first member, and the
		// members in declaration order, so two runs of one declaration emit
		// an identical matrix.
		{Name: "api-tally", Components: "api,tally"},
		{Name: "web", Components: "web"},
		{Name: "docs", Components: "docs"},
	}
	if len(got) != len(want) {
		t.Fatalf("matrix = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("matrix[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The row says why the group is a group, and the port is what says the
	// grouping came from the compose files rather than from the order the
	// components happen to be declared in.
	if !strings.Contains(out, "api and tally share port 5432") {
		t.Errorf("plan does not say why api and tally are one shard:\n%s", out)
	}
}

// Nothing about a declaration changes between two runs, so nothing about the
// matrix may either: a matrix job's name is what its artifact is called, and a
// name that moved would orphan the directory the fold reads.
func TestAPlanIsTheSameTwice(t *testing.T) {
	root := planRepo(t)
	dir := t.TempDir()
	read := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if out, err := runPlanCmd(t, root, "--out", p); err != nil {
			t.Fatalf("plan: %v\n%s", err, out)
		}
		data, err := os.ReadFile(p) // #nosec G304 -- a path this test just wrote
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if first, second := read("one.json"), read("two.json"); first != second {
		t.Errorf("two plans of one declaration differ:\n%s\n%s", first, second)
	}
}

// A matrix with no entries is a job that runs nothing, and every gate
// downstream of it goes green over an untested repository.
func TestPlanRefusesADeclarationWithNoComponents(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components: []\n")
	if _, err := runPlanCmd(t, root); err == nil {
		t.Fatal("a declaration with no components produced a matrix")
	}
}

// The ports come from the compose file, so a file that will not load leaves
// the grouping unknown — and a matrix built on unknown ports can put two
// contending components into different jobs, which is the one thing the
// planner exists to prevent.
func TestPlanFailsOnAComposeFileItCannotRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n  - name: api\n    dir: api\n    runner: go-test\n    compose:\n      file: compose.yml\n")
	write(t, root, "api/.keep", "")
	out, err := runPlanCmd(t, root)
	if err == nil {
		t.Fatalf("a component whose compose file is absent produced a matrix:\n%s", out)
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("error = %v, want it to name the component", err)
	}
}

// The report is what a person reads; the matrix is what a workflow reads. The
// one verdict the plan reaches is the orphan gate's, and `lydite test` reports
// that same row from its own document — a plan.json would carry it into the
// pull-request comment a second time. The plan's own channel is stdout, the
// job log and the exit code.
func TestPlanWritesNoReportDocument(t *testing.T) {
	root := planRepo(t)
	if _, err := runPlanCmd(t, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(documentPath(reportsDir(root), "plan")); !os.IsNotExist(err) {
		t.Errorf("plan wrote a report document: %v", err)
	}
}

// One declaration, two commands, one orphan verdict. Both reach the gate
// through the same orphanRow, and a copy of it in either command would agree
// with this one right up until somebody edited one of them for that command's
// own reason — a divergence neither command's other tests can see.
func TestPlanAndTestReachTheSameOrphanVerdict(t *testing.T) {
	root := gitRepo(t, map[string]string{
		component.FileName: "components:\n  - name: cli\n    dir: cli\n    runner: go-test\n",
		"cli/main.go":      "package main\n",
		"scripts/seed.ts":  "export const s = 1\n",
	})
	planned, err := runPlanCmd(t, root, "--json")
	if err == nil {
		t.Errorf("an orphan must fail the plan:\n%s", planned)
	}
	tested, _ := runTestCmd(t, root, "--json", "--component", "cli")
	fromPlan := jsonRowByLabel(t, planned, "orphans")
	fromTest := jsonRowByLabel(t, tested, "orphans")
	if fromPlan.Status != string(ui.StatusFail) {
		t.Errorf("status = %q, want %q — the declaration covers neither the orphan nor an exclude", fromPlan.Status, ui.StatusFail)
	}
	if fmt.Sprint(fromPlan) != fmt.Sprint(fromTest) {
		t.Errorf("the two commands disagree about the orphans:\nplan %+v\ntest %+v", fromPlan, fromTest)
	}
}

// The gate reads its file list from git, so outside a repository it has none:
// it reports unmeasured and the plan still emits its matrix. A plan that
// failed in an exported tarball would be the gate firing on ordinary work.
func TestPlanOutsideAGitRepositoryIsUnmeasured(t *testing.T) {
	root := planRepo(t)
	out, err := runPlanCmd(t, root, "--json")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	row := jsonRowByLabel(t, out, "orphans")
	if row.Status != string(ui.StatusUnmeasured) {
		t.Errorf("status = %q, want %q — an unrunnable gate is not a passing one", row.Status, ui.StatusUnmeasured)
	}
	if !strings.Contains(row.Value, "git") {
		t.Errorf("value = %q, want it to name the missing repository", row.Value)
	}
}

// The name is the matrix job's and the artifact's suffix. Two shards sharing
// one collide on upload, and the fold then reads one twice while the other's
// components go missing — reported as a shard that died, naming components
// nothing was wrong with.
func TestPlanRefusesTwoShardsWithOneName(t *testing.T) {
	root := t.TempDir()
	// `a-b` beside `c`, and `a` beside `b-c`, both spell `a-b-c`. Each pair is
	// a shard because the two share a directory.
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: a-b\n    dir: one\n    runner: go-test\n"+
			"  - name: c\n    dir: one\n    runner: go-test\n"+
			"  - name: a\n    dir: two\n    runner: go-test\n"+
			"  - name: b-c\n    dir: two\n    runner: go-test\n")
	write(t, root, "one/.keep", "")
	write(t, root, "two/.keep", "")
	out, err := runPlanCmd(t, root)
	if err == nil {
		t.Fatalf("two shards took one name:\n%s", out)
	}
	if !strings.Contains(err.Error(), "a-b-c") {
		t.Errorf("error = %v, want it to name the collision", err)
	}
}

// Two components writing into one generated tree belong in one job for the
// reason two sharing a port do: the scheduler serialises them inside a run,
// and split across jobs on a runner hosting both nothing does.
func TestComponentsOccupyingOneTreeAreOneShard(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml",
		"components:\n"+
			"  - name: ui\n    dir: packages/ui\n    runner: vitest\n    occupies: [\"packages/tokens\"]\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n"+
			"  - name: storybook\n    dir: apps/storybook\n    runner: vitest\n    occupies: [\"packages/tokens/dist\"]\n")
	for _, dir := range []string{"packages/ui", "go/api", "apps/storybook"} {
		write(t, root, dir+"/.keep", "")
	}
	matrix := filepath.Join(t.TempDir(), "matrix.json")
	out, err := runPlanCmd(t, root, "--out", matrix)
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	data, err := os.ReadFile(matrix) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	var got []matrixEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("matrix is not JSON: %v\n%s", err, data)
	}
	want := []matrixEntry{
		{Name: "ui-storybook", Components: "ui,storybook"},
		{Name: "api", Components: "api"},
	}
	if len(got) != len(want) {
		t.Fatalf("matrix = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("matrix[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !strings.Contains(out, "ui and storybook share directory packages/tokens") {
		t.Errorf("plan does not say why ui and storybook are one shard:\n%s", out)
	}
}

// The planner groups by the predicate the scheduler serialises by, so the two
// items have to carry the same fields: one that planItems fills and itemFor
// does not is a pair the matrix keeps together and the run then lets overlap,
// and one the other way round is a pair split across jobs that nothing
// serialises. Neither shows up in either command's own tests.
func TestPlanAndRunSeeTheSameConflicts(t *testing.T) {
	root := t.TempDir()
	write(t, root, component.FileName,
		"components:\n"+
			"  - name: ui\n    dir: packages/ui\n    runner: vitest\n    occupies: [\"packages/tokens\"]\n"+
			"  - name: storybook\n    dir: apps/storybook\n    runner: vitest\n    occupies: [\"packages/tokens/dist\"]\n"+
			"  - name: root\n    dir: .\n    runner: go-test\n"+
			"  - name: api\n    dir: go/api\n    runner: go-test\n")
	for _, dir := range []string{"packages/ui", "apps/storybook", "go/api"} {
		write(t, root, dir+"/.keep", "")
	}
	file, err := component.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	planned, err := planItems(root, file)
	if err != nil {
		t.Fatalf("planItems: %v", err)
	}
	var ran []scheduler.Item
	for _, p := range planComponents(context.Background(), root, file.Components, "test", false) {
		defer p.log.Close()
		ran = append(ran, itemFor(p))
	}

	a, b := scheduler.Conflicts(planned), scheduler.Conflicts(ran)
	if len(a) == 0 {
		t.Fatal("the declaration produced no conflicts, so the comparison proves nothing")
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("the planner and the run disagree:\nplan %v\nrun  %v", a, b)
	}
}
