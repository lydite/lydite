package typescript

import (
	"context"
	"encoding/json"
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
	dir := npmprobe(t)
	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
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
	dir := npmprobe(t)
	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	if _, held := pairs["@img/sharp-libvips-linux-x64"]; !held {
		t.Errorf("want the nested duplicate named @img/sharp-libvips-linux-x64, got %v", set.Dependencies())
	}
}

// Two lockfile entries for the same package under the same rejected licence,
// at different versions, resolve to the same version on every call — Go
// randomises map iteration order per run, and licence.Set.Add keeps only the
// first dependency it sees under a pair, so an unsorted read would let the
// version a row and a finding name for that pair change between two scans of
// the identical lockfile.
func TestLockfileDependenciesPicksTheVersionDeterministically(t *testing.T) {
	dir := t.TempDir()
	lockfile := `{
  "name": "dupeprobe",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "dupeprobe", "version": "0.0.0"},
    "node_modules/zlib-sync": {"version": "0.6.0", "license": "GPL-3.0-only"},
    "node_modules/wrangler/node_modules/zlib-sync": {"version": "0.6.1", "license": "GPL-3.0-only"}
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte(lockfile), 0o600); err != nil {
		t.Fatalf("writing package-lock.json: %v", err)
	}

	var versions []string
	for range 20 {
		set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy([]string{"MIT"}))
		if err != nil {
			t.Fatalf("LicenceSet: %v", err)
		}
		d, held := rejected(t, set)["zlib-sync"]
		if !held {
			t.Fatal("zlib-sync is GPL-3.0-only, which the allow-list does not hold, want it in the set")
		}
		versions = append(versions, d.Version)
	}
	for i, v := range versions {
		if v != versions[0] {
			t.Fatalf("read %d picked version %q, read 0 picked %q — the version is not deterministic", i, v, versions[0])
		}
	}
	if want := "0.6.1"; versions[0] != want {
		t.Errorf("picked version %q, want %q — the lexicographically first install path (node_modules/wrangler/... sorts before node_modules/zlib-sync, 'w' < 'z')", versions[0], want)
	}
}

// A `licenses` array composes through licence.Expression: each type is one term
// of an OR, sorted, so the pair a reordering produces is the same pair.
func TestLicenceSetComposesALicensesArray(t *testing.T) {
	dir := npmprobe(t)
	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy([]string{"ISC"}))
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
	dir := npmprobe(t)
	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
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
	dir := npmprobe(t)
	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
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
	if _, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive)); err == nil {
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

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
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

// pnpm's default layout symlinks every registry package's node_modules entry
// into its own `.pnpm` store — not only a workspace member's, the way yarn and
// npm link one. A licence source that treated every symlink as local would
// read almost nothing out of a real pnpm install and pass a component nothing
// measured.
func TestLicenceSetReadsAPnpmStoreSymlink(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	pnpmInstall(t, dir, "lightningcss", "1.33.0", `{"name":"lightningcss","version":"1.33.0","license":"MPL-2.0"}`)

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	d, held := rejected(t, set)["lightningcss"]
	if !held {
		t.Fatal("lightningcss is installed via a .pnpm store symlink under MPL-2.0, want it in the set")
	}
	if d.Version != "1.33.0" {
		t.Errorf("lightningcss = %q, want version 1.33.0", d.Version)
	}
}

// pnpmInstall writes one package into dir's node_modules the way pnpm does:
// the real manifest sits in the `.pnpm` store, inside node_modules, and
// node_modules/<name> is a symlink to it — resolving inside node_modules,
// unlike a workspace member's link back into the repository.
func pnpmInstall(t *testing.T, dir, name, version, manifest string) {
	t.Helper()
	store := filepath.Join(dir, "node_modules", ".pnpm", name+"@"+version, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(store, 0o750); err != nil {
		t.Fatalf("installing %s via the pnpm store: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("installing %s via the pnpm store: %v", name, err)
	}
	entry := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(entry), 0o750); err != nil {
		t.Fatalf("linking %s into the pnpm store: %v", name, err)
	}
	if err := os.Symlink(store, entry); err != nil {
		t.Fatalf("linking %s into the pnpm store: %v", name, err)
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

// A component nested under a workspace root declares no lockfile of its own,
// and its licences are read from the root that resolves them. Reading the
// component's own directory finds nothing there and answers `unmeasured` for a
// lockfile sitting one directory up.
func TestLicenceSetReadsTheWorkspaceRootsLockfile(t *testing.T) {
	root := t.TempDir()
	lockfile := `{
  "name": "workspace",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "workspace", "version": "0.0.0"},
    "packages/ui": {"name": "ui", "version": "0.0.0", "dependencies": {"lightningcss": "1.33.0", "typescript": "7.0.2"}},
    "node_modules/ui": {"resolved": "packages/ui", "link": true},
    "node_modules/lightningcss": {"version": "1.33.0", "license": "MPL-2.0"},
    "node_modules/typescript": {"version": "7.0.2", "license": "Apache-2.0"}
  }
}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lockfile), 0o600); err != nil {
		t.Fatalf("writing package-lock.json: %v", err)
	}
	member := filepath.Join(root, "packages", "ui")
	if err := os.MkdirAll(member, 0o750); err != nil {
		t.Fatalf("creating the workspace member: %v", err)
	}
	if err := os.WriteFile(filepath.Join(member, "package.json"), []byte(`{"name":"ui"}`), 0o600); err != nil {
		t.Fatalf("writing the member's package.json: %v", err)
	}

	set, err := LicenceSet(context.Background(), member, root, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	pairs := rejected(t, set)
	d, held := pairs["lightningcss"]
	if !held {
		t.Fatalf("lightningcss is MPL-2.0 in the root lockfile, want it in the set, got %v", set.Dependencies())
	}
	if d.Version != "1.33.0" {
		t.Errorf("lightningcss = %q, want version 1.33.0", d.Version)
	}
	if _, held := pairs["typescript"]; held {
		t.Error("typescript is Apache-2.0, which the allow-list holds")
	}
	if _, held := pairs["ui"]; held {
		t.Error("ui is the workspace's own package, want it out of the set entirely")
	}
}

