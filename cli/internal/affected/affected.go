// Package affected decides which components a change could have broken.
//
// A component is affected when the change touches its directory, one of its
// watch paths, a component it depends on transitively, or an invalidator.
// Everything here is a question about paths: nothing is read, no manifest is
// parsed and no source is inspected, the same stance internal/orphan takes and
// for the same reason — a rule that starts reading files has to be right about
// every language it meets.
//
// The default on ignorance is to widen. A changed path matching no component,
// no watch pattern and no invalidator selects every component, mirroring the
// way a change matching no exemption is referred. Narrowing there would make
// the invalidator set below a safety mechanism, where every file family missing
// from it is a change that silently tests nothing; widening makes it a
// performance concern, where a gap costs a slower run and never a missed one.
// See ADR 0018.
package affected

import (
	"path"
	"sort"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/pathmatch"
)

// Invalidators are the paths that make every component affected.
//
// Their purpose is to override a component directory match that is too narrow,
// not to catch a path that matches nothing — widening already handles that. A
// repository with one component rooted at "." and another at "web" would
// otherwise have a change to .lydite/components.yml select the root component
// alone, when a change to the component declaration is precisely the change
// that can affect all of them.
//
// The set is built in and cannot be removed or added to. Not removable for the
// reason the built-in disqualifier set is not: a repository able to drop
// ".lydite/**" could make a change to its own component declaration run
// nothing. Not extensible because `watch` already says "this outside file
// invalidates me" one level down, and a second mechanism for it would be
// surface bought with nothing.
//
// Every pattern is "**"-prefixed, which is the one place lydite deliberately
// departs from pathmatch's anchoring. A lockfile matters wherever it sits, and
// spelling the depth-independence into the pattern keeps the matcher's own
// semantics untouched and puts the widening where a reader can see it.
var Invalidators = []string{
	// lydite's own configuration: what the components are, what is excluded
	// from the orphan gate, and every gate knob.
	".lydite/**",

	// Lockfiles. What a suite resolves to is decided here, so a change is
	// felt by every component the tree installs for, not only the one whose
	// directory the file sits in.
	"**/Cargo.lock", "**/go.sum", "**/go.work.sum",
	"**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml", "**/bun.lockb",

	// Workspace and package manifests. Which members a workspace holds, and
	// which dependencies a member declares, are both stated here — and
	// telling a workspace root from a member means parsing the file, which
	// is the line this package does not cross.
	"**/go.mod", "**/go.work", "**/Cargo.toml",
	"**/package.json", "**/pnpm-workspace.yaml",

	// Toolchain declarations. internal/toolchain reads these to decide which
	// compiler every ecosystem is provisioned with.
	"**/rust-toolchain.toml", "**/rust-toolchain", "**/.nvmrc", "**/.tool-versions",
}

// Kind is why a component is affected.
type Kind string

const (
	// KindDir: the change touches a path under the component's directory.
	KindDir Kind = "dir"
	// KindWatch: the change touches one of the component's watch paths.
	KindWatch Kind = "watch"
	// KindDependency: a component this one depends on, transitively, is
	// affected. This is the one thing depends_on is for; the scheduler does
	// not read it.
	KindDependency Kind = "dependency"
	// KindInvalidator: the change touches a path that affects everything.
	KindInvalidator Kind = "invalidator"
	// KindUnmatched: the change touches a path under no component, on no
	// watch list and matching no invalidator. It affects everything, because
	// the alternative is a narrowing nothing would report.
	KindUnmatched Kind = "unmatched"
)

// Reason is why one component was selected, as a line a reader can act on.
type Reason struct {
	Kind Kind
	// Path is the changed path that selected the component. Empty for
	// KindDependency, where no path selected it directly.
	Path string
	// Via names the affected component the dependency edge came through.
	// Empty for every other kind.
	Via string
}

// String renders a reason as the audit line the select row carries.
func (r Reason) String() string {
	switch r.Kind {
	case KindDependency:
		return "depends on " + r.Via
	case KindUnmatched:
		return r.Path + " (under no component)"
	case KindInvalidator:
		return r.Path + " (invalidator)"
	default:
		return r.Path
	}
}

