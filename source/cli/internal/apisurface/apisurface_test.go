package apisurface

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate and component a test asks for, which Compare carries through
// untouched.
const (
	testGate      = "api"
	testComponent = "sdk"
)

// Compare is read over the same probe repository the measurement uses, so what
// it reports is tied to what the tool was recorded saying rather than to a
// second fixture that could drift from it.
func TestCompareReportsEachIncompatibleShape(t *testing.T) {
	cases := []struct {
		shape string
		// message is what the recorded report says about this shape.
		message string
		// site is the symbol the finding is identified by.
		site string
		// removed is a symbol the head no longer declares, located at its
		// merge-base declaration.
		removed bool
		// line is the declaration the finding must point at.
		line int
	}{
		{shape: "removed-function", message: "Removed: removed", site: "Removed", removed: true, line: 8},
		{shape: "changed-signature", message: "Signature: changed from func(int) error to func(int, string) error", site: "Signature", line: 11},
		{shape: "interface-method", message: "Store.Put: added", site: "Store.Put", line: 17},
		{shape: "widened-field", message: "Config.Timeout: changed from int to int64", site: "Config.Timeout", line: 21},
	}
	repo := newProbe(t)
	for _, c := range cases {
		t.Run(c.shape, func(t *testing.T) {
			base := repo.worktree(t, repo.base)
			head := repo.worktree(t, repo.heads[c.shape])
			findings, err := Compare(base, head, testGate, testComponent, nil)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
			}
			got := findings[0]
			if got.Message != c.message {
				t.Errorf("Message = %q, want %q", got.Message, c.message)
			}
			if got.Site != c.site {
				t.Errorf("Site = %q, want %q", got.Site, c.site)
			}
			if got.Gate != testGate || got.Component != testComponent {
				t.Errorf("Gate/Component = %q/%q, want %q/%q", got.Gate, got.Component, testGate, testComponent)
			}
			if got.Path != "surface.go" {
				t.Errorf("Path = %q, want the tree-relative %q", got.Path, "surface.go")
			}
			if got.Line != c.line {
				t.Errorf("Line = %d, want the declaration at %d", got.Line, c.line)
			}
			// The declaration the line names is read back, so a fixture edit
			// that moves it fails here rather than silently pointing a reader
			// at the wrong code.
			tree := head
			if c.removed {
				tree = base
			}
			if declared := line(t, filepath.Join(tree, got.Path), got.Line); !strings.Contains(declared, lastSegment(c.site)) {
				t.Errorf("line %d of %s is %q, which does not declare %s", got.Line, got.Path, declared, c.site)
			}
			if c.removed {
				if len(got.Detail) != 1 || got.Detail[0] != removalDetail {
					t.Errorf("Detail = %q, want the removal note", got.Detail)
				}
			} else if len(got.Detail) != 0 {
				t.Errorf("Detail = %q, want none", got.Detail)
			}
			// A caller rebases Path onto the scan root, so anything absolute
			// here becomes a path no other producer's finding shares.
			if filepath.IsAbs(got.Path) {
				t.Errorf("Path = %q, want a path relative to the tree", got.Path)
			}
			if got.Ordinal != 0 || got.Anchor != "" || got.Rule != "" || got.Severity != "" {
				t.Errorf("finding is finished rather than raw: %+v", got)
			}
		})
	}
}

// A change that breaks nobody is not a claim. Ordinary growth reaching a gate
// would make the gate fire on every release.
func TestCompareIgnoresCompatibleAndUnreachableChanges(t *testing.T) {
	repo := newProbe(t)
	for _, shape := range []string{"compatible-addition", "excluded-packages"} {
		t.Run(shape, func(t *testing.T) {
			findings, err := Compare(repo.worktree(t, repo.base), repo.worktree(t, repo.heads[shape]), testGate, testComponent, nil)
			if err != nil {
				t.Fatalf("Compare: %v", err)
			}
			if len(findings) != 0 {
				t.Errorf("got %d findings, want none: %+v", len(findings), findings)
			}
		})
	}
}

// A package the head no longer declares is a break of its own, which no
// per-package comparison can see because there is nothing left to compare
// against.
func TestComparePackageRemovedAndAdded(t *testing.T) {
	base := module(t, map[string]string{
		"go.mod":       "module lydite.example/two\n\ngo 1.26\n",
		"root.go":      "package two\n\n// Root stays.\nfunc Root() {}\n",
		"leaf/leaf.go": "package leaf\n\n// Leaf goes away with its package.\nfunc Leaf() {}\n",
		"kept/kept.go": "package kept\n\n// Kept stays.\nfunc Kept() {}\n",
	})
	head := module(t, map[string]string{
		"go.mod":         "module lydite.example/two\n\ngo 1.26\n",
		"root.go":        "package two\n\n// Root stays.\nfunc Root() {}\n",
		"kept/kept.go":   "package kept\n\n// Kept stays.\nfunc Kept() {}\n",
		"added/added.go": "package added\n\n// Added breaks nobody.\nfunc Added() {}\n",
	})
	findings, err := Compare(base, head, testGate, testComponent, nil)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want only the removed package: %+v", len(findings), findings)
	}
	got := findings[0]
	if want := "package lydite.example/two/leaf: removed"; got.Message != want {
		t.Errorf("Message = %q, want %q", got.Message, want)
	}
	if want := "package lydite.example/two/leaf"; got.Site != want {
		t.Errorf("Site = %q, want %q", got.Site, want)
	}
	if got.Path != "leaf/leaf.go" || got.Line != 1 {
		t.Errorf("located at %s:%d, want the package clause at leaf/leaf.go:1", got.Path, got.Line)
	}
}

