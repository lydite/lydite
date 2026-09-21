package typescript

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/nodedeps"
)

// GateLicence is the licence gate as this package reports it. It is
// licence.Gate rather than a second literal, so the row a TypeScript component
// carries and the row a Go component carries can never drift apart.
const GateLicence = licence.Gate

// packageJSONFile is the manifest a component declares its direct dependencies
// in, and the file a licence claim against one of them is located in.
const packageJSONFile = "package.json"

// npmLockFile is the one lockfile that states a dependency's licence. Neither
// yarn.lock nor pnpm-lock.yaml carries a licence in any format, which is why
// those two managers read an installed tree or report nothing at all.
const npmLockFile = "package-lock.json"

// nodeModulesDir is where an install puts what it resolved.
const nodeModulesDir = "node_modules"

// npmLockfile is `package-lock.json` as far as licences go: schema version 3's
// flat `packages` map, keyed by the path an entry was installed at.
type npmLockfile struct {
	Packages map[string]npmPackage `json:"packages"`
}

// npmPackage is one entry of that map.
//
// License and Licenses are raw, because npm's manifests spell a licence three
// ways — a string, an old-style array of `{type, url}` objects, and an even
// older single `{type, url}` object — and a typed field for one of them makes
// the whole lockfile fail to decode over a single dependency using another.
type npmPackage struct {
	Version  string            `json:"version"`
	License  json.RawMessage   `json:"license"`
	Licenses []json.RawMessage `json:"licenses"`
	// Link marks a workspace's own local package, whose entry is a pointer at
	// a directory in the repository rather than at a downloaded tarball. It is
	// skipped the way internal/golang skips the main module: a repository's own
	// licence is not a dependency's, and is not this gate's to judge.
	Link bool `json:"link"`
	// Resolved is the tarball URL or git reference npm fetched this entry
	// from — the origin that names the pin, alongside its name and version.
	// On a `link: true` entry it is instead the workspace member's own
	// directory, which is the `packages` key holding that member's own edges.
	Resolved string `json:"resolved"`
	// The edges resolution walks from this entry. Peers are among them because
	// npm installs a peer dependency into the tree like any other, and a
	// package whose peer resolves is a package that requires it at runtime.
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
}

// requires is every package name an entry depends on, sorted, across all four
// kinds of edge. An edge naming a package the lockfile resolved nowhere — an
// optional dependency npm skipped on this platform, a peer it did not install
// — resolves to nothing and drops out of the walk.
func (p npmPackage) requires() []string {
	names := make([]string, 0, len(p.Dependencies)+len(p.DevDependencies)+len(p.OptionalDependencies)+len(p.PeerDependencies))
	for _, block := range []map[string]string{p.Dependencies, p.DevDependencies, p.OptionalDependencies, p.PeerDependencies} {
		for name := range block {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// licenceOf is the SPDX expression an entry states, or Unknown when it states
// none.
//
// The `licenses` array composes through licence.Expression the way Go composes
// a module offering more than one licence file: each type is one term of an OR,
// so a package satisfying any one allowed identifier conforms. A `license`
// string passes through as written, including free text no SPDX parser accepts
// — licence.Policy contains that parse and answers Unclassifiable, so the pair
// reaches the set as Unknown rather than dropping out of it.
func licenceOf(p npmPackage) string {
	if id := licenceText(p.License); id != "" {
		return id
	}
	ids := make([]string, 0, len(p.Licenses))
	for _, raw := range p.Licenses {
		if id := licenceText(raw); id != "" {
			ids = append(ids, id)
		}
	}
	return licence.Expression(ids)
}

// licenceText is the licence a raw manifest value names, in either spelling: a
// bare string, or an object whose `type` holds the identifier.
func licenceText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var obj struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Type)
	}
	return ""
}

