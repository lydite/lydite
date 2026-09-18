package typescript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/licence"
)

// permissive is the shape of allow-list a repository shipping a binary states:
// it is this repository's own, out of .lydite/config.yml.
var permissive = []string{"Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MIT", "Unicode-3.0"}

// npmprobe materialises the npm probe tree and answers its root.
func npmprobe(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, filepath.Join("testdata", "npmprobe"))
}

// rejected is every pair the set holds, keyed by package, for an assertion that
// names one dependency at a time.
func rejected(t *testing.T, set licence.Set) map[string]licence.Dependency {
	t.Helper()
	out := map[string]licence.Dependency{}
	for _, d := range set.Dependencies() {
		out[d.Package] = d
	}
	return out
}

// A dependency whose lockfile entry states a licence the allow-list holds is
// not in the set at all, and one stating a licence it does not hold is, under
// the licence the lockfile stated.
func TestLicenceSetReadsTheLockfileLicenceField(t *testing.T) {
	set, err := LicenceSet(context.Background(), npmprobe(t), licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	for _, conforming := range []string{"typescript", "@babel/helper-string-parser", "wrangler", "pause-stream"} {
		if d, held := pairs[conforming]; held {
			t.Errorf("%s is in the non-conforming set as %s, want it allowed", conforming, d.Licence)
		}
	}
	d, held := pairs["lightningcss"]
	if !held {
		t.Fatal("lightningcss is MPL-2.0 and the allow-list does not hold it, want it in the set")
	}
	if d.Licence != "MPL-2.0" || d.Version != "1.33.0" {
		t.Errorf("lightningcss = %q at %q, want MPL-2.0 at 1.33.0", d.Licence, d.Version)
	}
}

// A nested duplicate is named by the segment after the last `node_modules/`,
// not by the whole install path.
func TestLicenceSetNamesANestedDuplicateByItsPackage(t *testing.T) {
	set, err := LicenceSet(context.Background(), npmprobe(t), licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	if _, held := pairs["@img/sharp-libvips-linux-x64"]; !held {
		t.Errorf("want the nested duplicate named @img/sharp-libvips-linux-x64, got %v", set.Dependencies())
	}
}

// A `licenses` array composes through licence.Expression: each type is one term
// of an OR, sorted, so the pair a reordering produces is the same pair.
func TestLicenceSetComposesALicensesArray(t *testing.T) {
	set, err := LicenceSet(context.Background(), npmprobe(t), licence.NewPolicy([]string{"ISC"}))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	d, held := rejected(t, set)["pause-stream"]
	if !held {
		t.Fatal("pause-stream states its licences as an array and the allow-list holds neither, want it in the set")
	}
	if d.Licence != "Apache-2.0 OR MIT" {
		t.Errorf("pause-stream = %q, want %q", d.Licence, "Apache-2.0 OR MIT")
	}
}

// An entry stating neither field is unknown, and unknown is in the set: a
// dependency dropped because its licence could not be read is one the gate
// silently allowed.
func TestLicenceSetReportsAnEntryStatingNoLicence(t *testing.T) {
	set, err := LicenceSet(context.Background(), npmprobe(t), licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	d, held := rejected(t, set)["unlicensed-probe"]
	if !held {
		t.Fatal("unlicensed-probe states no licence field, want it in the set as unknown")
	}
	if d.Licence != licence.Unknown {
		t.Errorf("unlicensed-probe = %q, want %q", d.Licence, licence.Unknown)
	}
}

// The workspace's own packages are not dependencies, under either of the two
// entries npm writes for one: the `link: true` alias and the source directory.
// Both state no licence, so a pass that kept them would report the repository's
// own code as an unknown-licence dependency.
func TestLicenceSetExcludesTheWorkspacesOwnPackages(t *testing.T) {
	set, err := LicenceSet(context.Background(), npmprobe(t), licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	for _, local := range []string{"@lydite/pr-relay", "pr-relay", ""} {
		if _, held := pairs[local]; held {
			t.Errorf("%q is the workspace's own package, want it out of the set entirely", local)
		}
	}
}

// A claim against a declared direct dependency locates at the manifest line
// declaring it, in either block, and one reached transitively locates at no
// line rather than at a guessed one.
func TestLicenceFindingsLocateADirectDependency(t *testing.T) {
	dir := npmprobe(t)
	findings := LicenceFindings(dir, []licence.Dependency{
		{Package: "lightningcss", Version: "1.33.0", Licence: "MPL-2.0"},
		{Package: "typescript", Version: "7.0.2", Licence: "Apache-2.0"},
		{Package: "@img/sharp-libvips-darwin-arm64", Version: "1.3.1", Licence: "LGPL-3.0-or-later"},
	})
	want := []int{10, 14, 0}
	for i, f := range findings {
		if f.Line != want[i] {
			t.Errorf("%s located at line %d, want %d", f.Rule, f.Line, want[i])
		}
		if f.Path != "package.json" {
			t.Errorf("%s located in %q, want package.json", f.Rule, f.Path)
		}
		if f.Gate != GateLicence {
			t.Errorf("%s reports gate %q, want %q", f.Rule, f.Gate, GateLicence)
		}
	}
}

// The site is the pair, never the text of the line: a package offering two
// non-conforming licences is two claims against one line, and a site read from
// the line would make them one and drop the second.
func TestLicenceFindingsSiteIsThePair(t *testing.T) {
	pairs := []licence.Dependency{
		{Package: "lightningcss", Licence: "MPL-2.0"},
		{Package: "lightningcss", Licence: "LGPL-3.0-or-later"},
	}
	findings := LicenceFindings(npmprobe(t), pairs)
	if findings[0].Site == findings[1].Site {
		t.Fatalf("two licences of one package share the site %q", findings[0].Site)
	}
	for i, f := range findings {
		if f.Site != pairs[i].Key().Site() {
			t.Errorf("site %q, want the pair's own %q", f.Site, pairs[i].Key().Site())
		}
	}
}

// A yarn or pnpm component with no installed tree has no licence source at all,
// and says so. An empty set returned successfully would be a component whose
// every dependency conforms, which is a pass nothing measured.
func TestLicenceSetWithoutAnInstalledTreeIsAnError(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	if _, err := LicenceSet(context.Background(), dir, licence.NewPolicy(permissive)); err == nil {
		t.Fatal("yarnprobe has no node_modules and yarn.lock states no licence, want an error")
	}
}

// Where a tree is already installed, yarn's and pnpm's licences are read out of
// it — the opportunistic read ADR 0042 allows — and a workspace member, which
// both managers link rather than unpack, is skipped the way npm's `link: true`
// entry is.
func TestLicenceSetReadsAnInstalledTree(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	install(t, dir, "lightningcss", `{"name":"lightningcss","version":"1.33.0","license":"MPL-2.0"}`)
	install(t, dir, "@img/sharp-libvips-darwin-arm64", `{"name":"@img/sharp-libvips-darwin-arm64","version":"1.3.1","license":"LGPL-3.0-or-later"}`)
	install(t, dir, "typescript", `{"name":"typescript","version":"7.0.2","license":"Apache-2.0"}`)
	link(t, dir, "@lydite/pr-relay", filepath.Join(dir, "pr-relay"))

	set, err := LicenceSet(context.Background(), dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	for _, want := range []string{"lightningcss", "@img/sharp-libvips-darwin-arm64"} {
		if _, held := pairs[want]; !held {
			t.Errorf("%s is installed under a licence the allow-list does not hold, want it in the set", want)
		}
	}
	if _, held := pairs["typescript"]; held {
		t.Error("typescript is Apache-2.0, which the allow-list holds")
	}
	if _, held := pairs["@lydite/pr-relay"]; held {
		t.Error("@lydite/pr-relay is linked, not installed: it is the workspace's own package")
	}
}

// install writes one package into dir's node_modules, with manifest as its
// package.json.
func install(t *testing.T, dir, name, manifest string) {
	t.Helper()
	pkg := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(pkg, 0o750); err != nil {
		t.Fatalf("installing %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("installing %s: %v", name, err)
	}
}

// link symlinks one workspace member into dir's node_modules, the way yarn and
// pnpm link a package the repository holds the source of.
func link(t *testing.T, dir, name, target string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("linking %s: %v", name, err)
	}
	entry := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(entry), 0o750); err != nil {
		t.Fatalf("linking %s: %v", name, err)
	}
	if err := os.Symlink(target, entry); err != nil {
		t.Fatalf("linking %s: %v", name, err)
	}
}

// A directory naming no single package manager — no lockfile at all, or two of
// them — is an error rather than an empty set, for the reason nodedeps.Manager
// reports false: which manager resolved the tree is not guessable, and a gate
// that could not run never renders as one that passed.
func TestLicenceSetWithoutASingleManagerIsAnError(t *testing.T) {
	bare := t.TempDir()
	_, err := LicenceSet(context.Background(), bare, licence.NewPolicy(permissive))
	if err == nil || !strings.Contains(err.Error(), "no single package manager") {
		t.Fatalf("a directory with no lockfile names no package manager, got %v", err)
	}

	ambiguous := t.TempDir()
	for _, lockfile := range []string{"package-lock.json", "yarn.lock"} {
		if err := os.WriteFile(filepath.Join(ambiguous, lockfile), []byte("{}"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", lockfile, err)
		}
	}
	// The reason has to name the ambiguity rather than whatever the manager it
	// guessed at failed to find: the error reaches an `unmeasured` row, and a
	// row saying node_modules is missing sends a reader to install a tree that
	// would not have been read either.
	_, err = LicenceSet(context.Background(), ambiguous, licence.NewPolicy(permissive))
	if err == nil || !strings.Contains(err.Error(), "no single package manager") {
		t.Fatalf("two lockfiles name two managers, want an error naming that rather than a guess, got %v", err)
	}
}

// A lockfile that cannot be parsed is an error, not an empty set: a component
// whose lockfile lydite cannot read has had no licence measured, and a pass
// there is a gate reporting on nothing.
func TestLicenceSetOnAnUnreadableLockfileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{\"packages\": ["), 0o600); err != nil {
		t.Fatalf("writing package-lock.json: %v", err)
	}
	if _, err := LicenceSet(context.Background(), dir, licence.NewPolicy(permissive)); err == nil {
		t.Fatal("want an error over a lockfile that does not parse")
	}
}

// An unreadable manifest costs every claim its line and no claim its existence.
func TestLicenceFindingsWithoutAManifest(t *testing.T) {
	findings := LicenceFindings(t.TempDir(), []licence.Dependency{{Package: "lightningcss", Licence: "MPL-2.0"}})
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want the claim kept", len(findings))
	}
	if findings[0].Line != 0 {
		t.Errorf("line %d, want 0 where no manifest names the package", findings[0].Line)
	}
}
