package affected

import (
	"strings"
	"testing"

	"lydite/lydite/internal/component"
)

// provingGround mirrors lydite/proving-ground's own .lydite/components.yml,
// which is the declaration the CI assertion runs against. The names and the
// directories differ on purpose there — the components are tally, api, sdk and
// web while the directories are rust, go/api, go/sdk and web — so a rule that
// confuses the two matches nothing here and passes.
func provingGround() component.File {
	return component.File{Components: []component.Component{
		{Name: "tally", Dir: "rust", Runner: "cargo-nextest", Watch: []string{"Makefile", "VERSION"}},
		{Name: "api", Dir: "go/api", Runner: "go-test"},
		{Name: "sdk", Dir: "go/sdk", Runner: "go-test", DependsOn: []string{"api"}, Watch: []string{"docs/openapi.json"}},
		{Name: "web", Dir: "web", Runner: "vitest", DependsOn: []string{"api"}, Watch: []string{"docs/openapi.json"}},
	}}
}

func names(cs []component.Component) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestSelect(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed []string
		want    string
	}{
		// The four cases lydite/proving-ground was built to answer.
		{"a watched file selects only its watcher", []string{"VERSION"}, "tally"},
		{"a watched file no dependency watches", []string{"docs/openapi.json"}, "sdk,web"},
		{"a directory selects its dependents transitively", []string{"go/api/main.go"}, "api,sdk,web"},
		{"a lockfile is an invalidator", []string{"go/api/go.sum"}, "tally,api,sdk,web"},

		// The posture: ignorance widens.
		{"a path under nothing selects everything", []string{"docs/design/notes.md"}, "tally,api,sdk,web"},
		{"the component declaration selects everything", []string{".lydite/components.yml"}, "tally,api,sdk,web"},

		{"a directory selects the component rooted at it", []string{"web/src/app.ts"}, "web"},
		{"a deletion selects its component", []string{"go/sdk/client.go"}, "sdk"},
		{"an empty diff selects nothing", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := names(Select(provingGround(), Paths("", tc.changed)).Selected)
			if got != tc.want {
				t.Errorf("Select(%v) = %q, want %q", tc.changed, got, tc.want)
			}
		})
	}
}

// TestSelectedIsEmptyOnlyWhenTheDiffIs holds ADR 0018's invariant. Every
// changed path selects at least one component: it matched something, or it
// matched nothing and therefore matched everything. A narrowing bug anywhere
// in Select breaks this before it reaches a report.
func TestSelectedIsEmptyOnlyWhenTheDiffIs(t *testing.T) {
	for _, changed := range [][]string{
		{"README.md"}, {"Makefile"}, {"rust/Cargo.toml"}, {"go/api/main.go"},
		{".github/workflows/ci.yml"}, {"web/package.json"}, {"nothing/at/all.txt"},
		{"VERSION", "docs/openapi.json"},
	} {
		if got := Select(provingGround(), Paths("", changed)).Selected; len(got) == 0 {
			t.Errorf("Select(%v) selected nothing; a non-empty diff must select at least one component", changed)
		}
	}
}

// TestSkippedAccountsForEveryComponent is what lets a reader tell "not
// affected" from "not declared": a deselected component is reported, never
// dropped.
func TestSkippedAccountsForEveryComponent(t *testing.T) {
	f := provingGround()
	res := Select(f, Paths("", []string{"VERSION"}))
	if got, want := names(res.Selected), "tally"; got != want {
		t.Fatalf("selected = %q, want %q", got, want)
	}
	if got, want := names(res.Skipped), "api,sdk,web"; got != want {
		t.Errorf("skipped = %q, want %q", got, want)
	}
	if len(res.Selected)+len(res.Skipped) != len(f.Components) {
		t.Errorf("selected+skipped = %d, want every one of %d declared components",
			len(res.Selected)+len(res.Skipped), len(f.Components))
	}
}

// TestRenameSelectsBothSides: moving a file between components must run both.
// The source lost a file and the destination gained one, and a rule reading
// only the destination runs the half of the change that cannot break.
func TestRenameSelectsBothSides(t *testing.T) {
	got := names(Select(provingGround(), Paths("", []string{"go/sdk/moved.go", "web/src/moved.ts"})).Selected)
	if want := "sdk,web"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
}

// TestARootComponentDoesNotSuppressWidening: a component rooted at "."
// contains every path, and that containment is not evidence the path was
// understood. It is still selected — a Go module at the repository root has no
// other way to spell where it lives — but the path counts as unmatched, so
// every other component is selected too.
//
// Reading the containment as a match switches the widening rule off for the
// whole repository: nothing is ever unmatched, and a root-level Makefile
// selects the root component alone while web silently does not run.
func TestARootComponentDoesNotSuppressWidening(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "root", Dir: ".", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	for _, p := range []string{"main.go", "Makefile", "tsconfig.base.json"} {
		res := Select(f, Paths("", []string{p}))
		if got, want := names(res.Selected), "root,web"; got != want {
			t.Errorf("Select(%q) = %q, want %q — containment by a root component is not a match", p, got, want)
		}
		// The reason the reader sees for the component they actually edited
		// is still their own path, not the widening.
		if got, want := res.Reasons["root"].Path, p; got != want {
			t.Errorf("Select(%q): root's reason path = %q, want %q", p, got, want)
		}
	}
	// A path web contains is matched, so nothing widens. The root component
	// is still selected, because it contains that path too — containment is
	// what selects, and only what counts as a match has changed.
	res := Select(f, Paths("", []string{"web/src/app.ts"}))
	if got, want := names(res.Selected), "root,web"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
	if got, want := res.Reasons["root"].Kind, KindDir; got != want {
		t.Errorf("root was selected by %q, want %q — not by the widening", got, want)
	}
}

