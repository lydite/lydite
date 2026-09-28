package mutationstages

import (
	"io"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/runner"
)

// A component's dir is relative to the scan root and so is git's diff; the
// generator works in component-relative paths, because that is what a compiler
// is pointed at.
func TestAChangedPathIsMappedOntoTheComponentItIsInside(t *testing.T) {
	for _, c := range []struct{ dir, file, want string }{
		{".", "cmd/main.go", "cmd/main.go"},
		{"source/cli", "source/cli/internal/a/a.go", "internal/a/a.go"},
		{"./source/cli", "source/cli/a.go", "a.go"},
	} {
		got, err := componentRelative(c.dir, c.file)
		if err != nil {
			t.Fatalf("%s in %s: %v", c.file, c.dir, err)
		}
		if got != c.want {
			t.Errorf("%s in %s = %q, want %q", c.file, c.dir, got, c.want)
		}
	}
	if _, err := componentRelative("web", "source/cli/a.go"); err == nil {
		t.Error("a path outside the component was mapped into it")
	}
}

// A line the coverage report lists with no hits is covered by no test, so a
// mutant on it survives by construction and would restate what patch coverage
// already said about the same line.
func TestOnlyAnExecutedLineIsMutated(t *testing.T) {
	root := t.TempDir()
	src := "package a\n\nfunc Less(x, y int) bool {\n\tif x < y {\n\t\treturn true\n\t}\n\treturn false\n}\n"
	writeFile(t, root, "a.go", src)
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test"}

	// Line 4 holds the comparison; the report says it never ran.
	hits := coverage.LineHits{"a.go": {4: 0, 5: 1}}
	mutants, err := generate(root, c, runner.Go, hits, map[string][]int{"a.go": {4, 5}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mutants {
		if m.Line == 4 {
			t.Errorf("an uncovered line was mutated: %s", m)
		}
	}
	// Line 5 did run, and it holds a `return true` the catalogue rewrites, so
	// the assertion above is not passing merely because nothing was generated.
	if len(mutants) == 0 {
		t.Fatal("no mutant was generated from the covered line either")
	}
}

// A file the diff names and the tree no longer holds — deleted, or renamed —
// has no source to mutate, and that is not an error about the declaration.
func TestAChangedFileThatIsGoneIsSkipped(t *testing.T) {
	root := t.TempDir()
	c := component.Component{Name: "app", Dir: ".", Runner: "go-test"}
	mutants, err := generate(root, c, runner.Go, coverage.LineHits{"gone.go": {3: 1}}, map[string][]int{"gone.go": {3}}, io.Discard)
	if err != nil {
		t.Fatalf("a deleted file failed the run: %v", err)
	}
	if len(mutants) != 0 {
		t.Errorf("%d mutant(s) from a file that is not there", len(mutants))
	}
}

// Files are visited in one order whatever order the diff was handed over in,
// so two runs over one change generate the same mutants in the same order.
func TestChangedFilesAreVisitedInPathOrder(t *testing.T) {
	got := sortedFiles(map[string][]int{"web/b.ts": nil, "app/z.go": nil, "app/a.go": nil})
	if want := []string{"app/a.go", "app/z.go", "web/b.ts"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sortedFiles = %v, want %v", got, want)
	}
}