// lockfileDependencies is every dependency `package-lock.json` resolved, with
// the licence it states.
//
// Only entries installed under a `node_modules/` path are dependencies. The
// root entry is keyed by the empty string and a workspace member by its own
// directory — `pr-relay`, `libs/github-app` — and both are the repository's own
// code, which states no licence field and must not be reported as a dependency
// that failed to state one. The `link: true` alias each workspace member also
// carries under `node_modules/` is skipped for the same reason.
//
// A package is named by the segment after the last `node_modules/`, because a
// duplicate is nested — `node_modules/wrangler/node_modules/esbuild` is esbuild.
//
// The install paths are sorted before the slice is built, because
// licence.Set.Add keeps only the first dependency it sees under a pair, and a
// plain range over lock.Packages would hand it whichever nested duplicate Go's
// randomised map order produced first — the version a row and a finding name
// for a rejected pair would then change between two scans of the identical
// lockfile, even though the verdict itself would not.
// A member names the workspace member the set is scoped to, as its directory
// relative to dir in slash form — `packages/ui`, the key npm writes that
// member's importer entry under. The empty member is the root itself, whose
// dependencies are the whole lockfile's: a component that owns the lockfile
// owns every entry resolved under it.
func lockfileDependencies(dir, member string) ([]licence.Dependency, error) {
	data, err := os.ReadFile(filepath.Join(dir, npmLockFile)) // #nosec G304 -- dir is the workspace root resolved for a declared component
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", npmLockFile, err)
	}
	var lock npmLockfile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parsing %s in %s: %w", npmLockFile, dir, err)
	}
	paths := make([]string, 0, len(lock.Packages))
	for path := range lock.Packages {
		paths = append(paths, path)
	}
	if member != "" {
		reachable, err := memberClosure(lock.Packages, member, dir)
		if err != nil {
			return nil, err
		}
		paths = slices.DeleteFunc(paths, func(path string) bool { return !reachable[path] })
	}
	sort.Strings(paths)
	out := make([]licence.Dependency, 0, len(paths))
	for _, path := range paths {
		p := lock.Packages[path]
		name, ok := packageName(path)
		if !ok || p.Link {
			continue
		}
		out = append(out, licence.Dependency{Package: name, Version: p.Version, Licence: licenceOf(p)})
	}
	return out, nil
}

// memberPath is the workspace member dir names, relative to the root that
// resolves it, in the slash form npm keys an importer entry by. The root
// itself is the empty member.
func memberPath(root, dir string) (string, error) {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return "", fmt.Errorf("locating %s under the workspace root %s: %w", dir, root, err)
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// memberClosure is every `packages` key one workspace member resolves to,
// walked from that member's own importer entry.
//
// A root lockfile resolves every member's dependencies into one tree, so the
// entries under it are the workspace's and not any one member's. Reporting the
// whole tree for a nested member puts a sibling's dependency in this
// component's set, where it locates against a manifest that never declares it
// and reads as transitive, and files the same claim once per member of the
// workspace. The lockfile already holds the structure that separates them: an
// importer entry names the member's own edges, and each `node_modules/` entry
// names its own, so the member's set is the closure over them.
//
// Resolution follows node's, because that is what decides which copy of a
// package an entry actually loads: a name required from the package installed
// at P resolves to `P/node_modules/<name>` if the lockfile holds one, and
// otherwise to the nearest ancestor directory's `node_modules/<name>`. A
// `link: true` entry resolves on to the member it points at, whose own
// dependencies a package depending on that member does require.
//
// A member the lockfile names no importer entry for is an error rather than an
// empty closure: nothing was resolved for it, and a set nothing was resolved
// into is every dependency conforming to a policy that read none of them.
func memberClosure(packages map[string]npmPackage, member, dir string) (map[string]bool, error) {
	if _, ok := packages[member]; !ok {
		return nil, fmt.Errorf("%s in %s resolves no dependencies for the workspace member %s: it names no entry for that path, so this component's dependencies are not among the ones it resolved", npmLockFile, dir, member)
	}
	reachable := map[string]bool{}
	queue := []string{member}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, name := range packages[from].requires() {
			target, ok := resolveEntry(packages, from, name)
			if !ok || reachable[target] {
				continue
			}
			reachable[target] = true
			queue = append(queue, target)
			if linked := packages[target]; linked.Link && linked.Resolved != "" {
				if _, ok := packages[linked.Resolved]; ok && !reachable[linked.Resolved] {
					reachable[linked.Resolved] = true
					queue = append(queue, linked.Resolved)
				}
			}
		}
	}
	return reachable, nil
}