// npmWorkspace materialises a three-member npm workspace and answers its root.
//
// `ui` and `api` share lightningcss at two versions — api's own copy is nested
// under its directory, the shape npm writes when two members want different
// releases — and api alone depends on zlib-sync, which pulls unlicensed-probe
// in transitively. `app` depends on the member `ui` rather than on a registry
// package.
func npmWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	lockfile := `{
  "name": "workspace",
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "workspace", "version": "0.0.0"},
    "packages/ui": {"name": "ui", "version": "0.0.0", "dependencies": {"lightningcss": "1.33.0"}, "devDependencies": {"typescript": "7.0.2"}},
    "packages/api": {"name": "api", "version": "0.0.0", "dependencies": {"lightningcss": "1.20.0", "zlib-sync": "0.6.0"}},
    "packages/app": {"name": "app", "version": "0.0.0", "dependencies": {"ui": "*"}},
    "node_modules/ui": {"resolved": "packages/ui", "link": true},
    "node_modules/api": {"resolved": "packages/api", "link": true},
    "node_modules/app": {"resolved": "packages/app", "link": true},
    "node_modules/lightningcss": {"version": "1.33.0", "license": "MPL-2.0"},
    "packages/api/node_modules/lightningcss": {"version": "1.20.0", "license": "MPL-2.0"},
    "node_modules/zlib-sync": {"version": "0.6.0", "license": "GPL-3.0-only", "dependencies": {"unlicensed-probe": "1.0.0"}},
    "node_modules/unlicensed-probe": {"version": "1.0.0"},
    "node_modules/typescript": {"version": "7.0.2", "license": "Apache-2.0"}
  }
}`
	if err := os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(lockfile), 0o600); err != nil {
		t.Fatalf("writing package-lock.json: %v", err)
	}
	manifests := map[string]string{
		"ui": `{
  "name": "ui",
  "dependencies": {
    "lightningcss": "1.33.0"
  },
  "devDependencies": {
    "typescript": "7.0.2"
  }
}`,
		"api": `{
  "name": "api",
  "dependencies": {
    "lightningcss": "1.20.0",
    "zlib-sync": "0.6.0"
  }
}`,
		"app": `{"name":"app","dependencies":{"ui":"*"}}`,
	}
	for name, manifest := range manifests {
		dir := filepath.Join(root, "packages", name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("creating the workspace member %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o600); err != nil {
			t.Fatalf("writing %s's package.json: %v", name, err)
		}
	}
	return root
}

