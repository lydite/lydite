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

// TestComponentAtRootContainsEverything: a component rooted at "." claims the
// whole tree, so every path is under its directory. The invalidator set exists
// for exactly this declaration — without it a change to .lydite/components.yml
// would select the root component alone and leave web unrun.
func TestComponentAtRootContainsEverything(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "root", Dir: ".", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	if got, want := names(Select(f, Paths("", []string{"main.go"})).Selected), "root"; got != want {
		t.Errorf("a root-rooted component claims main.go: got %q, want %q", got, want)
	}
	if got, want := names(Select(f, Paths("", []string{".lydite/components.yml"})).Selected), "root,web"; got != want {
		t.Errorf("the declaration must invalidate every component: got %q, want %q", got, want)
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
