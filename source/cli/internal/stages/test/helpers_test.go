package teststages

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/ui"
)

// write writes one file under root, creating its directory.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// git runs git in dir and fails the test when it does not succeed.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// pushedRepo is a repository holding files, committed once and pushed to an
// origin whose main is that commit — so HEAD is its own merge-base until the
// test commits again.
func pushedRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	origin := t.TempDir()
	for rel, body := range files {
		write(t, root, rel, body)
	}
	git(t, origin, "init", "--quiet", "--bare", "-b", "main")
	git(t, root, "init", "--quiet", "-b", "main")
	git(t, root, "config", "user.email", "t@example.com")
	git(t, root, "config", "user.name", "t")
	git(t, root, "remote", "add", "origin", origin)
	git(t, root, "add", "-A")
	git(t, root, "commit", "--quiet", "-m", "base")
	git(t, root, "push", "--quiet", "origin", "main")
	return root
}

// commit writes one file and commits the tree, moving HEAD past the base.
func commit(t *testing.T, root, rel, body string) {
	t.Helper()
	write(t, root, rel, body)
	git(t, root, "add", "-A")
	git(t, root, "commit", "--quiet", "-m", "change")
}

// labels are the rows' labels, in order.
func labels(rows []ui.Row) string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Label
	}
	return strings.Join(out, ",")
}

// row is the row under label, failing the test when there is none.
func row(t *testing.T, rows []ui.Row, label string) ui.Row {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no %q row in %s", label, labels(rows))
	return ui.Row{}
}