// One root lockfile resolves every member's dependencies together, and each
// member's set holds the ones it resolves rather than the workspace's. A
// sibling's dependency in this set would locate against a manifest that never
// declares it, and would be claimed once per member of the workspace.
func TestLicenceSetScopesANestedMemberToItsOwnDependencies(t *testing.T) {
	root := npmWorkspace(t)
	policy := licence.NewPolicy(permissive)

	ui, err := LicenceSet(context.Background(), filepath.Join(root, "packages", "ui"), root, policy)
	if err != nil {
		t.Fatalf("LicenceSet(ui): %v", err)
	}
	api, err := LicenceSet(context.Background(), filepath.Join(root, "packages", "api"), root, policy)
	if err != nil {
		t.Fatalf("LicenceSet(api): %v", err)
	}

	uiPairs, apiPairs := rejected(t, ui), rejected(t, api)
	// The shared dependency resolves to the copy each member's own directory
	// reaches first, so one lockfile answers one package at two versions.
	if d := uiPairs["lightningcss"]; d.Version != "1.33.0" {
		t.Errorf("ui's lightningcss = %q, want 1.33.0", d.Version)
	}
	if d := apiPairs["lightningcss"]; d.Version != "1.20.0" {
		t.Errorf("api's lightningcss = %q, want 1.20.0 from packages/api/node_modules", d.Version)
	}
	if _, held := uiPairs["zlib-sync"]; held {
		t.Errorf("zlib-sync is api's dependency alone, want it out of ui's set, got %v", ui.Dependencies())
	}
	if _, held := uiPairs["unlicensed-probe"]; held {
		t.Errorf("unlicensed-probe is reached through api's zlib-sync alone, want it out of ui's set, got %v", ui.Dependencies())
	}
	if _, held := apiPairs["zlib-sync"]; !held {
		t.Errorf("zlib-sync is GPL-3.0-only and api depends on it, want it in api's set, got %v", api.Dependencies())
	}
	if _, held := apiPairs["unlicensed-probe"]; !held {
		t.Errorf("unlicensed-probe is reached transitively through zlib-sync, want it in api's set, got %v", api.Dependencies())
	}
	if _, held := apiPairs["typescript"]; held {
		t.Error("typescript is Apache-2.0, which the allow-list holds")
	}
}

// A member depending on another member of the same workspace requires what
// that member requires: the `link: true` entry resolution lands on is a
// pointer at the sibling's own importer entry, and the walk follows it.
func TestLicenceSetFollowsALinkToAnotherMember(t *testing.T) {
	root := npmWorkspace(t)
	set, err := LicenceSet(context.Background(), filepath.Join(root, "packages", "app"), root, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet(app): %v", err)
	}
	pairs := rejected(t, set)
	if d, held := pairs["lightningcss"]; !held || d.Version != "1.33.0" {
		t.Errorf("app depends on the member ui, which depends on lightningcss 1.33.0, got %v", set.Dependencies())
	}
	for _, sibling := range []string{"ui", "app"} {
		if _, held := pairs[sibling]; held {
			t.Errorf("%q is the workspace's own package, want it out of the set entirely", sibling)
		}
	}
	if _, held := pairs["zlib-sync"]; held {
		t.Errorf("zlib-sync is reachable from api alone, want it out of app's set, got %v", set.Dependencies())
	}
}

