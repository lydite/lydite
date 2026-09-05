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

// The report is what a person reads; the matrix is what a workflow reads. A
// document in .lydite-reports/ would put a section titled "plan" in the
// pull-request comment saying nothing anyone can act on.
func TestPlanWritesNoReportDocument(t *testing.T) {
	root := planRepo(t)
	if _, err := runPlanCmd(t, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(documentPath(reportsDir(root), "plan")); !os.IsNotExist(err) {
		t.Errorf("plan wrote a report document: %v", err)
	}
}
