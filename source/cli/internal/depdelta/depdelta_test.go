package depdelta

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// extract reads one side of a fixture the way a caller reads a manifest: by
// the path alone, with the content handed to Extract.
func extract(t *testing.T, side, name string) Set {
	t.Helper()
	p := filepath.Join("testdata", side, name)
	content, err := os.ReadFile(p) // #nosec G304 -- a fixture path this test built
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	set, err := Extract(Detect(p), content, p)
	if err != nil {
		t.Fatalf("extracting %s: %v", p, err)
	}
	return set
}

// compare is the delta over one fixture case's two sides.
func compare(t *testing.T, dir, manifest string) Delta {
	t.Helper()
	return Compare(extract(t, filepath.Join(dir, "base"), manifest), extract(t, filepath.Join(dir, "head"), manifest))
}

// changed renders the delta's version moves, so an assertion names the pair
// rather than an index into a slice.
func changed(d Delta) []string {
	out := make([]string, 0, len(d.Changed))
	for _, c := range d.Changed {
		out = append(out, c.Name+" "+strings.Join(c.Base, ",")+" -> "+strings.Join(c.Head, ","))
	}
	return out
}

// A manifest is recognised by its file name wherever in the tree it sits,
// because the changed paths are what a diff answers completely.
func TestDetectReadsTheBaseName(t *testing.T) {
	for path, want := range map[string]Manifest{
		"go.mod":                                        ManifestGoMod,
		"source/cli/go.sum":                             ManifestGoSum,
		"source/cli/internal/rust/deny-pin/Cargo.lock":  ManifestCargoLock,
		"source/cloud-services/package-lock.json":       ManifestNPMLock,
		"source/web/yarn.lock":                          ManifestYarnLock,
		"pnpm-lock.yaml":                                ManifestPnpmLock,
		".github/scripts/requirements.txt":              ManifestRequirements,
		"tools/poetry.lock":                             ManifestPoetryLock,
		"Pipfile.lock":                                  ManifestPipfileLock,
		"source/cli/internal/rust/deny-pin/Cargo.toml":  ManifestNone,
		"source/cloud-services/pr-relay/package.json":   ManifestNone,
		"source/cli/internal/depdelta/depdelta_test.go": ManifestNone,
	} {
		if got := Detect(path); got != want {
			t.Errorf("Detect(%q) = %q, want %q", path, got, want)
		}
	}
}

// A yarn, pnpm or pip manifest is a named gap and not a silent one. It has no
// reader, and the error says which ecosystem went unread, because an ecosystem
// quietly outside the check is indistinguishable from one that had nothing to
// say.
func TestAnUnreadableManifestNamesItsEcosystem(t *testing.T) {
	for path, want := range map[string]Ecosystem{
		"yarn.lock":        EcosystemYarn,
		"pnpm-lock.yaml":   EcosystemPnpm,
		"requirements.txt": EcosystemPip,
		"poetry.lock":      EcosystemPip,
		"Pipfile.lock":     EcosystemPip,
	} {
		m := Detect(path)
		if m.Readable() {
			t.Errorf("%s reads as readable", path)
		}
		if got := m.Ecosystem(); got != want {
			t.Errorf("Detect(%q).Ecosystem() = %q, want %q", path, got, want)
		}
		if _, err := Extract(m, nil, path); err == nil || !strings.Contains(err.Error(), string(want)) {
			t.Errorf("Extract(%q) error = %v, want one naming %q", path, err, want)
		}
	}
}

// A manifest whose content does not parse is an error and never an empty set.
// An empty set is the claim that the change added no dependency, which is
// exactly what nobody knows about a file that could not be read.
func TestUnparseableContentIsAnErrorAndNotAnEmptySet(t *testing.T) {
	if _, err := Extract(ManifestNPMLock, []byte("{ not json"), "package-lock.json"); err == nil {
		t.Error("a malformed package-lock.json extracted a set")
	}
	if _, err := Extract(ManifestGoSum, []byte("github.com/example/pkg v1.2.3\n"), "go.sum"); err == nil {
		t.Error("a go.sum line missing its hash extracted a set")
	}
}