// A claim against a nested member's own declared dependency locates at the
// line of that member's own manifest, in either block. A set scoped to the
// whole workspace would carry siblings' dependencies, which that manifest
// names nowhere, and every one of them would report Line: 0 as though it had
// been reached transitively.
func TestLicenceFindingsLocateANestedMembersOwnDependency(t *testing.T) {
	root := npmWorkspace(t)
	member := filepath.Join(root, "packages", "ui")
	set, err := LicenceSet(context.Background(), member, root, licence.NewPolicy([]string{"ISC"}))
	if err != nil {
		t.Fatalf("LicenceSet(ui): %v", err)
	}
	lines := map[string]int{}
	for _, f := range LicenceFindings(member, set.Dependencies()) {
		lines[f.Message] = f.Line
	}
	for message, line := range lines {
		if line == 0 {
			t.Errorf("%q located at line 0, want the member's own manifest line declaring it", message)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("got %d claims, want one for each of ui's own two dependencies: %v", len(lines), lines)
	}
}

// A component the root's lockfile names no importer entry for — a directory
// the workspace never declared as a member, or one added since the lockfile
// was written — has had nothing resolved for it, and says so. An empty
// closure returned successfully is every dependency conforming to a policy
// that read none of them.
func TestLicenceSetOnAMemberTheLockfileDoesNotNameIsAnError(t *testing.T) {
	root := npmWorkspace(t)
	undeclared := filepath.Join(root, "packages", "docs")
	if err := os.MkdirAll(undeclared, 0o750); err != nil {
		t.Fatalf("creating the undeclared member: %v", err)
	}
	_, err := LicenceSet(context.Background(), undeclared, root, licence.NewPolicy(permissive))
	if err == nil || !strings.Contains(err.Error(), "packages/docs") {
		t.Fatalf("the lockfile names no entry for packages/docs, want an error naming it, got %v", err)
	}
}

// yarn's and pnpm's source is an installed tree, which records no importer and
// no edge to scope by, so a nested member under either is unmeasured rather
// than a set carrying every sibling's dependencies.
func TestLicenceSetOnANestedYarnMemberIsAnError(t *testing.T) {
	root := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	install(t, root, "lightningcss", `{"name":"lightningcss","version":"1.33.0","license":"MPL-2.0"}`)
	member := filepath.Join(root, "packages", "ui")
	if err := os.MkdirAll(member, 0o750); err != nil {
		t.Fatalf("creating the workspace member: %v", err)
	}

	_, err := LicenceSet(context.Background(), member, root, licence.NewPolicy(permissive))
	if err == nil || !strings.Contains(err.Error(), "per-member resolution") {
		t.Fatalf("yarn records no per-member resolution, want an error naming that rather than the root's whole tree, got %v", err)
	}
}

// A component under no lockfile at all, its own or any ancestor's up to the
// scan root, is the error that renders `unmeasured` — never the empty set that
// reads as every dependency conforming to a policy nothing was measured
// against.
func TestLicenceSetUnderNoWorkspaceRootIsAnError(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "packages", "ui")
	if err := os.MkdirAll(member, 0o750); err != nil {
		t.Fatalf("creating the workspace member: %v", err)
	}
	if err := os.WriteFile(filepath.Join(member, "package.json"), []byte(`{"name":"ui"}`), 0o600); err != nil {
		t.Fatalf("writing the member's package.json: %v", err)
	}

	_, err := LicenceSet(context.Background(), member, root, licence.NewPolicy(permissive))
	if err == nil || !strings.Contains(err.Error(), "no single package manager") {
		t.Fatalf("no lockfile between the component and the scan root names a manager, got %v", err)
	}
}

