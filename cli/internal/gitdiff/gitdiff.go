// Package gitdiff reads which paths a change touches.
//
// One implementation rather than two that agree today. internal/referral asks
// what a change touches to decide whether it may merge unread, and
// internal/affected asks the same question to decide which components run —
// both consequential, and a second reader would agree with this one until it
// learned about a git setting or a rename form this one already knew.
//
// Only the path half lives here. referral additionally parses the patch body
// for the lines a change adds and removes, which selection never reads and
// must not pay for: that parse deliberately fails closed on a line longer than
// its buffer, so a diff carrying a minified bundle would abort a selection that
// had no interest in its contents.
package gitdiff

import (
	"bufio"
	"fmt"
	"path"
	"sort"
	"strings"

	"context"

	"lydite/lydite/internal/executil"
)

// Config pins every git setting that decides what a diff says, because each is
// settable in a user or global gitconfig lydite does not control and each one
// silently changes what a caller sees.
//
//   - core.quotePath: without it ".github/workflows/déploy.yml" arrives
//     wrapped in quotes with escaped bytes, so a prefix test against a
//     component directory or ".github/workflows/" fails and the path stops
//     selecting what it should — while a "**" pattern still matches the
//     mangled string, so the failure is partial and silent.
//   - diff.relative: the command runs with cmd.Dir set to the scan root, so
//     this would scope the diff to that subtree and strip the prefix from what
//     survives. In a monorepo scanned with --dir source it drops
//     .github/workflows entirely — a change that must never go unnoticed by
//     either caller.
//   - diff.mnemonicPrefix: emits "w/" instead of "b/", which referral's patch
//     header parser would not recognise.
//
// A function returning a fresh slice rather than a package-level variable:
// every caller appends its own subcommand to these, and a shared backing array
// lets one caller's append land in another's arguments.
func Config() []string {
	return []string{
		"-c", "core.quotePath=false",
		"-c", "diff.relative=false",
		"-c", "diff.mnemonicPrefix=false",
	}
}

// Rename is a path a change moves, from its old location to its new one.
type Rename struct{ From, To string }

// Paths is what a change touches, repository-root-relative whatever directory
// the command ran in.
type Paths struct {
	// All is every path the change touches, both sides of a rename
	// included, sorted. Sorted rather than left in git's order so a caller's
	// report reads the same however the diff happened to be listed.
	All []string
	// Deleted is the subset of All the change removes outright.
	Deleted []string
	// Renamed carries both sides of each rename. A rename is how a file
	// leaves a tree without being deleted, so a caller reading only Deleted
	// misses it entirely.
	Renamed []Rename
}

// Changed reads the paths between base and HEAD.
//
// Working-tree state is deliberately excluded. Both callers have to reach the
// answer CI will reach, and CI sees commits; scoring uncommitted edits would
// answer a different question from the one that decides the merge.
//
// base must already be a verified commit. Passing an unvalidated value is how
// a caller reads a diff of nothing — which for referral passes everything and
// for selection runs nothing.
func Changed(ctx context.Context, dir, base string) (Paths, error) {
	args := append(Config(), "diff", "--name-status", "-M", base+"..HEAD")
	res := executil.RunQuiet(ctx, dir, "git", args...)
	if !res.Ok() {
		return Paths{}, fmt.Errorf("git diff --name-status %s..HEAD: %w", base, res.Err)
	}
	return parseNameStatus(res.Output)
}

// Prefix is where dir sits inside its repository, as a forward-slash path with
// a trailing slash, or "" at the repository root.
//
// It is git's own answer rather than a path computation, because the caller's
// --dir may reach the same directory through a symlink or a "..", and only git
// knows which work tree it landed in.
func Prefix(ctx context.Context, dir string) (string, error) {
	res := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--show-prefix")
	if !res.Ok() {
		return "", fmt.Errorf("git rev-parse --show-prefix: %w", res.Err)
	}
	return strings.TrimSpace(res.Output), nil
}

// Rel maps a repository-root-relative path onto prefix, reporting whether it
// falls inside at all.
//
// A path outside the scan root is not rewritten and not dropped: the caller
// decides what it means. For selection it means the path matches no component,
// which widens rather than narrows — a workflow edit outside a monorepo's scan
// root is exactly the change that must not select nothing.
func Rel(prefix, p string) (string, bool) {
	if prefix == "" {
		return p, true
	}
	if !strings.HasPrefix(p, prefix) {
		return p, false
	}
	return path.Clean(strings.TrimPrefix(p, prefix)), true
}

// parseNameStatus turns `git diff --name-status -M` into the paths a change
// touches, plus the subsets it deletes and renames.
//
// A rename contributes *both* of its paths. Counting only the destination
// would let a file be moved out of one tree — or into another — while the
// tree it left went unexamined, and moving a file between trees is precisely
// the change whose consequences depend on where it came from.
func parseNameStatus(out string) (Paths, error) {
	var p Paths
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			p.All = append(p.All, s)
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		for _, f := range fields[1:] {
			add(f)
		}
		switch {
		case fields[0] == "D":
			p.Deleted = append(p.Deleted, fields[1])
		case strings.HasPrefix(fields[0], "R") && len(fields) >= 3:
			p.Renamed = append(p.Renamed, Rename{From: fields[1], To: fields[2]})
		}
	}
	if err := scanner.Err(); err != nil {
		return Paths{}, fmt.Errorf("reading git diff --name-status: %w", err)
	}
	sort.Strings(p.All)
	sort.Strings(p.Deleted)
	return p, nil
}
