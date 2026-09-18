package typescript

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
func lockfileDependencies(dir string) ([]licence.Dependency, error) {
	data, err := os.ReadFile(filepath.Join(dir, npmLockFile)) // #nosec G304 -- dir is a declared component's directory
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", npmLockFile, err)
	}
	var lock npmLockfile
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("parsing %s in %s: %w", npmLockFile, dir, err)
	}
	out := make([]licence.Dependency, 0, len(lock.Packages))
	for path, p := range lock.Packages {
		name, ok := packageName(path)
		if !ok || p.Link {
			continue
		}
		out = append(out, licence.Dependency{Package: name, Version: p.Version, Licence: licenceOf(p)})
	}
	return out, nil
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
	// #nosec G304 -- root is a declared component's node_modules and name is a directory entry read out of it
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
// No install is run, for either manager and on either side of the comparison.
// npm's lockfile states every dependency's licence outright, and under the
// frozen install lydite's runner performs it states what an install is required
// to produce; yarn and pnpm state none, and an error here is the row saying so.
// See docs/adr/0042.
func LicenceSet(ctx context.Context, dir string, policy licence.Policy) (licence.Set, error) {
	manager, ok := nodedeps.Manager(dir)
	if !ok {
		return licence.Set{}, fmt.Errorf("no single package manager in %s: exactly one lockfile of %s names one", dir, strings.Join(nodedeps.Managers(), ", "))
	}
	if manager == "npm" {
		deps, err := lockfileDependencies(dir)
		if err != nil {
			return licence.Set{}, err
		}
		return policy.Reject(deps), nil
	}
	deps, err := installedDependencies(ctx, dir)
	if err != nil {
		return licence.Set{}, fmt.Errorf("%s states no dependency licence and %s in %s is unreadable, and this scan runs no install to produce one: %w", manager, nodeModulesDir, dir, err)
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
	if end < 0 {
		return ""
	}
	if !strings.HasPrefix(strings.TrimSpace(trimmed[end+2:]), ":") {
		return ""
	}
	return trimmed[1 : end+1]
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
