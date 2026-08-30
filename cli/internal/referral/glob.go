package referral

import (
	"fmt"
	"path"
	"strings"
)

// Match reports whether pattern covers a repository-root-relative,
// forward-slash path.
//
// Repository root, not scan root, and the distinction is not academic: the
// referral diff deliberately covers the whole repository so that a workflow
// edit outside a monorepo's scan root cannot slip past, so the paths reaching
// here carry no --dir prefix stripped. A repository scanned with --dir source
// writes "source/README.md", not "README.md".
//
// The syntax is deliberately a subset, and anchored:
//
//   - "*", "?" and character classes match within one path segment and never
//     across a "/", exactly as path.Match defines them.
//   - "**" matches zero or more whole segments, so "docs/**" covers
//     "docs/adr/0001.md" and "src/**/*_test.go" covers any depth.
//   - A pattern is always rooted. "README.md" matches the README at the
//     repository root and nothing else; matching one at any depth is spelled
//     "**/README.md".
//
// The anchoring is where this parts company with gitignore, whose slash-less
// patterns float to any depth. Floating is the right default for a list of
// things to skip, where over-matching costs nothing. It is the wrong default
// here: these patterns decide what merges without a human, so a pattern that
// silently covers more than it appears to is the whole failure mode. Every
// widening should have to be written down.
func Match(pattern, target string) bool {
	p, t := strings.Split(pattern, "/"), strings.Split(target, "/")
	// Each (pattern index, target index) pair has one answer, so memoising
	// them turns the "**" backtracking below from combinatorial in the
	// number of "**" segments into a product of the two lengths. Without it
	// a pattern carrying several "**" segments that ultimately fails to
	// match re-walks the same suffixes exponentially: eight of them against
	// a thirty-segment path is tens of millions of calls.
	seen := make(map[[2]int]bool, len(p)*len(t))
	return matchSegments(p, t, seen)
}

func matchSegments(pattern, target []string, seen map[[2]int]bool) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			key := [2]int{len(pattern), len(target)}
			if seen[key] {
				return false
			}
			seen[key] = true
			// Try every split point, shortest first. "**" is the only
			// construct that can consume a variable number of segments, so
			// it is the only one needing backtracking.
			for i := 0; i <= len(target); i++ {
				if matchSegments(pattern[1:], target[i:], seen) {
					return true
				}
			}
			return false
		}
		if len(target) == 0 {
			return false
		}
		// path.Match's error case is unreachable here: ValidatePattern has
		// already rejected any malformed pattern at load time, so a bad
		// pattern never reaches a matching run.
		ok, err := path.Match(pattern[0], target[0])
		if err != nil || !ok {
			return false
		}
		pattern, target = pattern[1:], target[1:]
	}
	return len(target) == 0
}

// ValidatePattern rejects a pattern path.Match cannot parse.
//
// Rejecting at load time rather than treating a malformed pattern as
// "matches nothing" is not about fail-closed — a pattern matching nothing
// refers more, not less. It is done because the author clearly meant
// something, and an exemption that silently does nothing is an exemption
// nobody will notice is broken.
func ValidatePattern(pattern string) error {
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("bad path pattern %q: %w", pattern, err)
		}
	}
	return nil
}