// A directory naming no single package manager — no lockfile at all, or two of
// them — is an error rather than an empty set, for the reason nodedeps.Manager
// reports false: which manager resolved the tree is not guessable, and a gate
// that could not run never renders as one that passed.
func TestLicenceSetWithoutASingleManagerIsAnError(t *testing.T) {
	bare := t.TempDir()
	_, err := LicenceSet(context.Background(), bare, bare, licence.NewPolicy(permissive))
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
	_, err = LicenceSet(context.Background(), ambiguous, ambiguous, licence.NewPolicy(permissive))
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
	if _, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive)); err == nil {
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

// A path naming no installed package at all answers the empty name, not just
// false — a caller that only checked the bool and typed the empty string in
// by hand would not notice the name itself drifting.
func TestPackageNameOnAPathWithNoNodeModulesIsEmpty(t *testing.T) {
	name, ok := packageName("pr-relay")
	if ok || name != "" {
		t.Errorf("packageName(%q) = (%q, %v), want (\"\", false)", "pr-relay", name, ok)
	}
}

// A dotted entry of node_modules is the manager's own bookkeeping — `.bin`,
// `.pnpm`, `.package-lock.json` — and names no package, whatever it is on
// disk. Reading it as one would install a dependency named ".bin".
func TestInstalledDependenciesSkipsDottedEntries(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", ".bin"), 0o750); err != nil {
		t.Fatalf("creating .bin: %v", err)
	}
	install(t, dir, "typescript", `{"name":"typescript","version":"7.0.2","license":"Apache-2.0"}`)

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	for _, d := range set.Dependencies() {
		if d.Package == ".bin" {
			t.Fatalf(".bin is the manager's own bookkeeping, want it excluded, got %v", set.Dependencies())
		}
	}
}

// Two claims that land on the identical pair are numbered apart, the way
// finding.Number numbers any other duplicate site — otherwise they would
// collapse into indistinguishable review threads on the same line.
func TestLicenceFindingsNumbersADuplicatePair(t *testing.T) {
	pairs := []licence.Dependency{
		{Package: "lightningcss", Licence: "MPL-2.0"},
		{Package: "lightningcss", Licence: "MPL-2.0"},
	}
	findings := LicenceFindings(t.TempDir(), pairs)
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want both claims kept", len(findings))
	}
	if findings[0].Ordinal == findings[1].Ordinal {
		t.Errorf("both claims carry ordinal %d, want the second numbered past the first", findings[0].Ordinal)
	}
}

