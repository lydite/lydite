package mutationstages

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/mutation"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// twoComponents is a declaration of two components whose directories exist.
func twoComponents(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, ".lydite/components.yml",
		"components:\n  - name: app\n    dir: app\n    runner: go-test\n  - name: web\n    dir: web\n    runner: vitest\n")
	writeFile(t, root, "app/.keep", "")
	writeFile(t, root, "web/.keep", "")
	return root
}

func names(cs []component.Component) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// A run responsible for part of the declaration is handed that part, and the
// whole declaration beside it: selection runs over every component, because a
// dependency edge reaches components another shard is responsible for.
func TestLoadDeclarationReadsThisRunsShareOfTheDeclaration(t *testing.T) {
	root := twoComponents(t)
	out, err := LoadDeclaration(t.Context(), LoadDeclarationIn{Dir: root, Components: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Declared {
		t.Error("a declaration naming two components was not declared")
	}
	if got := names(out.Own); !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("own = %v, want only web", got)
	}
	if got := names(out.File.Components); !reflect.DeepEqual(got, []string{"app", "web"}) {
		t.Errorf("the declaration = %v, want both components", got)
	}

	all, err := LoadDeclaration(t.Context(), LoadDeclarationIn{Dir: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(all.Own); !reflect.DeepEqual(got, []string{"app", "web"}) {
		t.Errorf("own with no --component = %v, want every declared component", got)
	}
}

// A declaration naming no component is not an error: what a run over nothing
// reports is the caller's to say, and it must be able to say it.
func TestADeclarationNamingNoComponentIsNotDeclared(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".lydite/components.yml", "components: []\n")
	out, err := LoadDeclaration(t.Context(), LoadDeclarationIn{Dir: root})
	if err != nil {
		t.Fatalf("an empty declaration failed to load: %v", err)
	}
	if out.Declared {
		t.Error("an empty declaration was reported declared")
	}
}

// A component this run names and the declaration does not is refused, rather
// than a run that mutates nothing and passes.
func TestLoadDeclarationRefusesAComponentNothingDeclares(t *testing.T) {
	root := twoComponents(t)
	if _, err := LoadDeclaration(t.Context(), LoadDeclarationIn{Dir: root, Components: []string{"nope"}}); err == nil {
		t.Error("a component nothing declares was selected")
	}
}

// Toolchains are provisioned for the run's own components, and their failure
// is the stage's.
func TestProvisionToolchainsProvisionsTheRunsOwnComponents(t *testing.T) {
	own := []component.Component{{Name: "app", Dir: "app", Runner: "go-test"}}
	want := toolchain.Envs{"app": &toolchain.Env{}}
	out, err := ProvisionToolchains(t.Context(), ProvisionToolchainsIn{
		Toolchains: fakeToolchains{t: t, ensure: func(_ context.Context, dir string, _ config.Config, cs []component.Component) (toolchain.Envs, error) {
			if dir != "root" {
				t.Errorf("provisioned under %q, want the scan root", dir)
			}
			if !reflect.DeepEqual(cs, own) {
				t.Errorf("provisioned %v, want %v", cs, own)
			}
			return want, nil
		}},
		Dir: "root",
		Own: own,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Envs, want) {
		t.Errorf("envs = %v, want the provisioned ones", out.Envs)
	}

	refused := errors.New("no toolchain")
	_, err = ProvisionToolchains(t.Context(), ProvisionToolchainsIn{
		Toolchains: fakeToolchains{t: t, ensure: func(context.Context, string, config.Config, []component.Component) (toolchain.Envs, error) {
			return nil, refused
		}},
	})
	if !errors.Is(err, refused) {
		t.Errorf("err = %v, want the provisioning failure", err)
	}
}

// twoChangesRepo is a branch carrying two changes over main, so the merge-base
// with main is a commit behind HEAD~1 and the two bases name different ranges.
func twoChangesRepo(t *testing.T) string {
	t.Helper()
	return mutationRepoWithOrigin(t, map[string]string{"a.txt": "seed\n"},
		map[string]string{"a.txt": "first\n"}, map[string]string{"a.txt": "second\n"})
}

func TestAnExplicitBaseResolvesTheCommitItNames(t *testing.T) {
	root := twoChangesRepo(t)
	ctx := context.Background()
	want := git(t, root, "rev-parse", "--verify", "HEAD~1")
	mergeBase, err := resolveMutationBase(ctx, root, "main", "")
	if err != nil {
		t.Fatalf("the merge-base against main did not resolve: %v", err)
	}
	if mergeBase == want {
		t.Fatal("the fixture's merge-base is its own HEAD~1, so nothing here distinguishes the two bases")
	}
	for _, c := range []struct {
		name     string
		revision string
	}{
		{"the full SHA", want},
		{"an abbreviated SHA", want[:8]},
		{"a relative ref", "HEAD~1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveMutationBase(ctx, root, "", c.revision)
			if err != nil {
				t.Fatalf("%s did not resolve: %v", c.revision, err)
			}
			if got != want {
				t.Errorf("--base-sha %s resolved %s, want %s", c.revision, got, want)
			}
		})
	}

	out, err := ResolveBase(ctx, ResolveBaseIn{Dir: root, BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Base != mergeBase {
		t.Errorf("the stage resolved %s, want the merge-base %s", out.Base, mergeBase)
	}
}

// An unresolvable explicit base is an error naming the revision and the fix,
// exactly as an unresolvable merge-base is — never a run that mutates nothing
// and reports a pass.
func TestAnUnresolvableExplicitBaseIsAnErrorNamingTheFix(t *testing.T) {
	root := twoChangesRepo(t)
	base, err := resolveMutationBase(t.Context(), root, "", "HEAD~99")
	if err == nil {
		t.Fatal("a base no commit answers to was accepted")
	}
	if base != "" {
		t.Errorf("an unresolvable base resolved %q alongside its error", base)
	}
	for _, want := range []string{"--base-sha", "HEAD~99", "depth 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the run failed with %q, want it to say %q", err, want)
		}
	}
	if _, err := ResolveBase(t.Context(), ResolveBaseIn{Dir: root, BaseSHA: "HEAD~99"}); err == nil {
		t.Fatal("a base no commit answers to was accepted through the stage")
	}
}

// An unresolvable merge-base names the usual cause and its fix, rather than
// the git error alone.
func TestAnUnresolvableMergeBaseIsAnErrorNamingTheFix(t *testing.T) {
	root := twoChangesRepo(t)
	base, err := resolveMutationBase(t.Context(), root, "no-such-branch", "")
	if err == nil {
		t.Fatal("a branch the remote does not hold resolved a merge-base")
	}
	if base != "" {
		t.Errorf("an unresolvable merge-base resolved %q alongside its error", base)
	}
	for _, want := range []string{"merge-base", "depth 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the run failed with %q, want it to say %q", err, want)
		}
	}
	if _, err := ResolveBase(t.Context(), ResolveBaseIn{Dir: root, BaseBranch: "no-such-branch"}); err == nil {
		t.Fatal("a branch the remote does not hold resolved a merge-base through the stage")
	}
}