// resolveEntry is the `packages` key a name required from the package
// installed at from resolves to, and false when the lockfile resolved that
// name nowhere reachable from there.
//
// The search climbs from's own directory and then each ancestor, skipping a
// `node_modules` directory itself — node looks in `<dir>/node_modules`, never
// in `node_modules/node_modules`.
func resolveEntry(packages map[string]npmPackage, from, name string) (string, bool) {
	for dir := from; ; {
		if dir != nodeModulesDir && !strings.HasSuffix(dir, "/"+nodeModulesDir) {
			candidate := strings.TrimPrefix(dir+"/", "/") + nodeModulesDir + "/" + name
			if _, ok := packages[candidate]; ok {
				return candidate, true
			}
		}
		if dir == "" {
			return "", false
		}
		if i := strings.LastIndex(dir, "/"); i >= 0 {
			dir = dir[:i]
		} else {
			dir = ""
		}
	}
}

// LockDependencies is every package a `package-lock.json`'s content resolved,
// mapped to the versions resolved for it, and the tarball or git origin each
// (name, version) pin was fetched from.
//
// It skips exactly what lockfileDependencies skips, and for the same reason:
// the root entry and a workspace member are the repository's own code rather
// than a dependency, and a package the repository itself contains is not one a
// change can be said to have added. The versions are a list because a nested
// duplicate resolves one name at two versions. The origin travels alongside
// rather than inside the version, because a pin keeping its name and version
// while `resolved` points somewhere else is a different install of the same
// label, not the same dependency it was.
//
// The base side of a comparison is `git show`n rather than checked out, so this
// takes bytes and never a directory.
func LockDependencies(content []byte) (map[string][]string, map[[2]string]string, error) {
	var lock npmLockfile
	if err := json.Unmarshal(content, &lock); err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", npmLockFile, err)
	}
	out := map[string][]string{}
	origins := map[[2]string]string{}
	for path, p := range lock.Packages {
		name, ok := packageName(path)
		if !ok || p.Link {
			continue
		}
		if !slices.Contains(out[name], p.Version) {
			out[name] = append(out[name], p.Version)
		}
		origins[[2]string{name, p.Version}] = p.Resolved
	}
	return out, origins, nil
}

// packageName is the package an entry's install path names, and false when the
// path names no installed package at all.
func packageName(path string) (string, bool) {
	i := strings.LastIndex(path, nodeModulesDir+"/")
	if i < 0 {
		return "", false
	}
	name := path[i+len(nodeModulesDir)+1:]
	if name == "" {
		return "", false
	}
	return name, true
}

// installedDependencies is every package present in dir's node_modules, with
// the licence its own manifest states.
//
// This is yarn's and pnpm's only source: neither lockfile format states a
// licence, and scan runs no install to produce one. A tree an earlier step
// already installed is read opportunistically, and a tree that is not there is
// the caller's error rather than an empty answer.
//
// A symlink is not, by itself, a local workspace package: pnpm's default
// layout links every registry package's node_modules entry into its own
// `.pnpm` store, so treating every symlink as local would read almost nothing
// out of a real pnpm install and pass a component nothing measured — the
// fail-open ADR 0038 forbids. workspaceLocal tells the two apart by where the
// link resolves to.
func installedDependencies(ctx context.Context, dir string) ([]licence.Dependency, error) {
	root := filepath.Join(dir, nodeModulesDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	// Resolved once, and compared against below rather than root itself: a
	// test's own TempDir, and some real systems, reach node_modules through a
	// symlinked ancestor — /var on darwin is one — and resolving only the
	// entry against an unresolved root would read every entry as escaping it.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var out []licence.Dependency
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !installedPackage(e) || workspaceLocal(resolvedRoot, filepath.Join(root, e.Name())) {
			continue
		}
		// A scope is a plain directory holding the packages under it, so its
		// members are read one level down and named `@scope/package`.
		if strings.HasPrefix(e.Name(), "@") {
			scoped, err := os.ReadDir(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			for _, s := range scoped {
				path := filepath.Join(root, e.Name(), s.Name())
				if !installedPackage(s) || workspaceLocal(resolvedRoot, path) {
					continue
				}
				out = append(out, installedDependency(root, e.Name()+"/"+s.Name()))
			}
			continue
		}
		out = append(out, installedDependency(root, e.Name()))
	}
	return out, nil
}

// installedPackage reports whether an entry of a node_modules directory is a
// package to consider at all: a real directory or a symlink to one, and never
// a dotted name — `.bin`, `.package-lock.json`, `.pnpm` — which is the
// manager's own bookkeeping and names no package. Whether a symlink among
// these is a workspace member rather than an installed one is workspaceLocal's
// question, not this one.
func installedPackage(e fs.DirEntry) bool {
	if strings.HasPrefix(e.Name(), ".") {
		return false
	}
	return e.IsDir() || e.Type()&fs.ModeSymlink != 0
}

// workspaceLocal reports whether path — a node_modules entry, symlinked or
// not — resolves outside resolvedRoot (node_modules, itself already resolved)
// and back into the repository's own tree, which is how both yarn and pnpm
// link a workspace member, the way npm writes `link: true` for the same case.
// A registry package pnpm symlinks resolves inside node_modules' own `.pnpm`
// store, which is not local and is read like any other installed package.
//
// A link that cannot be resolved at all — broken, or not a symlink — answers
// false rather than true: installedDependency then reads it as it stands, and
// an unreadable manifest already yields Unknown rather than being dropped.
func workspaceLocal(resolvedRoot, path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// installedDependency reads one installed package's manifest. A manifest that
// cannot be read or parsed still yields the dependency, under Unknown: a
// dependency dropped because its licence could not be read is a dependency the
// gate silently allowed.
func installedDependency(root, name string) licence.Dependency {
	d := licence.Dependency{Package: name, Licence: licence.Unknown}
	// #nosec G304 -- root is a resolved workspace root's node_modules and name is a directory entry read out of it
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name), packageJSONFile))
	if err != nil {
		return d
	}
	var manifest npmPackage
	if err := json.Unmarshal(data, &manifest); err != nil {
		return d
	}
	d.Version = manifest.Version
	d.Licence = licenceOf(manifest)
	return d
}

