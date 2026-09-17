// Package apisurface compares a Go module's exported API between two trees on
// disk and reports every incompatible change as a located finding.
//
// The comparison is `golang.org/x/exp/apidiff` over packages loaded with
// `go/packages`, minus `internal/` packages and test packages — see
// [ADR 0040]. Only incompatible changes are reported: a compatible addition is
// ordinary growth, and a gate that fired on one would fire on every release.
//
// The package knows nothing about git, components or verdicts. It is handed
// two directories that already hold the module checked out, and hands back raw
// findings for a caller to finish.
//
// [ADR 0040]: ../../../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
package apisurface

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/exp/apidiff"
	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"

	"lydite/lydite/internal/finding"
)

// ErrModulePathChanged is a module whose path is not the same in both trees.
//
// Under Go's import-compatibility rule a major bump is spelled as a new module
// path, so every package would read as removed and added again. The comparison
// is scoped to a module whose path holds still across the range, and saying so
// is better than reporting an entire module removed.
var ErrModulePathChanged = errors.New("module path changed")

// removalDetail is what a finding says when the symbol it names is gone from
// the head tree, so the line it carries is the merge-base's.
const removalDetail = "removed in this change; located at its declaration in the merge-base"

// Compare reports every incompatible change to the exported API of the Go
// module between baseDir and headDir — two directories already checked out,
// each holding the module at its own go.mod root. Materialising the base tree
// is the caller's work; this package does no git.
//
// gate and component name the caller's Finding.Gate and Finding.Component.
// Both are supplied rather than fixed here because this package knows nothing
// about report rows or declared components.
//
// env is the environment the loader runs under, layered over this process's
// own — the resolved Go toolchain a caller provisioned, which is what makes
// the comparison run under the version the module declares rather than under
// whatever `go` the PATH happens to lead to. Nil runs the loader under the
// ambient environment.
//
// The findings come back raw. Path is relative to the tree the symbol was
// located in — headDir, or baseDir for a removal — and **not** to the scan
// root, so a caller that knows the component's directory rebases them the way
// `scan`'s labelled step does for every other producer. Ordinal and Anchor are
// left at their zero values for the same reason: both are decisions made over
// a report's whole set and against the lines the change touched, neither of
// which this package is given.
func Compare(baseDir, headDir, gate, component string, env []string) ([]finding.Finding, error) {
	base, err := loadTree(baseDir, env)
	if err != nil {
		return nil, fmt.Errorf("merge-base tree: %w", err)
	}
	head, err := loadTree(headDir, env)
	if err != nil {
		return nil, fmt.Errorf("head tree: %w", err)
	}
	if base.module != head.module {
		return nil, fmt.Errorf("%w from %s to %s", ErrModulePathChanged, base.module, head.module)
	}

	var findings []finding.Finding
	// Packages are walked in import-path order rather than through
	// apidiff.ModuleChanges, which iterates a map: the order of a report's
	// rows would otherwise vary run to run for no reason a reader could act
	// on.
	for _, path := range union(base.packages, head.packages) {
		basePkg, inBase := base.packages[path]
		headPkg, inHead := head.packages[path]
		switch {
		case inBase && inHead:
			findings = append(findings, changes(basePkg, headPkg, gate, component)...)
		case inBase:
			findings = append(findings, removedPackage(basePkg, gate, component))
		}
		// A package only the head declares is an addition, which breaks
		// nobody.
	}
	return findings, nil
}

// changes compares one package present in both trees.
func changes(base, head *pkg, gate, component string) []finding.Finding {
	report := apidiff.Changes(base.types, head.types)
	messages := make([]string, 0, len(report.Changes))
	for _, c := range report.Changes {
		if !c.Compatible {
			messages = append(messages, c.Message)
		}
	}
	// The order a report is read in is this package's own guarantee, not
	// something the library promises.
	sort.Strings(messages)

	findings := make([]finding.Finding, 0, len(messages))
	for _, message := range messages {
		f := finding.Finding{
			Gate:      gate,
			Component: component,
			// apidiff's own wording is the claim. Rewording it would put
			// lydite's paraphrase where the tool's statement was.
			Message: message,
			Site:    symbol(message),
		}
		// A symbol still in the head is located there; one that is gone is
		// located where it was declared, which is the only line that can point
		// at what was removed.
		if obj := head.resolve(f.Site); obj != nil {
			f.Path, f.Line = head.position(obj)
		} else if obj := base.resolve(f.Site); obj != nil {
			f.Path, f.Line = base.position(obj)
			f.Detail = []string{removalDetail}
		}
		findings = append(findings, f)
	}
	return findings
}