// A matched path does not widen, and with no `.`-rooted component in the
// declaration that is visible as a narrower selection.
func TestAMatchedPathDoesNotWiden(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "api", Dir: "go/api", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	if got, want := names(Select(f, Paths("", []string{"web/src/app.ts"})).Selected), "web"; got != want {
		t.Errorf("selected = %q, want %q — a real directory match narrows", got, want)
	}
}

// TestDeclarationOrder: rows must read the same however the diff was ordered,
// the same rule the scheduler follows for its own rows.
func TestDeclarationOrder(t *testing.T) {
	got := names(Select(provingGround(), Paths("", []string{"web/src/app.ts", "VERSION", "go/api/main.go"})).Selected)
	if want := "tally,api,sdk,web"; got != want {
		t.Errorf("selected = %q, want declaration order %q", got, want)
	}
}

// TestOutsideTheScanRootWidens: a component directory is scan-root relative,
// so a repository-root path that happens to spell one is not that component's
// file. Selecting it would narrow a change lydite cannot see the consequences
// of — ADR 0018 promises the opposite.
func TestOutsideTheScanRootWidens(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "docs-site", Dir: "docs", Runner: "vitest"},
		{Name: "api", Dir: "go/api", Runner: "go-test"},
	}}
	// Scanned with --dir source: "docs/README.md" at the repository root is
	// not "source/docs/README.md", however alike the two read.
	got := names(Select(f, Paths("source/", []string{"docs/README.md"})).Selected)
	if want := "docs-site,api"; got != want {
		t.Errorf("selected = %q, want %q — a path outside the scan root matches no component", got, want)
	}
	// Inside it, the same component is selected alone.
	got = names(Select(f, Paths("source/", []string{"source/docs/README.md"})).Selected)
	if want := "docs-site"; got != want {
		t.Errorf("selected = %q, want %q", got, want)
	}
}

// TestReasonNamesTheReadersOwnEdit: a component touched directly keeps that
// path as its reason even when something else in the change widened to
// everything. The audit line exists to be acted on, and one pointing at a
// lockfile when the reader edited the component is worse than none.
func TestReasonNamesTheReadersOwnEdit(t *testing.T) {
	for _, changed := range [][]string{
		{"web/src/app.ts", ".lydite/config.yml"},
		{".lydite/config.yml", "web/src/app.ts"},
	} {
		res := Select(provingGround(), Paths("", changed))
		if got, want := res.Reasons["web"].Path, "web/src/app.ts"; got != want {
			t.Errorf("Select(%v): web's reason = %q, want %q", changed, got, want)
		}
		if got, want := res.Reasons["tally"].String(), ".lydite/config.yml (invalidator)"; got != want {
			t.Errorf("Select(%v): tally's reason = %q, want %q", changed, got, want)
		}
	}
}

// The invalidator set earns its place where a path IS claimed by a component
// and still affects every other one. A lockfile inside web/ is matched by web
// and by nothing else, so without the set it would select web alone — when
// what a lockfile changes is what every component in the tree resolves to.
//
// A path no component claims already widens, so nothing on the set has to
// carry that case.
func TestAnInvalidatorOverridesAMatchThatIsTooNarrow(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "api", Dir: "go/api", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	for _, p := range []string{"web/package-lock.json", "go/api/go.mod", ".lydite/components.yml"} {
		if got, want := names(Select(f, Paths("", []string{p})).Selected), "api,web"; got != want {
			t.Errorf("Select(%q) = %q, want %q — an invalidator selects every component", p, got, want)
		}
	}
}

// An exclude does not narrow selection, and that is deliberate.
//
// It states that no component *tests* a path, which is not the same as
// nothing depending on it: a file under no component's directory can still be
// imported into one, and the proving ground's own excluded generated/client.ts
// is derived from a spec and consumed by a component. Reading one declaration
// as the answer to two questions is the mistake the orphan gate already
// refuses to make with rust.enabled — and an exclude that narrowed would let a
// repository make changes to a path run nothing at all, by editing the file
// whose history is meant to record what goes untested.
//
// The cost is real and on the safe side: a change to an excluded path runs
// every component.
func TestAnExcludeDoesNotNarrowSelection(t *testing.T) {
	f := component.File{
		Components: []component.Component{
			{Name: "a", Dir: "moda", Runner: "go-test"},
			{Name: "b", Dir: "modb", Runner: "go-test"},
		},
		Excludes: []string{"scripts/**"},
	}
	if got, want := names(Select(f, Paths("", []string{"scripts/seed.ts"})).Selected), "a,b"; got != want {
		t.Errorf("selected = %q, want %q — an exclude says nobody tests the path, not that nothing depends on it", got, want)
	}
}
