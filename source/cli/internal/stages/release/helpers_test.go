package releasestages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
)

// releaseCommit is one commit of a fixture history, and the tag cut at it when
// tag is not empty.
type releaseCommit struct {
	message string
	tag     string
}

// releaseRepo builds a repository with the given history, oldest first.
func releaseRepo(t *testing.T, commits ...releaseCommit) string {
	t.Helper()
	dir := t.TempDir()
	releaseGit(t, dir, "init", "-b", "main")
	releaseGit(t, dir, "config", "user.email", "t@example.com")
	releaseGit(t, dir, "config", "user.name", "t")
	for i, c := range commits {
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(c.message+string(rune('a'+i))), 0o600); err != nil {
			t.Fatal(err)
		}
		releaseGit(t, dir, "add", "-A")
		releaseGit(t, dir, "commit", "-m", c.message)
		if c.tag != "" {
			releaseGit(t, dir, "tag", c.tag)
		}
	}
	return dir
}

// releaseShallowClone clones origin to its last commit alone, fetching no
// tags, and writes each of tags' refs back without the commit it names, which
// is the checkout a clone that fetched refs without the history behind them
// leaves.
func releaseShallowClone(t *testing.T, origin string, tags ...string) string {
	t.Helper()
	dir := t.TempDir()
	if r := executil.RunQuiet(context.Background(), origin, "git", "clone", "--depth", "1", "--no-tags", "file://"+origin, dir); !r.Ok() {
		t.Fatalf("git clone: %v\n%s", r.Err, r.Output)
	}
	refs := filepath.Join(dir, ".git", "refs", "tags")
	if err := os.MkdirAll(refs, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		sha := releaseGit(t, origin, "rev-parse", tag)
		if err := os.WriteFile(filepath.Join(refs, tag), []byte(sha+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// releaseGit runs git in dir, failing the test when it does not succeed, and
// answers its trimmed output.
func releaseGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}