// jsonKey names the key a line opens with, and empty for anything else: a
// line that is not a quoted key at all, one whose quote never closes, and a
// quoted string with nothing naming it a key (no trailing colon — the shape
// an array entry like a `keywords` list has).
func TestJSONKey(t *testing.T) {
	for _, tc := range []struct {
		line, want string
	}{
		{`  "dependencies": {`, "dependencies"},
		{`    "typescript": "7.0.2",`, "typescript"},
		{`  },`, ""},
		{`    "typescript",`, ""},
		{`    "unterminated`, ""},
		{`    "": 1,`, ""},
		{``, ""},
	} {
		if got := jsonKey(tc.line); got != tc.want {
			t.Errorf("jsonKey(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// The message names the version when the dependency states one, and omits it
// — rather than a trailing space — when it does not.
func TestLicenceMessageNamesTheVersionWhenThereIsOne(t *testing.T) {
	withVersion := licenceMessage(licence.Dependency{Package: "lightningcss", Version: "1.33.0", Licence: "MPL-2.0"})
	if want := "lightningcss 1.33.0 is MPL-2.0, which the licence policy does not allow"; withVersion != want {
		t.Errorf("licenceMessage = %q, want %q", withVersion, want)
	}
	withoutVersion := licenceMessage(licence.Dependency{Package: "lightningcss", Licence: "MPL-2.0"})
	if want := "lightningcss is MPL-2.0, which the licence policy does not allow"; withoutVersion != want {
		t.Errorf("licenceMessage = %q, want %q", withoutVersion, want)
	}
}

// A manifest value that is neither a bare string nor a `{type}` object — a
// number, a bool, null — states no licence lydite can read.
func TestLicenceTextOnAnUnrecognisedShapeIsEmpty(t *testing.T) {
	if got := licenceText(json.RawMessage("123")); got != "" {
		t.Errorf("licenceText(123) = %q, want empty", got)
	}
}

// A lockfile that cannot even be opened is the same kind of failure as one
// that cannot be parsed: nothing was measured, and LicenceSet's caller reads
// this as unmeasured rather than an empty, passing set.
func TestLockfileDependenciesOnAMissingLockfileIsAnError(t *testing.T) {
	if _, err := lockfileDependencies(t.TempDir(), ""); err == nil {
		t.Fatal("want an error over a directory with no package-lock.json")
	}
}

// An install path that ends exactly at `node_modules/`, naming nothing past
// it, resolves to no package — the same answer as a path with no
// `node_modules/` segment at all.
func TestPackageNameOnNodeModulesWithNothingAfterIsEmpty(t *testing.T) {
	name, ok := packageName("node_modules/")
	if ok || name != "" {
		t.Errorf("packageName(%q) = (%q, %v), want (\"\", false)", "node_modules/", name, ok)
	}
}

// A scope directory that cannot be read — broken, or permission-denied — is
// skipped rather than aborting the whole read: the packages under every other
// scope are still worth reporting.
func TestInstalledDependenciesSkipsAnUnreadableScope(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	scopeDir := filepath.Join(dir, "node_modules", "@broken")
	if err := os.MkdirAll(filepath.Dir(scopeDir), 0o750); err != nil {
		t.Fatalf("preparing node_modules: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), scopeDir); err != nil {
		t.Fatalf("linking @broken: %v", err)
	}
	install(t, dir, "typescript", `{"name":"typescript","version":"7.0.2","license":"Apache-2.0"}`)

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	for _, d := range set.Dependencies() {
		if strings.HasPrefix(d.Package, "@broken") {
			t.Errorf("an unreadable scope produced a dependency: %v", d)
		}
	}
}

// A node_modules entry that is a broken symlink resolves to no target, so
// workspaceLocal cannot tell where it points and answers false — read like
// any other installed package, which then can't read its manifest either and
// reports Unknown rather than dropping the entry.
func TestInstalledDependenciesReadsABrokenSymlinkAsUnknown(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	entry := filepath.Join(dir, "node_modules", "broken-pkg")
	if err := os.MkdirAll(filepath.Dir(entry), 0o750); err != nil {
		t.Fatalf("preparing node_modules: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), entry); err != nil {
		t.Fatalf("linking broken-pkg: %v", err)
	}

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	d, held := rejected(t, set)["broken-pkg"]
	if !held {
		t.Fatal("broken-pkg is a broken symlink, want it read as an unknown-licence dependency")
	}
	if d.Licence != licence.Unknown {
		t.Errorf("broken-pkg = %q, want %q", d.Licence, licence.Unknown)
	}
}

// A package whose manifest exists but does not parse as JSON still yields the
// dependency, under Unknown — the same treatment as one with no manifest at
// all, because a dependency dropped over a broken manifest is one the gate
// silently allowed.
func TestInstalledDependencyOnAMalformedManifestIsUnknown(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("testdata", "yarnprobe"))
	install(t, dir, "garbled", `{"license": `)

	set, err := LicenceSet(context.Background(), dir, dir, licence.NewPolicy(permissive))
	if err != nil {
		t.Fatalf("LicenceSet: %v", err)
	}
	d, held := rejected(t, set)["garbled"]
	if !held {
		t.Fatal("garbled's manifest does not parse, want it read as an unknown-licence dependency")
	}
	if d.Licence != licence.Unknown {
		t.Errorf("garbled = %q, want %q", d.Licence, licence.Unknown)
	}
}