// Without --affected every one of the run's own components is selected, and
// selection is never asked: a later stage reads Selected either way.
func TestWithoutAffectedEveryOwnComponentIsSelected(t *testing.T) {
	own := []component.Component{{Name: "app"}, {Name: "web"}}
	out, err := SelectAffected(t.Context(), SelectAffectedIn{
		Own: own,
		Affected: func(context.Context, string, component.File, string) (affected.Result, error) {
			t.Error("selection was asked for a run that did not ask for it")
			return affected.Result{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Selected, own) {
		t.Errorf("selected = %v, want every own component", names(out.Selected))
	}
	if len(out.Skipped) != 0 || len(out.Ordered) != 0 {
		t.Errorf("skipped %v and ordered %v, want neither", names(out.Skipped), names(out.Ordered))
	}
}

// With --affected the run mutates its own share of the one selection, from
// the base already resolved, and names the share it skipped so the skipped
// rows can sit where the declaration puts them.
func TestAffectedNarrowsTheRunsOwnShareFromTheResolvedBase(t *testing.T) {
	app, web, api := component.Component{Name: "app"}, component.Component{Name: "web"}, component.Component{Name: "api"}
	file := component.File{Components: []component.Component{app, web, api}}
	res := affected.Result{Selected: []component.Component{web, api}, Skipped: []component.Component{app}}
	out, err := SelectAffected(t.Context(), SelectAffectedIn{
		Only: true,
		Dir:  "root",
		File: file,
		Own:  []component.Component{app, web},
		Base: "base",
		Affected: func(_ context.Context, dir string, f component.File, base string) (affected.Result, error) {
			if dir != "root" || base != "base" || !reflect.DeepEqual(f, file) {
				t.Errorf("selection asked about %q at %q over %v", dir, base, names(f.Components))
			}
			return res, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(out.Selected); !reflect.DeepEqual(got, []string{"web"}) {
		t.Errorf("selected = %v, want web alone — api is another run's", got)
	}
	if got := names(out.Skipped); !reflect.DeepEqual(got, []string{"app"}) {
		t.Errorf("skipped = %v, want app", got)
	}
	if got := names(out.Ordered); !reflect.DeepEqual(got, []string{"app", "web"}) {
		t.Errorf("ordered = %v, want the run's own components in declaration order", got)
	}
	if !reflect.DeepEqual(out.Selection, res) {
		t.Errorf("selection = %+v, want the whole declaration's", out.Selection)
	}

	failed := errors.New("no diff")
	_, err = SelectAffected(t.Context(), SelectAffectedIn{Only: true,
		Affected: func(context.Context, string, component.File, string) (affected.Result, error) {
			return affected.Result{}, failed
		}})
	if !errors.Is(err, failed) {
		t.Errorf("err = %v, want selection's own failure", err)
	}
}

// The change is read once for the run, and the file listing a worker
// directory is copied from only for a language whose mutants need one.
func TestScopeChangeListsFilesOnlyForAWorkerDirectory(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n", "web/a.ts": "export const x = 1;\n"})
	base := git(t, root, "rev-parse", "main")
	shape := &fakeShape{t: t, lang: runnerLang}

	goOnly, err := ScopeChange(t.Context(), ScopeChangeIn{Shape: shape, Dir: root, Base: base,
		Selected: []component.Component{{Name: "app", Dir: "app", Runner: "go-test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if goOnly.Files != nil {
		t.Errorf("a Go-only run listed %v, want no listing at all", goOnly.Files)
	}
	if _, ok := goOnly.Changed["app/a.go"]; !ok {
		t.Errorf("changed = %v, want app/a.go's added lines", goOnly.Changed)
	}

	withWeb, err := ScopeChange(t.Context(), ScopeChangeIn{Shape: shape, Dir: root, Base: base,
		Selected: []component.Component{{Name: "app", Dir: "app", Runner: "go-test"}, {Name: "web", Dir: "web", Runner: "vitest"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withWeb.Files, []string{"app/a.go", "web/a.ts"}) {
		t.Errorf("files = %v, want every file git lists", withWeb.Files)
	}
	if _, ok := withWeb.Changed["web/a.ts"]; !ok {
		t.Errorf("changed = %v, want web/a.ts's added lines", withWeb.Changed)
	}
}

// Go needs no worker directory: an overlay names the mutated file wherever it
// is written. Rust, TypeScript and Python have no such instruction, so their
// mutants run in a copy of the repository — and one git lists no file in has
// nothing to copy, which is said out loud rather than reported as a component
// whose suite killed everything.
func TestOnlyALanguageWithNoOverlayNeedsAWorkerDirectory(t *testing.T) {
	goComponent := component.Component{Name: "cli", Dir: ".", Runner: "go-test"}
	web := component.Component{Name: "web", Dir: ".", Runner: "vitest"}
	py := component.Component{Name: "py", Dir: ".", Runner: "python-pytest"}
	shape := &fakeShape{t: t, lang: runnerLang}

	if needsWorktree(shape, []component.Component{goComponent}) {
		t.Error("a Go component asked for a worker directory")
	}
	if !needsWorktree(shape, []component.Component{goComponent, web}) {
		t.Error("a TypeScript component did not ask for a worker directory")
	}
	if !needsWorktree(shape, []component.Component{goComponent, py}) {
		t.Error("a Python component did not ask for a worker directory")
	}

	none := func(context.Context, string) error { return nil }
	backend, err := backendFor(runner.Go, t.TempDir(), "cli", runner.Invocation{}, runner.Invocation{}, nil, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Go); !ok {
		t.Errorf("Go got %T, want the overlay backend", backend)
	}
	backend, err = backendFor(runner.TypeScript, t.TempDir(), "web", runner.Invocation{}, runner.Invocation{}, []string{"web/src/a.ts"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Tree); !ok {
		t.Errorf("TypeScript got %T, want the worker-directory backend", backend)
	}
	backend, err = backendFor(runner.Python, t.TempDir(), "py", runner.Invocation{}, runner.Invocation{}, []string{"py/a.py"}, none)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(mutation.Tree); !ok {
		t.Errorf("Python got %T, want the worker-directory backend", backend)
	}
	if _, err := backendFor(runner.Rust, t.TempDir(), "rust", runner.Invocation{}, runner.Invocation{}, nil, none); err == nil {
		t.Error("a scan root git lists no file under was given a worker directory to copy nothing into")
	}
	if _, err := backendFor(runner.Shell, t.TempDir(), "sh", runner.Invocation{}, runner.Invocation{}, []string{"a.sh"}, none); err == nil {
		t.Error("a language with no backend was given one")
	}

	// A component rooted at the scan root is where every path join in the
	// backend collapses, so it is the case least likely to be noticed and the
	// one a `.`-rooted repository always takes.
	backend, err = backendFor(runner.TypeScript, t.TempDir(), ".", runner.Invocation{}, runner.Invocation{}, []string{"src/a.ts"}, none)
	if err != nil {
		t.Fatal(err)
	}
	tree, ok := backend.(mutation.Tree)
	if !ok {
		t.Fatalf("a component at the scan root got %T, want the worker-directory backend", backend)
	}
	if tree.Component != "." {
		t.Errorf("a component declared at %q became %q", ".", tree.Component)
	}
}