// LicenceSet is the component's non-conforming dependencies under policy.
//
// It is only ever the non-conforming ones and never a full inventory, which is
// what makes recomputing it at the merge-base affordable.
//
// The lockfile and the installed tree are read at the workspace root
// nodedeps.WorkspaceRoot resolves for dir, bounded by scanRoot, because a
// member of a workspace declares neither: both sit at the root that resolves
// the whole workspace, and reading dir alone answers a nested component
// `unmeasured` for a lockfile that exists one directory up. A component whose
// own directory holds the lockfile resolves to itself, and a component under no
// resolvable root is the error the row reports as `unmeasured` — never an empty
// set, which is every dependency conforming to a policy nothing was read
// against.
//
// What is read at that root is scoped to the one component. npm's lockfile
// holds the structure that separates a member's dependencies from its
// siblings' — see memberClosure — so a nested npm component's set is its own
// closure. yarn and pnpm are read out of an installed `node_modules`, which
// records no importer and no edge, and scoping one there needs either an
// install or a structure neither manager writes: a nested member under those
// two is the error the row reports as `unmeasured`, rather than a set carrying
// every sibling's dependencies. A component whose own directory holds the
// lockfile is the workspace root and is read whole, under all three.
//
// No install is run, for either manager and on either side of the comparison.
// npm's lockfile states every dependency's licence outright, and under the
// frozen install lydite's runner performs it states what an install is required
// to produce; yarn and pnpm state none, and an error here is the row saying so.
// See docs/adr/0042.
func LicenceSet(ctx context.Context, dir, scanRoot string, policy licence.Policy) (licence.Set, error) {
	root, ok := nodedeps.WorkspaceRoot(dir, scanRoot)
	if !ok {
		return licence.Set{}, fmt.Errorf("no single package manager for %s: exactly one lockfile of %s names one, in the component's own directory or in a workspace root above it", dir, strings.Join(nodedeps.Managers(), ", "))
	}
	// WorkspaceRoot ends its walk only at a directory naming exactly one
	// manager, so this lookup cannot disagree with it.
	manager, _ := nodedeps.Manager(root)
	member, err := memberPath(root, dir)
	if err != nil {
		return licence.Set{}, err
	}
	if manager == "npm" {
		deps, err := lockfileDependencies(root, member)
		if err != nil {
			return licence.Set{}, err
		}
		return policy.Reject(deps), nil
	}
	if member != "" {
		return licence.Set{}, fmt.Errorf("%s is the member %s of the workspace at %s, and %s records no per-member resolution to scope %s by: reading the root's %s whole would report every sibling's dependencies as this component's", dir, member, root, manager, nodeModulesDir, nodeModulesDir)
	}
	deps, err := installedDependencies(ctx, root)
	if err != nil {
		return licence.Set{}, fmt.Errorf("%s states no dependency licence and %s in %s is unreadable, and this scan runs no install to produce one: %w", manager, nodeModulesDir, root, err)
	}
	return policy.Reject(deps), nil
}

