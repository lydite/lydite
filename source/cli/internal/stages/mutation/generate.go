package mutationstages

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
)

// generate produces every mutant for one component: from the lines this change
// touched, intersected with the lines its own coverage reports as executed.
//
// Both bounds are load-bearing and neither is the other. The diff makes the
// cost proportional to the change rather than to the repository, which is what
// makes mutation affordable at all; the coverage intersection removes mutants
// that cannot be killed by construction, and reporting one would only restate
// what patch coverage already said about the same line.
//
// A declaration that matched no mutant is named on diagnostics, one line each,
// and counted across every file in the component: the count is what reaches the
// component's row, where a reader who never sees the stream still learns of it.
func generate(root string, c component.Component, lang runner.Lang, executed coverage.LineHits, scoped map[string][]int, diagnostics io.Writer) ([]mutation.Mutant, int, error) {
	var out []mutation.Mutant
	unmatchedCount := 0
	for _, file := range sortedFiles(scoped) {
		ran := executed[file]
		lines := map[int]bool{}
		for _, l := range scoped[file] {
			// Reported *and* executed. A line the report lists with a hit
			// count of zero is covered by no test, so a mutant on it survives
			// by construction and says nothing about the suite.
			if ran[l] > 0 {
				lines[l] = true
			}
		}
		if len(lines) == 0 {
			continue
		}
		// The generator works in component-relative paths, because that is
		// what the executor writes and what a compiler is pointed at; the
		// diff and the coverage report are both scan-root relative.
		rel, err := componentRelative(c.Dir, file)
		if err != nil {
			return nil, 0, err // [lydite:exclude_from_mutation][the caller returns on this error before reading the count]
		}
		// Joined onto the scan root, which is what a component's dir is
		// relative to. Resolving it against this process's working directory
		// instead reads the right file only when lydite happens to be run
		// from the scan root, and silently generates nothing everywhere else.
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(c.Dir), filepath.FromSlash(rel))) // #nosec G304 -- a source file of the component being mutated, named by git's own diff
		if err != nil {
			// A file in the diff that is no longer in the tree — deleted, or
			// renamed — has no source to mutate and is not an error about the
			// declaration.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, 0, err // [lydite:exclude_from_mutation][the caller returns on this error before reading the count]
		}
		mutants, unmatched, err := mutation.Generate(lang, rel, src, lines)
		if err != nil {
			return nil, 0, err // [lydite:exclude_from_mutation][the caller returns on this error before reading the count]
		}
		// Named, because their author believes they have answered a survivor
		// and nothing they can see says otherwise: the comment is well
		// formed, it carries a reason, and the mutant it was meant for is
		// generated and run anyway.
		for _, u := range unmatched {
			_, _ = fmt.Fprintf(diagnostics, "lydite: %s: %s\n", c.Name, u)
		}
		unmatchedCount += len(unmatched)
		out = append(out, mutants...)
	}
	return out, unmatchedCount, nil
}

// componentRelative maps a scan-root-relative path onto the component it is
// inside. The component's Scope has already established that it is.
func componentRelative(dir, file string) (string, error) {
	clean := path.Clean(dir)
	if clean == "." {
		return file, nil
	}
	rel, ok := strings.CutPrefix(file, clean+"/")
	if !ok {
		return "", fmt.Errorf("%s is not inside %s", file, dir) // [lydite:exclude_from_mutation][the one caller abandons the file when the error is non-nil, so no path reads the string beside it and no test can be shown a different one]
	}
	return rel, nil
}

func sortedFiles(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
