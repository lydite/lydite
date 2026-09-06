package mutation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/runner"
)

// goComponent writes a file into a throwaway component directory and returns
// the backend over it, built from the runner's own variants so what mutation
// compiles and runs is the same suite the coverage gate does.
func goComponent(t *testing.T, rel, src string) Go {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	r, ok := runner.Lookup(runner.GoTest)
	if !ok {
		t.Fatal("the go-test runner is not registered")
	}
	build, _ := r.Build(runner.BuildOnly, []string{"./..."})
	suite, _ := r.Build(runner.Plain, []string{"-race", "./..."})
	return Go{Dir: dir, Build: build, Suite: suite}
}

func stage(t *testing.T, g Go, m Mutant) (Worker, Staged) {
	t.Helper()
	w, err := g.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	staged, err := w.Stage(m)
	if err != nil {
		t.Fatal(err)
	}
	return w, staged
}

// negated is a mutant of the `<` in the source goComponent is given below.
func negated(path, src string) Mutant {
	return Mutant{
		Path: path, Line: 3, Column: 12, Operator: NegateConditional,
		Offset: strings.Index(src, "<"), Length: 1, Original: "<", Mutated: ">=",
	}
}

const goSrc = "package a\n\nfunc Less(x, y int) bool { return x < y }\n"

func TestTheOverlayNamesTheMutatedFileAndTheTreeIsNotTouched(t *testing.T) {
	g := goComponent(t, "internal/a/a.go", goSrc)
	m := negated("internal/a/a.go", goSrc)
	_, staged := stage(t, g, m)

	file := overlayPath(t, staged.Build)
	var doc overlay
	read(t, file, &doc)
	original := filepath.Join(g.Dir, "internal", "a", "a.go")
	abs, err := filepath.Abs(original)
	if err != nil {
		t.Fatal(err)
	}
	replacement, ok := doc.Replace[abs]
	if !ok {
		t.Fatalf("the overlay replaces %v, not %s", keysOf(doc.Replace), abs)
	}
	got, err := os.ReadFile(replacement) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if want := "return x >= y"; !strings.Contains(string(got), want) {
		t.Errorf("the replacement holds %q, want it to contain %q", got, want)
	}
	// The component's own tree is what an interrupt would leave behind, and
	// nothing here may write to it.
	untouched, err := os.ReadFile(original) // #nosec G304 -- a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != goSrc {
		t.Errorf("the component's own source was edited: %q", untouched)
	}
}

func TestTheMutantsOwnPackageRunsBeforeTheClosure(t *testing.T) {
	g := goComponent(t, "internal/a/a.go", goSrc)
	_, staged := stage(t, g, negated("internal/a/a.go", goSrc))

	if len(staged.Phases) != 2 {
		t.Fatalf("%d phase(s), want the mutant's own package then the closure", len(staged.Phases))
	}
	if !slices.Contains(staged.Phases[0].Args, "./internal/a") {
		t.Errorf("the first phase runs %v, want it narrowed to ./internal/a", staged.Phases[0].Args)
	}
	if !slices.Contains(staged.Phases[1].Args, "./...") {
		t.Errorf("the second phase runs %v, want the whole closure", staged.Phases[1].Args)
	}
	// The declared arguments survive the narrowing: a mutant killed only
	// under the race detector must still be killed by the phase that runs
	// first.
	if !slices.Contains(staged.Phases[0].Args, "-race") {
		t.Errorf("the first phase runs %v, want the component's declared arguments kept", staged.Phases[0].Args)
	}
}

func TestAMutantAtTheComponentRootNarrowsToTheRootPackage(t *testing.T) {
	// "." is the package the file sits in and "./..." is every package under
	// it, so even here the first phase is the cheaper of the two.
	g := goComponent(t, "a.go", goSrc)
	_, staged := stage(t, g, negated("a.go", goSrc))
	if len(staged.Phases) != 2 {
		t.Fatalf("%d phase(s), want the package and the closure", len(staged.Phases))
	}
	if !slices.Contains(staged.Phases[0].Args, ".") {
		t.Errorf("the first phase runs %v, want it narrowed to the root package", staged.Phases[0].Args)
	}
}