// removedPackage is a package the head no longer declares. apidiff phrases it
// this way itself, and the finding is located at the package clause of the
// first file that declared it.
func removedPackage(base *pkg, gate, component string) finding.Finding {
	f := finding.Finding{
		Gate:      gate,
		Component: component,
		Message:   fmt.Sprintf("package %s: removed", base.types.Path()),
		Site:      "package " + base.types.Path(),
		Detail:    []string{removalDetail},
	}
	f.Path, f.Line = base.at(base.clause)
	return f
}

// symbol is the part of an apidiff message that names what changed.
//
// A message is `<name>: <what happened>`, where `<name>` is the object's name
// within its package — `Removed`, `(*T).Method` — followed by the struct field
// or interface method that changed, if the change is to a part of it:
// `Config.Timeout`, `Store.Put`. A Change carries no structured symbol and no
// position, so the name is recovered from the message it is formatted into.
func symbol(message string) string {
	name, _, ok := strings.Cut(message, ": ")
	if !ok {
		return message
	}
	return name
}

// pkg is one loaded package of the module under comparison.
type pkg struct {
	types *types.Package
	fset  *token.FileSet
	// clause is the package clause of the first file, which is where a whole
	// removed package is reported.
	clause token.Pos
	// root is the tree the package was loaded from, which every path a finding
	// carries is relative to.
	root string
}

// resolve finds the object an apidiff message names, or nil when the package
// no longer declares it.
//
// The leading segment is a top-level name and each one after it a field or
// method of what came before, so `Store.Put` resolves to the interface method
// and carries the method's own line rather than the interface's.
func (p *pkg) resolve(name string) types.Object {
	// A message may append a part after a comma rather than a dot — a method
	// set, say — which names no declaration of its own.
	head, _, _ := strings.Cut(name, ",")
	segments := strings.Split(head, ".")
	// A receiver is formatted as `(*T)`, and the declaration to point at is
	// T's.
	obj := p.types.Scope().Lookup(strings.Trim(segments[0], "(*)"))
	if obj == nil {
		return nil
	}
	for _, segment := range segments[1:] {
		part, _, _ := types.LookupFieldOrMethod(obj.Type(), true, p.types, segment)
		if part == nil {
			// The enclosing object is still a truer location than none.
			break
		}
		obj = part
	}
	return obj
}

// position is where an object is declared, as a tree-relative path and a line.
func (p *pkg) position(obj types.Object) (string, int) { return p.at(obj.Pos()) }

// at resolves a position within the tree the package was loaded from. A
// position that cannot be named relative to that tree is no location at all —
// a guessed line points a reader at code that is not what the claim is about.
func (p *pkg) at(pos token.Pos) (string, int) {
	if !pos.IsValid() {
		return "", 0
	}
	position := p.fset.Position(pos)
	rel, err := filepath.Rel(resolved(p.root), resolved(position.Filename))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", 0
	}
	return filepath.ToSlash(rel), position.Line
}

// tree is a module loaded from one directory.
type tree struct {
	module string
	// packages is keyed by module-relative import path, so the two trees are
	// matched on what a consumer imports rather than on a directory.
	packages map[string]*pkg
}

