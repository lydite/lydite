// Package orphan is the guard on a declared component list.
//
// Components are declared rather than discovered, because the declaration is
// the reviewable statement of what gets tested and its history is the record
// of every change to that. The cost of that choice is that a declared list
// fails open: nothing breaks when it goes stale, so a directory nobody
// declared is tested by nobody while every run still reports green.
//
// So every source file must fall under some component's directory or an
// explicit exclude. A file under neither is an orphan, and the author clears
// it by declaring a component or writing the exclude — a gate, in the sense
// CONTEXT.md gives the word, not a referral: there is always work that
// satisfies it. See docs/adr/0016-components-and-lydite-run-tests.md.
//
// It reads nothing from config.FileName, and a language disabled there is
// still checked. "rust.enabled: false" says which checks run over a
// repository's Rust; it does not say that no component should test it. Taking
// it as both would let a repository drop a whole language out of this gate by
// changing what its linter looks at — a widening of what may go untested,
// made silently, in a file whose history is not the record of that. A
// repository that means it writes the exclude, which is one line and says so
// where a reviewer is already looking.
//
// It is a path question and nothing more. It reads no manifest, parses no
// source and asks internal/detect nothing, which is what lets it catch a
// whole undeclared directory that has no manifest in it yet — the case
// detection cannot see at all. A generated file is not special-cased for the
// same reason: recognising one means reading it, and a gate that starts
// reading files is one that has to be right about every language it meets.
// The exclude is where a repository says so.
package orphan

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/pathmatch"
	"lydite/lydite/internal/runner"
)

// ErrNoRepository reports that root is not inside a git repository, so the
// files it contains cannot be listed.
var ErrNoRepository = gitdiff.ErrNoRepository

// ErrNoFiles reports that git listed no source file at all under the scan
// root.
//
// Distinct from finding none orphaned, and the distinction is the whole
// point. A scan root that is itself ignored — a vendored checkout, a build
// directory someone pointed --dir at — is inside a work tree and lists
// nothing, exits zero, and would otherwise render as a green gate that had
// examined the entire repository. A gate that could not run must never read
// as one that did.
var ErrNoFiles = errors.New("git lists no source file under the scan root, so there is nothing to check")

// Result is what the gate found.
type Result struct {
	// Orphans are the source files under no component and no exclude, in
	// path order, relative to the scan root.
	Orphans []string
	// Scanned is how many source files were considered.
	Scanned int
	// UnusedExcludes are declared excludes that covered no file.
	//
	// Reported rather than rejected. An exclude covering nothing is very
	// likely a mistake — a directory name written where a pattern was
	// needed, since "scripts" matches the path "scripts" and never a file
	// inside it — but it is also what an exclude honestly becomes the day
	// the file it named is deleted, and failing a build over tidying is how
	// a gate earns a reputation for firing on ordinary work.
	UnusedExcludes []string
}

// Find lists the source files under root that no component's dir covers and
// no exclude matches.
//
// The tree comes from git: tracked files, plus untracked ones git is not
// ignoring. Both halves matter. Tracked alone would miss the newly written
// file that has not been staged, which is precisely the moment the author
// can most cheaply act on the answer; including ignored files would report
// every compiled artefact and every installed dependency as untested source.
func Find(ctx context.Context, root string, f component.File) (Result, error) {
	files, err := sourceFiles(ctx, root)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, ErrNoFiles
	}
	used := make([]bool, len(f.Excludes))
	res := Result{Scanned: len(files)}
	for _, p := range files {
		// Every matching exclude is marked, and matching is asked of every
		// file rather than only the uncovered ones. Both halves keep the
		// unused warning honest: an exclude whose files all happen to sit
		// inside a component covers them all the same, and of two excludes
		// that overlap, stopping at the first would report the second as
		// covering nothing. Either would put a warning on a correct
		// declaration, on every run, which is how a diagnostic teaches
		// people to ignore it.
		excluded := false
		for i, e := range f.Excludes {
			if pathmatch.Match(e, p) {
				used[i] = true
				excluded = true
			}
		}
		if excluded || coveredByComponent(p, f.Components) {
			continue
		}
		res.Orphans = append(res.Orphans, p)
	}
	for i, u := range used {
		if !u {
			res.UnusedExcludes = append(res.UnusedExcludes, f.Excludes[i])
		}
	}
	return res, nil
}

// coveredByComponent reports whether a component's dir contains the file.
//
// A dir of "." is the whole scan root, which is the shape a single-component
// repository declares and would otherwise match nothing: "./x" is not a
// prefix any cleaned path carries.
func coveredByComponent(file string, components []component.Component) bool {
	for _, c := range components {
		dir := path.Clean(filepath(c.Dir))
		if dir == "." || strings.HasPrefix(file, dir+"/") {
			return true
		}
	}
	return false
}