// Result is the selection, in declaration order.
//
// Skipped carries the components that were not selected rather than dropping
// them, because that is what lets a reader tell "not affected" from "not
// declared" — and a report that simply omitted them would read as a complete
// run over fewer components.
type Result struct {
	Selected []component.Component
	Skipped  []component.Component
	// Reasons is keyed by component name and covers exactly Selected.
	Reasons map[string]Reason
}

// Select returns the components a change touching changed could have broken.
//
// changed is scan-root-relative, forward-slash, and carries both sides of a
// rename: moving a file between two components must run both, since the source
// lost a file and a rule reading only the destination runs the half of the
// change that cannot break.
//
// An empty diff selects nothing, and is the only way to select nothing. Every
// changed path selects at least one component — it matched something, or it
// matched nothing and therefore matched everything — so "0 of N affected" can
// only mean HEAD has no changes against the merge-base, never that the
// narrowing went wrong.
func Select(f component.File, changed []string) Result {
	reasons := map[string]Reason{}

	all := func(r Reason) Result {
		for _, c := range f.Components {
			reasons[c.Name] = r
		}
		return assemble(f, reasons)
	}

	for _, p := range changed {
		p = strings.TrimPrefix(path.Clean(p), "./")
		if matchesAny(Invalidators, p) {
			return all(Reason{Kind: KindInvalidator, Path: p})
		}
		matched := false
		for _, c := range f.Components {
			switch {
			case under(p, c.Dir):
				matched = true
				keep(reasons, c.Name, Reason{Kind: KindDir, Path: p})
			case matchesAny(c.Watch, p):
				matched = true
				keep(reasons, c.Name, Reason{Kind: KindWatch, Path: p})
			}
		}
		if !matched {
			return all(Reason{Kind: KindUnmatched, Path: p})
		}
	}

	spread(f, reasons)
	return assemble(f, reasons)
}

// keep records the first reason a component was selected for. First rather
// than last so the audit line does not depend on the order git happened to
// list the diff in.
func keep(reasons map[string]Reason, name string, r Reason) {
	if _, ok := reasons[name]; !ok {
		reasons[name] = r
	}
}

// spread walks depends_on backwards: a component is affected when something it
// depends on is. The edge is declared because it is not always derivable —
// a client generated from a spec is an edge no tool sees — and selection is
// the only thing that reads it.
func spread(f component.File, reasons map[string]Reason) {
	dependents := map[string][]component.Component{}
	for _, c := range f.Components {
		for _, d := range c.DependsOn {
			dependents[d] = append(dependents[d], c)
		}
	}
	queue := make([]string, 0, len(reasons))
	for name := range reasons {
		queue = append(queue, name)
	}
	// Sorted, so the reason a diamond dependency records names the same
	// component on every run. Map iteration order would otherwise put this
	// process's hash seed into the report.
	sort.Strings(queue)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		for _, c := range dependents[name] {
			if _, ok := reasons[c.Name]; ok {
				continue
			}
			reasons[c.Name] = Reason{Kind: KindDependency, Via: name}
			queue = append(queue, c.Name)
		}
	}
}

// assemble splits the declaration into selected and skipped, preserving
// declaration order in both. Declaration order rather than selection order, so
// two runs over the same change produce the same document.
func assemble(f component.File, reasons map[string]Reason) Result {
	res := Result{Reasons: reasons}
	for _, c := range f.Components {
		if _, ok := reasons[c.Name]; ok {
			res.Selected = append(res.Selected, c)
		} else {
			res.Skipped = append(res.Skipped, c)
		}
	}
	return res
}

// under reports whether p sits inside dir. A component rooted at "." claims
// the whole tree, which is what the invalidator set exists to override.
func under(p, dir string) bool {
	dir = path.Clean(dir)
	if dir == "." {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

func matchesAny(patterns []string, p string) bool {
	for _, pat := range patterns {
		if pathmatch.Match(pat, p) {
			return true
		}
	}
	return false
}
