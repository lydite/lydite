package reviewdecision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// titleWarning is the whole line PullRequestTitle returns for a payload at
// path that would not be read, up to the reason the read gave.
func titleWarning(path string) string {
	return "warning: could not read the event at " + path + " ("
}

const titleWarningTail = ") — a break declared only in the pull request title is not seen"

// No path is no pull request: an empty title and nothing to warn about, since
// a local review has no payload and the commits carry the declaration there.
func TestPullRequestTitleIsEmptyWithNoPath(t *testing.T) {
	title, warnings := PullRequestTitle("")
	if title != "" || warnings != nil {
		t.Errorf("PullRequestTitle(\"\") = (%q, %q), want empty and no warnings", title, warnings)
	}
}

// A payload that names a pull request yields its title, and warns about
// nothing.
func TestPullRequestTitleReadsTheTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"number": 7, "pull_request": {"title": "feat(api)!: drop Do"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	title, warnings := PullRequestTitle(path)
	if title != "feat(api)!: drop Do" {
		t.Errorf("title = %q, want %q", title, "feat(api)!: drop Do")
	}
	if len(warnings) != 0 {
		t.Errorf("a readable payload must warn about nothing, got %q", warnings)
	}
}

// A payload that is missing, will not be read, or will not parse is one
// warning and no title, never an error: the title can only add a referral,
// so failing over an unreadable one would turn an additive source into a
// blocker.
func TestPullRequestTitleWarnsOnAnEventItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	unreadable := t.TempDir()
	malformed := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(malformed, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, reason string
	}{
		{"missing", missing, "reading the event payload"},
		{"unreadable", unreadable, "reading the event payload"},
		{"malformed", malformed, "parsing the event payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			title, warnings := PullRequestTitle(tc.path)
			if title != "" {
				t.Errorf("title = %q, want empty", title)
			}
			if len(warnings) != 1 {
				t.Fatalf("want exactly one warning, got %q", warnings)
			}
			w := warnings[0]
			if !strings.HasPrefix(w, titleWarning(tc.path)+tc.reason) || !strings.HasSuffix(w, titleWarningTail) {
				t.Errorf("warning = %q, want %q…%q", w, titleWarning(tc.path)+tc.reason, titleWarningTail)
			}
			if strings.Contains(w, "\n") {
				t.Errorf("a warning is a whole line without its newline, got %q", w)
			}
		})
	}
}

// The path is read exactly as given: resolving a default from the
// environment is the caller's, so a set GITHUB_EVENT_PATH never stands in for
// an empty one.
func TestPullRequestTitleDoesNotReadTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"number": 7, "pull_request": {"title": "feat!: x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_EVENT_PATH", path)
	title, warnings := PullRequestTitle("")
	if title != "" || warnings != nil {
		t.Errorf("PullRequestTitle(\"\") = (%q, %q), want empty with GITHUB_EVENT_PATH set", title, warnings)
	}
}
