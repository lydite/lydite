package pathmatch

import (
	"strings"
	"testing"
	"time"
)

func TestMatchIsAnchoredAndSegmentAware(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"README.md", "README.md", true},
		// Anchored, unlike gitignore: a slash-less pattern does not float to
		// any depth. These patterns decide what merges without a human, so
		// one that silently covers more than it appears to is the whole
		// failure mode.
		{"README.md", "docs/README.md", false},
		{"**/README.md", "docs/vendor/README.md", true},
		{"docs/**", "docs/adr/0013.md", true},
		{"docs/**", "docsite/index.md", false},
		{"*.md", "README.md", true},
		// "*" never crosses a separator, so a single-star pattern cannot
		// reach into a subdirectory.
		{"*.md", "docs/README.md", false},
		{"src/**/*_test.go", "src/a/b/thing_test.go", true},
		{"src/**/*_test.go", "src/thing_test.go", true},
		{"src/**/*_test.go", "src/thing.go", false},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.target); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}

// Each (pattern index, target index) pair has one answer, so memoising them
// keeps a pattern carrying many "**" segments from re-walking the same
// suffixes exponentially.
func TestManyDoubleStarsDoNotBlowUp(t *testing.T) {
	pattern := strings.Repeat("**/", 12) + "nomatch.txt"
	target := strings.Repeat("a/", 30) + "b.txt"
	done := make(chan bool, 1)
	go func() { done <- Match(pattern, target) }()
	select {
	case got := <-done:
		if got {
			t.Errorf("Match(%q, ...) = true, want false", pattern)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Match did not terminate promptly on a pattern with many ** segments")
	}
}