// go.sum is the fuller source: it pins every module the build resolves, and
// the two lines per version — the zip's hash and the go.mod's — are the one
// version they name.
func TestGoSumHoldsEveryPinnedModuleOncePerVersion(t *testing.T) {
	set := extract(t, filepath.Join("go", "head"), "go.sum")
	want := []string{
		"github.com/cpuguy83/go-md2man/v2",
		"github.com/github/go-spdx/v2",
		"github.com/google/licensecheck",
		"github.com/kr/pretty",
		"github.com/spf13/cobra",
		"github.com/spf13/pflag",
		"golang.org/x/mod",
	}
	if got := set.Names(); !slices.Equal(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if got, want := set.Versions("golang.org/x/mod"), []string{"v0.41.0"}; !slices.Equal(got, want) {
		t.Fatalf("golang.org/x/mod = %v, want %v", got, want)
	}
}

// A `go mod tidy` that pulls in a module is an addition whatever the versions
// beside it did, and an `x/mod` minor inside its `0.x` line is not the routine
// maintenance a patch-and-minor exemption covers.
func TestAGoSumDeltaSeparatesTheAdditionFromTheBumps(t *testing.T) {
	d := compare(t, "go", "go.sum")
	if want := []string{"github.com/github/go-spdx/v2"}; !slices.Equal(d.Added, want) {
		t.Fatalf("added = %v, want %v", d.Added, want)
	}
	if len(d.Removed) != 0 {
		t.Fatalf("removed = %v, want none", d.Removed)
	}
	want := []string{
		"github.com/spf13/cobra v1.10.1 -> v1.10.2",
		"golang.org/x/mod v0.40.0 -> v0.41.0",
	}
	if got := changed(d); !slices.Equal(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	if d.PatchOrMinorEligible() {
		t.Error("a minor inside a 0.x line is eligible")
	}
}

// The manifest states what the module itself requires; go.sum states what the
// build pins. The two answer different questions and the manifest's answer is
// the smaller one.
func TestGoModStatesOnlyWhatTheManifestRequires(t *testing.T) {
	mod := extract(t, filepath.Join("go", "head"), "go.mod")
	sum := extract(t, filepath.Join("go", "head"), "go.sum")
	if mod.Len() >= sum.Len() {
		t.Fatalf("go.mod holds %d modules and go.sum %d", mod.Len(), sum.Len())
	}
	if !mod.Has("github.com/spf13/pflag") {
		t.Error("an indirect require is not in the manifest's set")
	}
	if mod.Has("github.com/kr/pretty") {
		t.Error("a module only the build graph names is in the manifest's set")
	}
	if got, want := mod.Versions("github.com/spf13/cobra"), []string{"v1.10.2"}; !slices.Equal(got, want) {
		t.Fatalf("cobra = %v, want %v", got, want)
	}
}

// A `=0.20.1` pin becoming `=0.20.2` is an ordinary dependency edit: the
// lockfile it writes is read like any other, and a patch inside a non-zero
// `0.x` minor is the boundary cargo's own caret default treats as compatible.
func TestACargoPinBumpIsPatchOrMinorEligible(t *testing.T) {
	d := compare(t, "cargo", "Cargo.lock")
	if len(d.Added) != 0 || len(d.Removed) != 0 {
		t.Fatalf("added = %v, removed = %v, want neither", d.Added, d.Removed)
	}
	want := []string{
		"anstyle 1.0.13 -> 1.0.14",
		"cargo-deny 0.20.1 -> 0.20.2",
	}
	if got := changed(d); !slices.Equal(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	if !d.PatchOrMinorEligible() {
		t.Error("a pin's patch bump is not eligible")
	}
}

// A group update moves several packages at once, and the condition is on every
// pair rather than on most of them.
func TestAnNPMGroupUpdateMovesEveryPackageInTheGroup(t *testing.T) {
	d := compare(t, "npm-group", "package-lock.json")
	want := []string{
		"@biomejs/biome 2.5.11 -> 2.5.12",
		"@biomejs/cli-darwin-arm64 2.5.11 -> 2.5.12",
	}
	if got := changed(d); !slices.Equal(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	if !d.PatchOrMinorEligible() {
		t.Error("a patch group update is not eligible")
	}
}

// A major bump brings its own additions with it, and one major among several
// patches is not made boring by them. The repository's own code — the root
// entry and a workspace member's link — is not a dependency the change added.
func TestAVitestMajorAddsPackagesAndIsNotEligible(t *testing.T) {
	d := compare(t, "npm-major", "package-lock.json")
	if want := []string{"@vitest/mocker"}; !slices.Equal(d.Added, want) {
		t.Fatalf("added = %v, want %v", d.Added, want)
	}
	want := []string{
		"@vitest/expect 3.2.4 -> 4.0.1",
		"@vitest/runner 3.2.4 -> 4.0.1",
		"vite 6.3.5 -> 7.1.5",
		"vitest 3.2.4 -> 4.0.1",
	}
	if got := changed(d); !slices.Equal(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	if d.PatchOrMinorEligible() {
		t.Error("a major bump is eligible")
	}
	if extract(t, filepath.Join("npm-major", "head"), "package-lock.json").Has("pr-relay") {
		t.Error("a workspace member is read as a dependency")
	}
}

// A package the change dropped is reported and never gated on: a removal is
// the opposite of new untrusted code arriving.
func TestARemovalIsReportedAndLeavesTheDeltaEligible(t *testing.T) {
	base := NewSet(map[string][]string{"serde": {"1.0.219"}, "toml": {"0.8.19"}})
	head := NewSet(map[string][]string{"serde": {"1.0.219"}})
	d := Compare(base, head)
	if want := []string{"toml"}; !slices.Equal(d.Removed, want) {
		t.Fatalf("removed = %v, want %v", d.Removed, want)
	}
	if len(d.Added) != 0 || len(d.Changed) != 0 {
		t.Fatalf("added = %v, changed = %v, want neither", d.Added, changed(d))
	}
	if !d.PatchOrMinorEligible() {
		t.Error("a delta whose only move is a removal is not eligible")
	}
}

// A set is built once from whatever order a reader produced, so two readings
// of one manifest can never differ by a map's iteration order.
func TestASetSortsAndDeduplicatesEveryPackagesVersions(t *testing.T) {
	set := NewSet(map[string][]string{"rand": {"0.9.0", "0.8.5", "0.9.0"}})
	if got, want := set.Versions("rand"), []string{"0.8.5", "0.9.0"}; !slices.Equal(got, want) {
		t.Fatalf("rand = %v, want %v", got, want)
	}
}
