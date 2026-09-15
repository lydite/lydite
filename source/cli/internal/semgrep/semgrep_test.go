package semgrep

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	cases := []struct {
		name          string
		rulesetConfig string
		appToken      bool
		baseSHA       string
		want          []string
	}{
		{"no token, auto ruleset", "auto", false, "", []string{"scan", "--config", "auto", "--error"}},
		{"no token, custom ruleset", "p/security-audit", false, "", []string{"scan", "--config", "p/security-audit", "--error"}},
		{"no token, base SHA scopes the scan to the diff", "auto", false, "abc123", []string{"scan", "--config", "auto", "--error", "--baseline-commit", "abc123"}},
		{"token present, auto ruleset omits --config", "auto", true, "", []string{"ci"}},
		{"token present, custom ruleset still passed", "p/security-audit", true, "", []string{"ci", "--config", "p/security-audit"}},
		{"token present, empty ruleset omits --config", "", true, "", []string{"ci"}},
		{"token present, base SHA ignored — semgrep ci is already diff-aware", "auto", true, "abc123", []string{"ci"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildArgs(tc.rulesetConfig, tc.appToken, tc.baseSHA)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("buildArgs(%q, %v, %q) = %v, want %v", tc.rulesetConfig, tc.appToken, tc.baseSHA, got, tc.want)
			}
		})
	}
}

// A `.semgrepignore` replaces Semgrep's built-in default ignore list rather
// than extending it, so a file written to skip one directory widens the scan to
// every path the defaults held back — vendored code, build output and the test
// trees `*_test.go` and `tests/` name. The warning is the only thing between
// that and a findings count that moves with no rule change.
func TestASemgrepignoreIsNamedWithWhatItDrops(t *testing.T) {
	dir := t.TempDir()
	writeIgnore(t, dir, "docs/design/reference/\n")

	var w bytes.Buffer
	warnSemgrepignore(&w, dir)

	got := w.String()
	if !strings.Contains(got, semgrepignoreFile) {
		t.Errorf("warning = %q, want it to name the file that caused it", got)
	}
	for _, pattern := range defaultIgnorePatterns {
		if !strings.Contains(got, pattern) {
			t.Errorf("warning = %q, want it to name the dropped default %q", got, pattern)
		}
	}
}

// Nothing to say: without the file, Semgrep applies its own defaults and the
// scan is the one the ruleset describes.
func TestNoSemgrepignoreIsSilent(t *testing.T) {
	var w bytes.Buffer
	warnSemgrepignore(&w, t.TempDir())

	if w.Len() != 0 {
		t.Errorf("warning = %q, want silence", w.String())
	}
}

// Presence is what opts out of the defaults, not content: an empty file adds
// nothing back and still drops all of them.
func TestAnEmptySemgrepignoreStillWarns(t *testing.T) {
	dir := t.TempDir()
	writeIgnore(t, dir, "")

	var w bytes.Buffer
	warnSemgrepignore(&w, dir)

	if !strings.Contains(w.String(), "*_test.go") {
		t.Errorf("warning = %q, want an empty file reported like any other", w.String())
	}
}

func writeIgnore(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, semgrepignoreFile), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
