package tsapisurface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// manifestName is the file every one of these resolutions starts at.
const manifestName = "package.json"

// manifest is the part of a package.json this package reads.
type manifest struct {
	Name    string `json:"name"`
	Main    string `json:"main"`
	Types   string `json:"types"`
	Typings string `json:"typings"`
	// Exports and Workspaces are each two shapes in npm's own schema — a
	// string or an object, a list or an object with a `packages` list — so
	// they are decoded where they are read rather than declared as one of
	// them here.
	Exports    json.RawMessage `json:"exports"`
	Workspaces json.RawMessage `json:"workspaces"`
}

// pkg is one npm package under a component's directory, with the entry points
// it declares.
type pkg struct {
	// rel is the package directory relative to the component's own, in slash
	// form, and "." for a component holding a single package.
	rel string
	// name is what package.json calls it. It names the package in a skip and
	// in a finding's detail, where a bare directory would say less.
	name string
	// entries are the subpaths a consumer can import, each resolved to the
	// declaration file the package names for it.
	entries []entry
}

// entry is one import a consumer can write, and the declaration it reaches.
type entry struct {
	// subpath is what the consumer writes — "." or "./sub".
	subpath string
	// dts is the declaration file, relative to the package directory and in
	// slash form.
	dts string
}

// label is how a package is named back to a caller: its declared name, and its
// directory for a package that declares none.
func (p pkg) label() string {
	if p.name != "" {
		return p.name
	}
	return p.rel
}

// packagesOf is every package under dir, in the order a report reads them.
//
// A directory whose package.json declares `workspaces` contributes its members
// and not itself: a workspace root is the thing that holds packages, and the
// surface a consumer reaches is each member's own. A directory declaring none
// is one package, at ".".
func packagesOf(dir string) ([]pkg, error) {
	root, err := readManifest(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	members := workspaceGlobs(root.Workspaces)
	if len(members) == 0 {
		return []pkg{{rel: ".", name: root.Name, entries: entriesOf(root)}}, nil
	}
	var out []pkg
	for _, rel := range workspaceMembers(dir, members) {
		m, err := readManifest(filepath.Join(dir, filepath.FromSlash(rel), manifestName))
		if err != nil {
			// A glob match with no readable manifest is not a package. The
			// pattern is the workspace root's own, so this is a directory that
			// happens to sit beside the members rather than a failure to
			// report.
			continue
		}
		out = append(out, pkg{rel: rel, name: m.Name, entries: entriesOf(m)})
	}
	return out, nil
}

// readManifest reads and decodes one package.json.
func readManifest(path string) (manifest, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the component directory a caller supplied, joined with a fixed filename
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// workspaceGlobs is the patterns a `workspaces` field holds, in either of the
// two shapes npm accepts.
func workspaceGlobs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(raw, &object); err == nil {
		return object.Packages
	}
	return nil
}

// workspaceMembers expands the patterns against dir and answers every matching
// directory, relative to dir, in sorted order.
//
// Whether a match is a package is decided by reading its manifest, which is the
// one thing that can say so; a second test here for the file's presence would
// agree with that reading only until one of the two learned something.
//
// Sorted rather than in the order the patterns were written: two patterns can
// match one directory, and a report whose rows moved because a pattern was
// added above another is a diff nobody can act on.
func workspaceMembers(dir string, globs []string) []string {
	seen := map[string]bool{}
	for _, glob := range globs {
		matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(glob)))
		if err != nil {
			// A malformed pattern matches nothing, which is what npm does with
			// it too.
			continue
		}
		for _, match := range matches {
			rel, err := filepath.Rel(dir, match)
			if err != nil {
				continue
			}
			seen[filepath.ToSlash(rel)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// entriesOf is the entry points a manifest names, resolved the way TypeScript's
// own module resolution reads them: `exports["."]`'s types condition, then
// `types`, then `typings`, then `main` with the declaration beside it.
//
// A package naming none has no surface a consumer can reach and yields nothing,
// which is what makes it a skip rather than a failure.
func entriesOf(m manifest) []entry {
	if es := exportEntries(m.Exports); len(es) > 0 {
		return es
	}
	for _, named := range []string{m.Types, m.Typings, m.Main} {
		if dts := declarationFor(named); dts != "" {
			return []entry{{subpath: ".", dts: dts}}
		}
	}
	return nil
}

// exportEntries is every subpath an `exports` field resolves to a declaration.
//
// npm's own rule decides which of the two shapes an object is: keys beginning
// with a dot are subpaths and anything else is a set of conditions, and the two
// are never mixed. A subpath pattern — a key holding `*` — names no one file, so
// there is nothing to point one run at and it is left out.
func exportEntries(raw json.RawMessage) []entry {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	switch v := value.(type) {
	case string:
		if dts := declarationFor(v); dts != "" {
			return []entry{{subpath: ".", dts: dts}}
		}
	case map[string]any:
		if !subpathMap(v) {
			if dts := declarationFor(condition(v)); dts != "" {
				return []entry{{subpath: ".", dts: dts}}
			}
			return nil
		}
		var out []entry
		for _, subpath := range sortedKeys(v) {
			if strings.Contains(subpath, "*") {
				continue
			}
			dts := declarationFor(target(v[subpath]))
			if dts == "" {
				continue
			}
			out = append(out, entry{subpath: subpath, dts: dts})
		}
		return out
	}
	return nil
}

// subpathMap reports whether an exports object maps subpaths rather than
// conditions.
func subpathMap(m map[string]any) bool {
	for key := range m {
		if strings.HasPrefix(key, ".") {
			return true
		}
	}
	return false
}

// target is one subpath's value, which is a file or a set of conditions.
func target(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		return condition(v)
	}
	return ""
}

// conditionOrder is which condition a declaration is read from, most specific
// first. `types` is what a consumer's compiler reads; the runtime conditions
// are the fallback, because the declaration beside the file a consumer loads is
// what that compiler resolves when no `types` condition is written.
var conditionOrder = []string{"types", "typings", "import", "require", "node", "default"}

// condition resolves one conditional-exports object to the file it names.
func condition(m map[string]any) string {
	for _, key := range conditionOrder {
		value, ok := m[key]
		if !ok {
			continue
		}
		if file := target(value); file != "" {
			return file
		}
	}
	return ""
}

// sortedKeys is a map's keys in order, so that two runs over one manifest
// produce one order.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// jsToDeclaration maps each JavaScript extension onto the declaration emitted
// beside it, which is what a consumer's compiler resolves for a package that
// names no types condition.
var jsToDeclaration = map[string]string{".js": ".d.ts", ".mjs": ".d.mts", ".cjs": ".d.cts"}

// declarationFor is the declaration file a named target resolves to, in slash
// form and relative to the package directory.
//
// A path that is already a declaration is taken as it stands, a JavaScript file
// resolves to the declaration beside it, and anything else — a `.ts` source, a
// directory — is carried through unchanged. Carrying it rather than dropping it
// is what makes a package that names an entry point api-extractor then refuses
// report as uncomputable, which is what it is, instead of as a package naming
// no surface at all.
func declarationFor(named string) string {
	clean := strings.TrimPrefix(filepath.ToSlash(named), "./")
	if clean == "" {
		return ""
	}
	for js, dts := range jsToDeclaration {
		if strings.HasSuffix(clean, js) {
			return strings.TrimSuffix(clean, js) + dts
		}
	}
	return clean
}
