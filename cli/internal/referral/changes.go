package referral

import (
	"bufio"
	"context"
	"fmt"
	"path"
	"strings"

	"lydite/lydite/internal/executil"
)

// DiffLine is one line the change introduces or removes, with the file it
// landed in.
type DiffLine struct {
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
	// Paths is every path the change touches, both sides of a rename
	// included.
	Paths []string
	// Deleted is the subset of Paths the change removes outright.
	Deleted []string
	// Added and Removed are the lines the change introduces and drops.
	// Both are needed: an added suppression is evidence that something tried
	// to make a verdict go away, and so is a deleted test.
	Added   []DiffLine
	Removed []DiffLine
}

// quotePathOff keeps git from C-quoting a path that contains non-ASCII,
// control or quote bytes.
//
// Without it, ".github/workflows/déploy.yml" arrives wrapped in double quotes
// with escaped bytes, so a prefix test against ".github/workflows/" fails and
// the veto that path was supposed to trip never fires — while a "**" pattern
// still matches the mangled string, so an exemption can still cover it.
var quotePathOff = []string{"-c", "core.quotePath=false"}

// Changes reads the diff between base and HEAD.
//
// Working-tree state is deliberately excluded. The verdict has to be the same
// one CI will reach, and CI sees commits; a local run that quietly scored
// uncommitted edits would answer a different question from the one that
// decides the merge. Callers warn separately when the tree is dirty — see
// Dirty.
//
// base must already be a verified commit. Passing an unvalidated flag value
// here is how the gate reads a diff of nothing and passes everything; see
// cmd/lydite's resolveReviewBase.
func Changes(ctx context.Context, dir, base string) (Change, error) {
	nameArgs := append(append([]string{}, quotePathOff...),
		"diff", "--name-status", "-M", base+"..HEAD")
	names := executil.RunQuiet(ctx, dir, "git", nameArgs...)
	if !names.Ok() {
		return Change{}, fmt.Errorf("git diff --name-status %s..HEAD: %w", base, names.Err)
	}
	paths, deleted, err := parseNameStatus(names.Output)
	if err != nil {
		return Change{}, err
	}

	// --text stops a "-diff" or "binary" gitattribute from replacing a
	// hunk body with "Binary files ... differ". Diff attributes are read
	// from the branch, so without this a change that adds its own
	// .gitattributes hides its own added lines — the same self-declaration
	// the merge-base read of the exemptions file exists to prevent.
	// .gitattributes is a disqualifier for the same reason.
	//
	// -U0 drops context lines, so every "+" line inside a hunk is one the
	// change actually introduced.
	patchArgs := append(append([]string{}, quotePathOff...),
		"-c", "diff.mnemonicPrefix=false", "diff", "-M", "--text", "--unified=0", base+"..HEAD")
	patch := executil.RunQuiet(ctx, dir, "git", patchArgs...)
	if !patch.Ok() {
		return Change{}, fmt.Errorf("git diff --unified=0 %s..HEAD: %w", base, patch.Err)
	}
	added, removed := parseDiffLines(patch.Output)
	return Change{Paths: paths, Deleted: deleted, Added: added, Removed: removed}, nil
}

// parseNameStatus turns `git diff --name-status -M` into the set of paths a
// change touches, plus the subset it deletes.
//
// A rename contributes *both* of its paths. Counting only the destination
// would let a file be moved out of an exempt tree — or into one — while the
// exemption still matched, and moving a file between trees is precisely the
// kind of change whose safety depends on where it came from.
func parseNameStatus(out string) (paths, deleted []string, err error) {
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
		if fields[0] == "D" {
			deleted = append(deleted, fields[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading git diff --name-status: %w", err)
	}
	return paths, deleted, nil
}

// parseDiffLines extracts the lines a patch adds and removes.
//
// It tracks the diff's structure rather than trusting a line's prefix, and
// that is load-bearing: a "+++ " test alone is satisfied by an added source
// line whose own text is "++ /dev/null", which would set the current file to
// nothing and silently drop every remaining added line in it. Content can
// only appear after an "@@" header, and a "+++" header can only appear
// before one, so the hunk flag makes the two unambiguous — and it also lets
// an added line beginning with "++" be read as content, which a bare "+++"
// prefix test discards.
func parseDiffLines(patch string) (added, removed []DiffLine) {
	var file string
	inHunk := false
	scanner := bufio.NewScanner(strings.NewReader(patch))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, inHunk = "", false
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case !inHunk && strings.HasPrefix(line, "+++ "):
			// git pads the header with a tab when the path contains a
			// space, so the raw remainder is not the path.
			file = strings.TrimRight(strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/"), "\t")
			if file == "/dev/null" {
				file = ""
			}
		case inHunk && file != "" && strings.HasPrefix(line, "+"):
			added = append(added, DiffLine{Path: file, Text: strings.TrimPrefix(line, "+")})
		case inHunk && file != "" && strings.HasPrefix(line, "-"):
			removed = append(removed, DiffLine{Path: file, Text: strings.TrimPrefix(line, "-")})
		}
	}
	return added, removed
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

// IsTestPath reports whether p looks like a file whose job is to test other
// code, by the naming conventions of the three ecosystems lydite gates.
func IsTestPath(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, "_test.rs"):
		return true
	}
	for _, infix := range []string{".test.", ".spec."} {
		if strings.Contains(base, infix) {
			return true
		}
	}
	for _, dir := range []string{"test/", "tests/", "__tests__/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	return false
}