func TestTheSuiteIsNeverGivenCountOne(t *testing.T) {
	// The test cache is what makes a mutant cost 1.67s instead of 79s: an
	// overlay invalidates the mutated package and its dependents and nothing
	// else, so mutation gets incremental test selection for free. -count=1
	// would multiply the cost of mutation by roughly fifty.
	g := goComponent(t, "internal/a/a.go", goSrc)
	_, staged := stage(t, g, negated("internal/a/a.go", goSrc))
	for _, inv := range append([]runner.Invocation{staged.Build}, staged.Phases...) {
		for _, arg := range inv.Args {
			if strings.HasPrefix(arg, "-count") {
				t.Errorf("%v disables the test cache", inv.Args)
			}
		}
	}
}

func TestAStaleMutantIsRefusedRatherThanSpliced(t *testing.T) {
	g := goComponent(t, "a.go", goSrc)
	m := negated("a.go", goSrc)
	m.Original = ">" // the file holds "<" at that offset
	w, err := g.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if _, err := w.Stage(m); err == nil {
		t.Fatal("a mutant was spliced into source it does not describe")
	}
}

func TestAPathOutsideTheComponentIsRefused(t *testing.T) {
	g := goComponent(t, "a.go", goSrc)
	w, err := g.Worker(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for _, path := range []string{"../outside.go", "/etc/passwd", ""} {
		if _, err := w.Stage(Mutant{Path: path, Original: "<", Mutated: ">="}); err == nil {
			t.Errorf("%q was staged", path)
		}
	}
}

func TestReleaseLeavesNoOverlayForTheNextMutantToInherit(t *testing.T) {
	g := goComponent(t, "a.go", goSrc)
	w, staged := stage(t, g, negated("a.go", goSrc))
	file := overlayPath(t, staged.Build)
	if err := w.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the overlay outlived the mutant it presented")
	}
}

func TestNarrowLeavesAFlagsOwnValueAlone(t *testing.T) {
	// The narrowed phase is never authoritative — a mutant it fails to kill
	// runs again against the closure — so the worst a misread argument costs
	// is the second run. It still must not rewrite one.
	inv := runner.Invocation{Name: "go", Args: []string{"test", "-run", "TestThing", "-race", "./..."}}
	out, ok := narrow(inv, "./internal/a")
	if !ok {
		t.Fatal("nothing was narrowed")
	}
	want := []string{"test", "-run", "TestThing", "-race", "./internal/a"}
	if !slices.Equal(out.Args, want) {
		t.Errorf("narrowed to %v, want %v", out.Args, want)
	}
}

func TestNothingAfterArgsIsReadAsAPackage(t *testing.T) {
	inv := runner.Invocation{Name: "go", Args: []string{"test", "./...", "-args", "./fixtures/..."}}
	out, ok := narrow(inv, "./internal/a")
	if !ok {
		t.Fatal("nothing was narrowed")
	}
	want := []string{"test", "./internal/a", "-args", "./fixtures/..."}
	if !slices.Equal(out.Args, want) {
		t.Errorf("narrowed to %v, want %v — everything after -args belongs to the test binary", out.Args, want)
	}
}

func TestAnInvocationNamingNoPackageGainsOne(t *testing.T) {
	inv := runner.Invocation{Name: "go", Args: []string{"test"}}
	out, ok := narrow(inv, "./internal/a")
	if !ok {
		t.Fatal("nothing was narrowed")
	}
	want := []string{"test", "./internal/a"}
	if !slices.Equal(out.Args, want) {
		t.Errorf("narrowed to %v, want %v", out.Args, want)
	}
}

// overlayPath reads the -overlay flag back off an invocation, which is where
// the go command is told to find the mutant.
func overlayPath(t *testing.T, inv runner.Invocation) string {
	t.Helper()
	for _, arg := range inv.Args {
		if path, ok := strings.CutPrefix(arg, "-overlay="); ok {
			return path
		}
	}
	t.Fatalf("%v names no overlay", inv.Args)
	return ""
}

func read(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test just staged
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
