package recordstages

import (
	"context"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
)

// recordGit runs git in dir and fails the test when it does not succeed.
func recordGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}

// recordRepo is a repository with one commit holding files, and the tree
// that commit points at.
func recordRepo(t *testing.T, files map[string]string) (dir, tree string) {
	t.Helper()
	dir = t.TempDir()
	recordGit(t, dir, "init", "--quiet", "-b", "main")
	recordGit(t, dir, "config", "user.email", "t@example.com")
	recordGit(t, dir, "config", "user.name", "t")
	recordWrite(t, dir, files)
	recordGit(t, dir, "add", "-A")
	recordGit(t, dir, "commit", "--quiet", "-m", "measured")
	return dir, recordGit(t, dir, "rev-parse", "HEAD^{tree}")
}

func TestBindTreeBindsMeasurementsOfTheCheckedOutTree(t *testing.T) {
	dir, tree := recordRepo(t, map[string]string{"a.go": "package a\n"})

	out, err := BindTree(context.Background(), BindTreeIn{Dir: dir, Folded: Measurements{Tree: tree}})
	if err != nil {
		t.Fatalf("BindTree: %v", err)
	}
	if want := (BindTreeOut{Bound: true, Head: tree, Measured: tree}); out != want {
		t.Errorf("BindTree = %+v, want %+v", out, want)
	}
}

// Measurements of another tree are an answer, not an error: they were read,
// and what they say is that they must not be recorded here. Both trees are
// carried so the refusal can name them.
func TestBindTreeDoesNotBindMeasurementsOfAnotherTree(t *testing.T) {
	dir, tree := recordRepo(t, map[string]string{"a.go": "package a\n"})
	const other = "0123456789abcdef0123456789abcdef01234567"

	out, err := BindTree(context.Background(), BindTreeIn{Dir: dir, Folded: Measurements{Tree: other}})
	if err != nil {
		t.Fatalf("BindTree: %v", err)
	}
	if want := (BindTreeOut{Bound: false, Head: tree, Measured: other}); out != want {
		t.Errorf("BindTree = %+v, want %+v", out, want)
	}
}

// A checkout with no tree is the one thing here that is an error: there is
// nothing for a mismatch to be measured against.
func TestBindTreeWithNoCheckedOutTreeIsAnError(t *testing.T) {
	_, err := BindTree(context.Background(), BindTreeIn{Dir: t.TempDir(), Folded: Measurements{Tree: "deadbeef"}})
	const prefix = "resolving the tree that is checked out: git rev-parse HEAD^{tree}: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %v, want it to start %q", err, prefix)
	}
}