// filepath normalises a declared dir to the forward-slash form every path
// here is in. Component dirs are written relative to the scan root with "/"
// separators, which is what the declaration's own validation already
// requires.
func filepath(dir string) string { return strings.ReplaceAll(dir, "\\", "/") }

// sourceFiles lists the repository's source files under root, sorted.
func sourceFiles(ctx context.Context, root string) ([]string, error) {
	out, err := gitdiff.Tracked(ctx, root)
	if err != nil {
		return nil, err
	}
	return sourceOf(out), nil
}

// Unscanned lists, per language, the source files no component of that
// language covers — the files `lydite scan` runs no check over.
//
// It is a different question from Find's, and the difference is the language.
// Find asks whether any component's directory contains the file, because a
// component tests what is under it whatever the file is written in. A scanner
// is per language: a Go component covering `web/app.ts` by containment runs
// gosec, which will never look at it. So a file is unscanned when no component
// both contains it *and* implies its language.
//
// This is the half of the guard `Find` cannot give `lydite scan`. A component
// rooted at `.` contains every path in the repository, so a Go component at
// the root leaves TypeScript beside it orphaning nothing — and a repository
// declaring one of its two Go modules leaves the other one contained by
// nothing while Find is the only thing that would have said so, in a command
// the consumer may not run.
//
// Excludes narrow it, exactly as they narrow Find. An exclude is the
// repository's reviewable statement that a path is claimed by no component,
// which is the same statement this would otherwise ask for. It does not narrow
// what gets *scanned* — nothing scans these files either way — so it is not
// the declaration answering two questions.
//
// enabled decides which languages are worth reporting; a language switched off
// in config is an answer rather than an oversight. Passing nil reports every
// language.
func Unscanned(ctx context.Context, root string, f component.File, enabled func(runner.Lang) bool) ([]Gap, error) {
	tracked, err := gitdiff.Tracked(ctx, root)
	if err != nil {
		return nil, err
	}
	files := sourceOf(tracked)
	// Symmetric with Find, and for its reason: git listing no source at all is
	// a different answer from finding nothing unscanned. `--dir` pointed at a
	// gitignored tree — a vendored checkout, build output — lists nothing and
	// exits zero, and a silent no-op there reads exactly like a repository
	// whose every file is accounted for.
	if len(files) == 0 {
		return nil, ErrNoFiles
	}
	modules := goModuleDirs(tracked)
	byLang := map[runner.Lang][]string{}
	for _, p := range files {
		ext := strings.ToLower(path.Ext(p))
		lang, ok := runner.LangForExt(ext)
		if !ok || ambiguousExt[ext] || (enabled != nil && !enabled(lang)) {
			continue
		}
		if excluded(p, f.Excludes) || coveredByLanguage(p, lang, f.Components, modules) {
			continue
		}
		byLang[lang] = append(byLang[lang], p)
	}
	var out []Gap
	for lang, paths := range byLang {
		out = append(out, Gap{Lang: lang, Files: paths})
	}
	// By language, so two runs of one repository report in one order.
	sort.Slice(out, func(i, j int) bool { return out[i].Lang < out[j].Lang })
	return out, nil
}

