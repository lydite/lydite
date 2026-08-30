package referral

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"lydite/lydite/internal/executil"
)

// AddedLine is one line the change introduces, with the file it landed in.
// Only added lines are collected: a disqualifier asks what this change
// brought in, and a suppression that was already there is somebody else's
// referral, already given.
type AddedLine struct {
	Path string
	Text string
}

// Change is everything about a diff that the referral decision reads.
//
// Paths are repository-root-relative, and cover the whole repository rather
// than only what is under --dir. That is deliberate and differs from
// internal/coverage, which scopes its diff to --dir with git's --relative:
// coverage measures what it can measure, so paths it cannot reach are
// legitimately outside its remit, whereas referral decides whether a *pull
// request* needs a human. A workflow edit sitting outside a monorepo's scan
// root is exactly the change that must not slip past, so the diff is taken
// whole and --dir serves only to locate the exemptions file.
type Change struct {
	Paths      []string
	AddedLines []AddedLine
}

// Changes reads the diff between base and HEAD.
//
// Working-tree state is deliberately excluded. The verdict has to be the
// same one CI will reach, and CI sees commits; a local run that quietly
// scored uncommitted edits would answer a different question from the one
// that decides the merge. Callers warn separately when the tree is dirty —
// see Dirty.
func Changes(ctx context.Context, dir, base string) (Change, error) {
	names := executil.RunQuiet(ctx, dir, "git", "diff", "--name-status", "-M", base+"..HEAD")
	if !names.Ok() {
		return Change{}, fmt.Errorf("git diff --name-status %s..HEAD: %w", base, names.Err)
	}
	paths, err := parseNameStatus(names.Output)
	if err != nil {
		return Change{}, err
	}

	// -U0 drops context lines, so every "+" line in the output is one the
	// change actually introduced.
	patch := executil.RunQuiet(ctx, dir, "git", "-c", "diff.mnemonicPrefix=false", "diff", "-M", "--unified=0", base+"..HEAD")
	if !patch.Ok() {
		return Change{}, fmt.Errorf("git diff --unified=0 %s..HEAD: %w", base, patch.Err)
	}
	return Change{Paths: paths, AddedLines: parseAddedLines(patch.Output)}, nil
}

// parseNameStatus turns `git diff --name-status -M` into the set of paths a
// change touches.
//
// A rename contributes *both* of its paths. Counting only the destination
// would let a file be moved out of an exempt tree — or into one — while the
// exemption still matched, and moving a file between trees is precisely the
// kind of change whose safety depends on where it came from.
func parseNameStatus(out string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		for _, p := range fields[1:] {
			add(p)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading git diff --name-status: %w", err)
	}
	return paths, nil
}

func parseAddedLines(patch string) []AddedLine {
	var added []AddedLine
	var file string
	scanner := bufio.NewScanner(strings.NewReader(patch))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
			if file == "/dev/null" {
				file = ""
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if file != "" {
				added = append(added, AddedLine{Path: file, Text: strings.TrimPrefix(line, "+")})
			}
		}
	}
	return added
}

// Dirty reports whether the working tree has changes Changes did not see.
//
// It exists so a local run can say so out loud. Silently deciding on HEAD
// while the developer is looking at edited files is the one way this command
// gives a confidently wrong answer.
func Dirty(ctx context.Context, dir string) bool {
	r := executil.RunQuiet(ctx, dir, "git", "status", "--porcelain")
	return r.Ok() && strings.TrimSpace(r.Output) != ""
}

// RootRelative returns dir's path from the repository root, "" when dir is
// the root itself. It is what turns --dir into the location of the
// exemptions file inside a commit, for reading the file out of the
// merge-base rather than out of the working tree.
func RootRelative(ctx context.Context, dir string) (string, error) {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--show-prefix")
	if !r.Ok() {
		return "", fmt.Errorf("git rev-parse --show-prefix: %w", r.Err)
	}
	return strings.TrimSpace(r.Output), nil
}