// LicenceFindings is each introduced pair as a claim on the manifest line
// naming its package.
//
// The dependencies entry, because the one edit that clears the claim is to the
// dependency — dropping it, or moving to a release under a licence the policy
// allows. A package reached only transitively is named by no line in this
// manifest and reports zero rather than a guess: a guessed line is a review
// thread on code that has nothing to do with the claim.
//
// The site is the pair rather than the text of that line: a package offering
// two non-conforming licences is two claims against one line, and a site read
// from the line would make them one and drop the second.
func LicenceFindings(dir string, pairs []licence.Dependency) []finding.Finding {
	manifest := readPackageJSON(dir)
	out := make([]finding.Finding, 0, len(pairs))
	for _, d := range pairs {
		out = append(out, finding.Finding{
			Gate:     GateLicence,
			Path:     packageJSONFile,
			Line:     manifest.Line(d.Package),
			Rule:     d.Licence,
			Severity: "licence",
			Message:  licenceMessage(d),
			Site:     d.Key().Site(),
		})
	}
	finding.Number(out)
	return out
}

// packageJSON is a component's manifest read for the lines its direct
// dependencies are named on.
//
// The lines are scanned out of the raw text rather than read off a decoded
// document, because encoding/json keeps no positions and a claim's line has to
// be the one an author can edit.
type packageJSON struct {
	// dependencies maps a package name to its one-based line in the manifest.
	dependencies map[string]int
}

// readPackageJSON reads dir's manifest, answering an empty one when it cannot.
//
// An unreadable manifest costs every claim its line and no claim its existence:
// the gate has already found what it found, and a claim with no line belongs in
// the standing comment rather than nowhere.
func readPackageJSON(dir string) *packageJSON {
	m := &packageJSON{dependencies: map[string]int{}}
	data, err := os.ReadFile(filepath.Join(dir, packageJSONFile)) // #nosec G304 -- dir is a declared component's directory
	if err != nil {
		return m
	}
	// Whether the line being read is inside a dependencies block, and how deep
	// the object nesting is outside one. Depth is tracked so that a
	// `dependencies` key nested inside some other object — an override block,
	// one workspace member described inline — cannot be read as the manifest's
	// own, which would file a package under a line whose edit changes nothing
	// about it.
	inBlock := false
	depth := 0
	for n, raw := range strings.Split(string(data), "\n") {
		key := jsonKey(raw)
		switch {
		case inBlock:
			if key != "" {
				m.setDependency(key, n+1)
			}
			if strings.HasPrefix(strings.TrimSpace(raw), "}") {
				inBlock = false
			}
		case depth == 1 && (key == "dependencies" || key == "devDependencies"):
			// An entry on the same line as the brace that closes its block is
			// named by no line of its own, so the block is not entered and
			// every package in it keeps zero.
			inBlock = strings.Contains(raw, "{") && !strings.Contains(raw, "}")
		}
		depth += strings.Count(raw, "{") - strings.Count(raw, "}")
	}
	return m
}

// jsonKey is the key a line opens with, or empty when it opens with anything
// else.
func jsonKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, `"`) {
		return ""
	}
	end := strings.Index(trimmed[1:], `"`)
	if end == -1 {
		return "" // unterminated: no closing quote at all
	}
	key := trimmed[1 : end+1]
	if key == "" {
		return "" // the closing quote follows the opening one directly
	}
	if !strings.HasPrefix(strings.TrimSpace(trimmed[end+2:]), ":") {
		return ""
	}
	return key
}

// setDependency records a package's line, keeping the first of several. A
// manifest naming one package in both `dependencies` and `devDependencies`
// keeps the line an install resolves it from.
func (m *packageJSON) setDependency(name string, line int) {
	if _, seen := m.dependencies[name]; !seen {
		m.dependencies[name] = line
	}
}

// Line is the manifest line naming pkg, or zero when the manifest does not name
// it — a package reached only transitively, which a manifest legitimately never
// mentions.
func (m *packageJSON) Line(pkg string) int { return m.dependencies[pkg] }

// licenceMessage is the one-line claim: which dependency, at which version,
// under which licence.
func licenceMessage(d licence.Dependency) string {
	msg := d.Package
	if d.Version != "" {
		msg += " " + d.Version
	}
	return msg + " is " + d.Licence + ", which the licence policy does not allow"
}