// head declaring more packages than base is the shape that tells union's
// capacity hint from a wrong one: base+head is a safe over-estimate whatever
// either holds, while base-head goes negative the moment head is the larger
// side, which make() panics on rather than silently mis-sizing.
func TestComparePackageAddedWithNothingRemoved(t *testing.T) {
	base := module(t, map[string]string{
		"go.mod":  "module lydite.example/grown\n\ngo 1.26\n",
		"root.go": "package grown\n\n// Root stays.\nfunc Root() {}\n",
	})
	head := module(t, map[string]string{
		"go.mod":         "module lydite.example/grown\n\ngo 1.26\n",
		"root.go":        "package grown\n\n// Root stays.\nfunc Root() {}\n",
		"added/added.go": "package added\n\n// Added breaks nobody.\nfunc Added() {}\n",
	})
	findings, err := Compare(base, head, testGate, testComponent, nil)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("a package only the head declares breaks nobody, got %+v", findings)
	}
}

// A module whose path moves cannot be compared at all: every package reads as
// removed and added again, so the caller is told rather than handed a module's
// worth of findings.
func TestCompareRefusesAMovedModulePath(t *testing.T) {
	base := module(t, map[string]string{
		"go.mod":  "module lydite.example/moved\n\ngo 1.26\n",
		"root.go": "package moved\n\n// Root stays.\nfunc Root() {}\n",
	})
	head := module(t, map[string]string{
		"go.mod":  "module lydite.example/moved/v2\n\ngo 1.26\n",
		"root.go": "package moved\n\n// Root stays.\nfunc Root() {}\n",
	})
	if _, err := Compare(base, head, testGate, testComponent, nil); !errors.Is(err, ErrModulePathChanged) {
		t.Fatalf("err = %v, want ErrModulePathChanged", err)
	}
}

// The loader runs the go tool over a tree the change under review controls,
// inside a job that holds a token that can write the referral status
// clearance depends on. Nothing token-shaped may reach it, whatever the
// caller's own environment carries or the ambient process exports.
func TestCredentialFreeDropsAnythingTokenShaped(t *testing.T) {
	in := []string{
		"GITHUB_TOKEN=ghs_secret",
		"GH_TOKEN=ghp_secret",
		"AWS_SECRET_ACCESS_KEY=aws",
		"API_KEY=k",
		"DB_PASSWORD=p",
		"SOME_CREDENTIAL=c",
		"github_token=lowercase-still-counts",
		"PATH=/usr/bin",
		"HOME=/root",
		"GOFLAGS=-mod=mod",
	}
	got := credentialFree(in)
	want := []string{"PATH=/usr/bin", "HOME=/root", "GOFLAGS=-mod=mod"}
	if len(got) != len(want) {
		t.Fatalf("credentialFree(%v) = %v, want %v", in, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// module writes a tree of Go sources and answers where it wrote them.
func module(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// line reads one line of a file, counting from one.
func line(t *testing.T, path string, n int) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a tree this test wrote or checked out
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if n < 1 || n > len(lines) {
		t.Fatalf("%s has no line %d", path, n)
	}
	return lines[n-1]
}

// lastSegment is the name a site's declaration line actually spells: a field
// or a method is named without the type that encloses it.
func lastSegment(site string) string {
	parts := strings.Split(site, ".")
	return parts[len(parts)-1]
}

// resolved must actually resolve a symlink, not merely pass every path
// through unchanged — the two are indistinguishable on a tree with no
// symlink in it, which is why this needs one built by hand rather than
// relying on Compare's own temp directories.
//
// The comparison is against resolved(real) rather than the literal path
// t.TempDir() returned, because on macOS that path is itself reached through
// a symlink (/var -> /private/var) — asserting equality with the raw string
// would fail for a reason this test has nothing to do with.
func TestResolvedFollowsASymlink(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want := resolved(real)
	if got := resolved(link); got != want {
		t.Errorf("resolved(%q) = %q, want the real path %q", link, got, want)
	}
	// Proof the two paths actually differ before resolution — otherwise a
	// negated check that always returned the input unchanged would pass this
	// test for the wrong reason.
	if link == want {
		t.Fatalf("link %q already equals the resolved path %q; this test proves nothing", link, want)
	}
}
