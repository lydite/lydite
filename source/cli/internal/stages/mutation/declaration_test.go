package mutationstages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

func stateScope(t *testing.T, root, stateDir string, comps ...component.Component) ScopeChangeOut {
	t.Helper()
	out, err := ScopeChange(t.Context(), ScopeChangeIn{Shape: &fakeShape{t: t, lang: runnerLang}, Dir: root,
		Base: git(t, root, "rev-parse", "main"), Selected: comps, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A Go-only run has no worker directory to copy and still digests the tree
// when resume is on; with resume off it computes none.
func TestScopeChangeDigestsTheTreeOfAGoOnlyRunWhenResumeIsOn(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n"})
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}

	off := stateScope(t, root, "", app)
	if off.TreeDigest != "" {
		t.Errorf("digest = %q with resume off, want none", off.TreeDigest)
	}
	on := stateScope(t, root, filepath.Join(root, ".lydite-state"), app)
	if on.TreeDigest == "" {
		t.Fatal("a Go-only run with resume on computed no digest")
	}
	if on.Files != nil {
		t.Errorf("a Go-only run handed on %v, want no listing", on.Files)
	}
	writeFile(t, root, "app/a.go", "package a\n\nvar X = 2\n")
	if changed := stateScope(t, root, filepath.Join(root, ".lydite-state"), app); changed.TreeDigest == on.TreeDigest {
		t.Error("editing a file left the digest unchanged")
	}
}

// A tracked file deleted from the worktree is an ordinary dirty tree: the digest
// is still computed, and it moves.
func TestScopeChangeDigestsATreeWithADeletedTrackedFile(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n", "app/b.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n"})
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	state := filepath.Join(root, ".lydite-state")

	before := stateScope(t, root, state, app)
	if err := os.Remove(filepath.Join(root, "app", "b.go")); err != nil {
		t.Fatal(err)
	}
	after := stateScope(t, root, state, app)
	if after.TreeDigest == "" || after.TreeDigest == before.TreeDigest {
		t.Errorf("digest = %q after a deletion, want one different from %q", after.TreeDigest, before.TreeDigest)
	}
}

// A listed entry that is a directory, as a symlink to one is, is skipped rather
// than failing the digest.
func TestScopeChangeSkipsAListedDirectory(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n"})
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	if err := os.Symlink("app", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "link")

	if out := stateScope(t, root, filepath.Join(root, ".lydite-state"), app); out.TreeDigest == "" {
		t.Error("a directory in the listing left no digest")
	}
}

// A state root that cannot be created switches resume off with one diagnostic
// line and never fails the run; a worker directory's listing is still handed on.
func TestScopeChangeSwitchesResumeOffWhenTheStateRootCannotBeCreated(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n", "web/a.ts": "export const x = 1;\n"})
	blocker := filepath.Join(root, "blocker")
	writeFile(t, root, "blocker", "a file\n")
	state := filepath.Join(blocker, "state")
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	web := component.Component{Name: "web", Dir: "web", Runner: "vitest"}

	for _, c := range []struct {
		name      string
		selected  []component.Component
		wantFiles bool
	}{
		{"go only", []component.Component{app}, false},
		{"with a worker directory", []component.Component{app, web}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var diag strings.Builder
			out, err := ScopeChange(t.Context(), ScopeChangeIn{Shape: &fakeShape{t: t, lang: runnerLang}, Dir: root,
				Base: git(t, root, "rev-parse", "main"), Selected: c.selected, StateDir: state, Diagnostics: &diag})
			if err != nil {
				t.Fatalf("an unusable state root failed the run: %v", err)
			}
			if out.TreeDigest != "" {
				t.Errorf("digest = %q, want none with resume off", out.TreeDigest)
			}
			if (out.Files != nil) != c.wantFiles {
				t.Errorf("files = %v, want listed = %v", out.Files, c.wantFiles)
			}
			if _, ok := out.Changed["app/a.go"]; !ok {
				t.Errorf("changed = %v, want the change still read", out.Changed)
			}
			got := diag.String()
			if strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "warning: mutation state is off: ") ||
				!strings.HasSuffix(got, "; every mutant is measured\n") {
				t.Errorf("diagnostics = %q, want one state-off line", got)
			}
		})
	}
}