// ambiguousExt is the extensions whose presence is not evidence of a codebase
// nothing scans.
//
// The .js family is the extension of build output, configuration and tooling
// glue in every ecosystem, lydite's languages included: a Go repository with a
// docs/theme.js or a root *.config.js is an ordinary Go repository, not one
// with unscanned TypeScript in it. Warning about those would fire on ordinary
// work, on every run, with an exclude as the only way to silence it — and that
// exclude would change what the orphan gate reads too.
//
// The orphan gate deliberately keeps the full set, because it asks a different
// question: whether a component claims the file, which a component rooted at
// `.` does. This asks whether a body of source is checked by nothing, and a
// single .js cannot answer it either way. The cost is a genuinely unscanned
// JavaScript-only package going unmentioned, which is narrow and is the
// direction that keeps the diagnostic worth reading.
var ambiguousExt = map[string]bool{
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

// Gap is one language's unscanned source.
type Gap struct {
	Lang  runner.Lang
	Files []string
}

// coveredByLanguage reports whether a component contains the file, runs checks
// for the language it is written in, and — for Go — actually reaches it.
//
// Containment is not enough for Go, and the exception is exact rather than a
// heuristic: a nested go.mod starts a separate module that the enclosing
// module's package graph excludes, so `./...` at an ancestor never compiles it
// and neither gosec nor govulncheck sees it. A component rooted at `.` in a
// repository with a second module at `sdk/` therefore contains every Go file
// in the tree and scans only its own. Reproduced by putting the same G306 in
// both modules and watching one of the two be reported.
//
// Rust deliberately gets no equivalent rule. A Cargo.toml between the
// component root and the file may be a workspace member cargo already covers
// or an unrelated crate it does not, and telling those apart means reading the
// manifest — so a path-shaped rule would warn about crates that are perfectly
// well scanned, which is how a diagnostic earns being ignored. TypeScript
// needs none: Biome walks the tree from where it is pointed.
func coveredByLanguage(file string, lang runner.Lang, components []component.Component, modules map[string]bool) bool {
	for _, c := range components {
		r, ok := runner.Lookup(c.Runner)
		if !ok {
			// A component declaring a raw command implies no language, so
			// nothing here can say whether its checks reach this file. It is
			// not a gap: a component *is* declared for it, and `lydite scan`
			// already reports that component unmeasured with the reason. A
			// warning too would say the same thing twice, once wrongly —
			// telling an author to declare a component they have declared,
			// with an exclude as the only way to silence it, which would take
			// those files out of the orphan gate as well.
			if coveredByComponent(file, []component.Component{c}) {
				return true
			}
			continue
		}
		if r.Lang != lang {
			continue
		}
		if !coveredByComponent(file, []component.Component{c}) {
			continue
		}
		if lang == runner.Go && !sameGoModule(file, filepath(c.Dir), modules) {
			continue
		}
		return true
	}
	return false
}

// sameGoModule reports whether the file and the component's directory sit in
// the same Go module.
//
// Both sides are resolved the same way, and that is the point: the question is
// not whether the component's directory *is* a module root but whether the
// module it belongs to is the one the file belongs to. A component declared at
// `services/api` in a repository with a single root go.mod is scanned exactly
// as it should be — `gosec ./...` run there is inside that module — and asking
// whether `services/api` held a go.mod would report its own files as scanned
// by nobody, naming a component the repository has already declared.
//
// A file with no enclosing module at all answers yes, so a component that is
// broken in louder ways — govulncheck exits with "no go.mod file" — does not
// also collect a warning about it.
func sameGoModule(file, dir string, modules map[string]bool) bool {
	fileModule := nearestModule(path.Dir(file), modules)
	if fileModule == "" {
		return true
	}
	return fileModule == nearestModule(path.Clean(dir), modules)
}

// nearestModule is the closest enclosing directory holding a go.mod, or "" if
// none does.
func nearestModule(dir string, modules map[string]bool) string {
	for d := dir; ; d = path.Dir(d) {
		if modules[d] {
			return d
		}
		if d == "." || d == "/" {
			return ""
		}
	}
}

// goModuleDirs is every directory holding a go.mod, read off the file list.
//
// A filename's presence and nothing else — no manifest is opened, which is
// what keeps this the same kind of question the rest of the package asks.
func goModuleDirs(tracked []string) map[string]bool {
	dirs := map[string]bool{}
	for _, p := range tracked {
		if path.Base(p) != "go.mod" || goIgnored(path.Dir(p)) {
			continue
		}
		dirs[path.Dir(p)] = true
	}
	return dirs
}

// goIgnored reports whether the Go toolchain itself ignores a directory when
// resolving packages: "testdata", and any name beginning with "." or "_".
//
// It matters here for the reason it mattered to discovery: a go.mod under
// testdata/ is a fixture rather than a project, and routinely unbuildable on
// purpose. Treating one as a module boundary would put every fixture .go file
// outside its component's module and warn that nothing scans it — on an
// ordinary Go repository layout, on every run, which is how a diagnostic earns
// being ignored. The go command ignores those directories too, so a fixture is
// not scanned by the enclosing module either; nothing is being claimed as
// covered that is not.
func goIgnored(dir string) bool {
	// The scan root itself. path.Dir("go.mod") is ".", whose one segment
	// begins with a dot, so the loop below would read a repository's own root
	// module as an ignored directory and never record it as a boundary.
	// Nothing answers differently today, because a file with no enclosing
	// module is treated as being in its component's — but the two are only
	// accidentally compensating, and the next reader of either has no reason
	// to expect it.
	if dir == "." {
		return false
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "testdata" || strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") {
			return true
		}
	}
	return false
}

// sourceOf keeps the paths written in a language lydite has a runner for.
func sourceOf(tracked []string) []string {
	exts := map[string]bool{}
	for _, e := range runner.SourceExts() {
		exts[e] = true
	}
	var files []string
	for _, p := range tracked {
		if exts[strings.ToLower(path.Ext(p))] {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files
}

// excluded reports whether any exclude covers the file.
func excluded(file string, excludes []string) bool {
	for _, e := range excludes {
		if pathmatch.Match(e, file) {
			return true
		}
	}
	return false
}
