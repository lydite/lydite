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
	"lydite/lydite/internal/gitdiff"
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
// The file patterns are "**"-prefixed, which is the one place lydite
// deliberately departs from pathmatch's anchoring: a lockfile matters wherever
// it sits. Spelling the depth-independence into the pattern keeps the
// matcher's own semantics untouched and puts the widening where a reader can
// see it. ".lydite/**" is the exception and stays anchored, because lydite
// reads its configuration from the scan root and nowhere else — a
// "docs/.lydite/config.yml" configures nothing.
//
// Unexported so the set is a floor rather than a suggestion. Exported it would
// be a mutable package-level slice any caller could append to or replace,
// which is a weaker guarantee than the doc above claims.
var invalidators = []string{
	// lydite's own configuration: what the components are, what is excluded
	// from the orphan gate, and every gate knob.
	".lydite/**",

	// What CI runs, and how. A component rooted at "." claims every path, so
	// in such a repository nothing ever reaches the widening rule and only
	// this set protects the other components — a workflow edit would
	// otherwise select the root component alone and leave every other
	// component unrun.
	".github/workflows/**", ".github/actions/**",

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
	// KindDefaultBranch: selection does not apply. The run is on the branch
	// a change is selected *against*, so there is no change to select by.
	KindDefaultBranch Kind = "default-branch"
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
	case KindDefaultBranch:
		return "every component runs on the default branch"
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

// Path is one changed path, already mapped onto the scan root.
//
// Outside says the path lies outside the scan root altogether. Such a path can
// match no component by construction — a component directory is scan-root
// relative — so it widens, and carrying the flag is what stops a
// repository-root path from colliding with a same-named component directory
// and narrowing to it instead.
type Path struct {
	Rel     string
	Outside bool
}

// All selects every component, for a caller that has established that
// selection does not apply.
//
// The default branch is that caller. ADR 0016 requires a run there to be
// complete, because a forgotten depends_on edge is caught at merge or never —
// and the merge-base of the default branch with itself is its own head, so a
// selection computed there narrows to nothing and reports a green run that
// executed no suite at all.
func All(f component.File, r Reason) Result {
	reasons := make(map[string]Reason, len(f.Components))
	for _, c := range f.Components {
		reasons[c.Name] = r
	}
	return assemble(f, reasons)
}

// Paths maps repository-root-relative paths onto the scan root, which is where
// component directories are written. prefix comes from gitdiff.Prefix.
//
// It lives here rather than at the call site because forgetting it is silent:
// the paths still match something, just not the right thing.
func Paths(prefix string, all []string) []Path {
	out := make([]Path, 0, len(all))
	for _, p := range all {
		rel, inside := gitdiff.Rel(prefix, p)
		out = append(out, Path{Rel: rel, Outside: !inside})
	}
	return out
}

// Select returns the components a change touching changed could have broken.
//
// changed carries both sides of a rename: moving a file between two components
// must run both, since the source lost a file and a rule reading only the
// destination runs the half of the change that cannot break.
//
// An empty diff selects nothing, and is the only way to select nothing. Every
// changed path selects at least one component — it matched something, or it
// matched nothing and therefore matched everything — so "0 of N affected" can
// only mean HEAD has no changes against the merge-base, never that the
// narrowing went wrong.
func Select(f component.File, changed []Path) Result {
	reasons := map[string]Reason{}

	// The first path that matches nothing, or matches an invalidator. Held
	// rather than acted on, so that a component the change *also* touched
	// directly keeps the reason naming the reader's own edit: a widening
	// applied the moment it is found overwrites every reason recorded before
	// it and suppresses every one after.
	var widen *Reason
	note := func(r Reason) {
		if widen == nil {
			widen = &r
		}
	}

	for _, p := range changed {
		if p.Outside {
			note(Reason{Kind: KindUnmatched, Path: p.Rel})
			continue
		}
		rel := strings.TrimPrefix(path.Clean(p.Rel), "./")
		if matchesAny(invalidators, rel) {
			note(Reason{Kind: KindInvalidator, Path: rel})
			continue
		}
		matched := false
		for _, c := range f.Components {
			switch {
			case under(rel, c.Dir):
				// A component rooted at "." is selected by containment like
				// any other, and is deliberately not evidence that the path
				// was understood. `dir: .` says where a component is rooted —
				// a Go module at the repository root has no other way to
				// spell it — not that it tests every file in the tree, and
				// reading it as a match switches the widening rule off for
				// the whole repository: nothing is ever unmatched, so a
				// root-level Makefile selects that component alone while
				// every other one silently does not run.
				matched = matched || !claimsEverything(c.Dir)
				keep(reasons, c.Name, Reason{Kind: KindDir, Path: rel})
			case matchesAny(c.Watch, rel):
				// A watch always counts. It names a specific path outside the
				// component's own directory, which is a statement about that
				// file rather than about where the component lives.
				matched = true
				keep(reasons, c.Name, Reason{Kind: KindWatch, Path: rel})
			}
		}
		if !matched {
			note(Reason{Kind: KindUnmatched, Path: rel})
		}
	}

	if widen != nil {
		for _, c := range f.Components {
			keep(reasons, c.Name, *widen)
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

// under reports whether p sits inside dir. A component rooted at "." contains
// every path, and is selected accordingly; whether that containment counts as
// the path having been matched is the caller's question, and claimsEverything
// is where it is answered.
func under(p, dir string) bool {
	dir = path.Clean(dir)
	if dir == "." {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// claimsEverything reports whether a component's directory contains every path
// in the repository, which is true of exactly one spelling: the scan root.
//
// Such a component contains a path without that containment saying anything
// about the path, which is why it is not evidence of a match. Every other
// directory is a real statement — a change under `web/` is a change to the
// component rooted there.
func claimsEverything(dir string) bool {
	return path.Clean(dir) == "."
}

func matchesAny(patterns []string, p string) bool {
	for _, pat := range patterns {
		if pathmatch.Match(pat, p) {
			return true
		}
	}
	return false
}