// A listing the digest cannot read is a cache that is not there: the run loses
// its resume, says so once, and is not failed.
func TestScopeChangeSwitchesResumeOffWhenTheTreeCannotBeDigested(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n", "dir/inner.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n"})
	// The listing names dir/inner.go, and a file where its directory was makes
	// stat answer something other than "not there".
	if err := os.RemoveAll(filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "dir", "now a file\n")
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}

	var diag strings.Builder
	out, err := ScopeChange(t.Context(), ScopeChangeIn{Shape: &fakeShape{t: t, lang: runnerLang}, Dir: root,
		Base: git(t, root, "rev-parse", "main"), Selected: []component.Component{app},
		StateDir: filepath.Join(root, ".lydite-state"), Diagnostics: &diag})
	if err != nil {
		t.Fatalf("an undigestable tree failed the run: %v", err)
	}
	if out.TreeDigest != "" {
		t.Errorf("digest = %q, want none", out.TreeDigest)
	}
	if got := diag.String(); strings.Count(got, "\n") != 1 || !strings.HasPrefix(got, "warning: mutation state is off: ") {
		t.Errorf("diagnostics = %q, want one state-off line", got)
	}
}

// The state root is ignored by git, kept out of the digest and out of the
// listing a worker directory is copied from.
func TestScopeChangeKeepsTheStateRootOutOfTheTree(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"app/a.go": "package a\n"},
		map[string]string{"app/a.go": "package a\n\nvar X = 1\n", "web/a.ts": "export const x = 1;\n"})
	state := filepath.Join(root, ".lydite-state")
	app := component.Component{Name: "app", Dir: "app", Runner: "go-test"}
	web := component.Component{Name: "web", Dir: "web", Runner: "vitest"}

	first := stateScope(t, root, state, app, web)
	ignore, err := os.ReadFile(filepath.Join(state, ".gitignore"))
	if err != nil || string(ignore) != "*\n" {
		t.Fatalf(".gitignore = %q, %v, want %q", ignore, err, "*\n")
	}
	writeFile(t, state, "ledger.jsonl", "one\n")
	writeFile(t, state, "sub/more.jsonl", "two\n")
	second := stateScope(t, root, state, app, web)

	if first.TreeDigest != second.TreeDigest {
		t.Error("writing into the state root changed the digest")
	}
	for _, f := range second.Files {
		if strings.HasPrefix(f, ".lydite-state/") {
			t.Errorf("files hold %s from the state root", f)
		}
	}
	if !reflect.DeepEqual(second.Files, []string{"app/a.go", "web/a.ts"}) {
		t.Errorf("files = %v, want the tree without the state root", second.Files)
	}
}

// A state root whose ignore file cannot be written switches resume off instead
// of leaving state git would list as part of the tree.
func TestIgnoreStateReportsAnIgnoreFileItCannotWrite(t *testing.T) {
	state := t.TempDir()
	// Stat on a dangling link finds nothing and a write through it fails.
	if err := os.Symlink(filepath.Join(state, "missing", "target"), filepath.Join(state, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	if err := ignoreState(state); err == nil {
		t.Error("an ignore file that could not be written was reported as written")
	}
}

// withoutState drops what lies under the state root and leaves the listing
// alone wherever the root is none, is the scan root itself, or is outside it.
func TestWithoutStateDropsOnlyWhatLiesUnderTheStateRoot(t *testing.T) {
	root := t.TempDir()
	files := []string{"a.go", ".lydite-state/verdicts.jsonl", ".lydite-state-other/x", "b/c.go"}
	for name, c := range map[string]struct {
		stateDir string
		want     []string
	}{
		"none":            {"", files},
		"under the root":  {filepath.Join(root, ".lydite-state"), []string{"a.go", ".lydite-state-other/x", "b/c.go"}},
		"the root itself": {root, files},
		"outside":         {filepath.Join(filepath.Dir(root), "elsewhere"), files},
	} {
		t.Run(name, func(t *testing.T) {
			if got := withoutState(files, root, c.stateDir); !slices.Equal(got, c.want) {
				t.Errorf("withoutState = %v, want %v", got, c.want)
			}
		})
	}
}

// A working directory that has gone cannot make a relative path absolute, and
// the listing is then left as it is.
func TestWithoutStateLeavesTheListingAloneWhenPathsCannotBeResolved(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Skipf("the working directory cannot be removed here: %v", err)
	}
	files := []string{"a.go", "state/x"}
	abs := t.TempDir()
	if got := withoutState(files, ".", "state"); !slices.Equal(got, files) {
		t.Errorf("a relative scan root: got %v, want the listing unchanged", got)
	}
	if got := withoutState(files, abs, "state"); !slices.Equal(got, files) {
		t.Errorf("a relative state root: got %v, want the listing unchanged", got)
	}
}
