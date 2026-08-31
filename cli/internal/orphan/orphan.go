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
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/pathmatch"
	"lydite/lydite/internal/runner"
)

// ErrNoRepository reports that root is not inside a git repository, so the
// set of files the repository contains cannot be established.
//
// Returned rather than fallen back to a filesystem walk. A walk would need
// its own list of directories to skip — node_modules, target, dist, coverage
// — which is a second copy of a judgement git already holds in .gitignore,
// and the copy that drifts is the one that starts reporting a build artefact
// as untested source.
var ErrNoRepository = errors.New("not a git repository, so the files it contains cannot be listed")

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
	out, err := lsFiles(ctx, root)
	if err != nil {
		return nil, err
	}
	exts := map[string]bool{}
	for _, e := range runner.SourceExts() {
		exts[e] = true
	}
	var files []string
	for _, p := range out {
		if exts[strings.ToLower(path.Ext(p))] {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files, nil
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// lsFiles asks git for the paths it knows about, relative to root.
//
// -z rather than newline-separated output, so a path containing a newline or
// a byte git would otherwise render as a quoted escape arrives intact. That
// removes the need to pin core.quotePath the way internal/referral has to.
// --deduplicate keeps a file that is both staged and present on disk from
// being counted twice.
func lsFiles(ctx context.Context, root string) ([]string, error) {
	res := executil.RunQuiet(ctx, root, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--deduplicate")
	if !res.Ok() {
		if !executil.RunQuiet(ctx, root, "git", "rev-parse", "--is-inside-work-tree").Ok() {
			return nil, ErrNoRepository
		}
		// git writes its diagnostics to stderr, which RunQuiet keeps apart
		// from stdout — reading Output here would report an empty string and
		// leave the row saying only "exit status 129". The first line is the
		// error; what follows it is a usage dump nobody acts on.
		if msg := firstLine(res.Stderr); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, res.Err
	}
	var out []string
	for _, p := range strings.Split(res.Output, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