// load reads the module's public surface: every package it declares except the
// ones nothing outside the module can reach.
//
// `internal/` is Go's own public/private boundary, so an internal package's
// exported symbol is not part of any API. Test packages are left out by not
// asking for them at all — an external `_test` package is importable by
// nothing, and its surface changing is not a change any consumer can see.
//
// The comparison is over the default, untagged build. A symbol that exists
// only under a build tag is invisible to it, because comparing every tag
// combination is a combinatorial question no single gate can answer.
func loadTree(dir string, env []string) (*tree, error) {
	module, err := modulePath(dir)
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir: dir,
		// packages.Load runs the go tool over a tree the change under review
		// controls, inside a job that holds a token this package must not let
		// that tree reach. credentialFree drops anything token-shaped from the
		// inherited environment before a caller's own env or GOWORK is
		// layered over it, and CGO_ENABLED=0 is set last of all so the tree
		// cannot turn a build flag into arbitrary code running with
		// whatever's left in the environment. GOWORK is set last of the
		// rest, since the last occurrence of a key is the one a child process
		// reads and a caller's environment must not be able to reinstate a
		// workspace.
		Env:   append(append(credentialFree(os.Environ()), env...), "GOWORK=off", "CGO_ENABLED=0"),
		Tests: false,
	}
	loaded, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", dir, err)
	}
	t := &tree{module: module, packages: map[string]*pkg{}}
	for _, loadedPkg := range loaded {
		if len(loadedPkg.Errors) > 0 {
			return nil, fmt.Errorf("loading %s: %w", loadedPkg.PkgPath, loadedPkg.Errors[0])
		}
		if internalPath(loadedPkg.PkgPath) || strings.HasSuffix(loadedPkg.PkgPath, "_test") {
			continue
		}
		p := &pkg{types: loadedPkg.Types, fset: loadedPkg.Fset, root: dir}
		if len(loadedPkg.Syntax) > 0 {
			p.clause = loadedPkg.Syntax[0].Name.Pos()
		}
		t.packages[strings.TrimPrefix(loadedPkg.PkgPath, module)] = p
	}
	return t, nil
}

// credentialToken is what a key is checked for, case-insensitively, to decide
// whether it might carry a secret. Broad on purpose: this filters the
// environment handed to a build run over code the change under review
// controls, so a variable let through by mistake is worse than one held back
// that the load never needed.
var credentialTokens = []string{"TOKEN", "SECRET", "KEY", "PASSWORD", "CREDENTIAL"}

// credentialFree drops every entry of env whose key looks like it might carry
// a secret.
//
// packages.Load runs the go tool, and with it any code the tree under
// comparison declares, over a tree the pull request being reviewed controls —
// inside review, which holds a token that can write the referral status
// clearance depends on. Handing that token to a build of untrusted code is
// exactly what CONTEXT.md's Relay entry and this repository's workflows both
// forbid a job running the change's own code from doing, so nothing
// token-shaped reaches the loader regardless of what the caller passes
// alongside it.
func credentialFree(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(key)
		suspect := false
		for _, token := range credentialTokens {
			if strings.Contains(upper, token) {
				suspect = true
				break
			}
		}
		if !suspect {
			out = append(out, kv)
		}
	}
	return out
}

// modulePath reads the module the tree declares, rather than assuming the
// caller knows it: the path is what every package's import path is measured
// against, and a tree that declares a different one is not this module.
func modulePath(dir string) (string, error) {
	name := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(name) // #nosec G304 -- the root of a tree this package was asked to compare
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", name, err)
	}
	path := modfile.ModulePath(data)
	if path == "" {
		return "", fmt.Errorf("%s declares no module path", name)
	}
	return path, nil
}

// internalPath answers whether Go's own import rule already hides the package.
func internalPath(path string) bool {
	return path == "internal" || strings.HasPrefix(path, "internal/") ||
		strings.HasSuffix(path, "/internal") || strings.Contains(path, "/internal/")
}

// union is every import path either tree declares, in order.
func union(base, head map[string]*pkg) []string {
	paths := make([]string, 0, len(base)+len(head))
	for path := range base {
		paths = append(paths, path)
	}
	for path := range head {
		if _, ok := base[path]; !ok {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

// resolved follows any symlink in a path, so that a tree reached by one name
// and reported by the loader under another still yields a relative path.
func resolved(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}
