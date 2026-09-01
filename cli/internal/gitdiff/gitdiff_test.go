package gitdiff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a real git repository and returns its path. A fixture list of
// paths would agree with whatever this package does; only git can say what a
// rename, a deletion or an awkwardly named file actually looks like in a diff.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		run(t, dir, args...)
	}
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// base commits everything as the starting point and returns its SHA.
func base(t *testing.T, dir string) string {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-qm", "base")
	return strings.TrimSpace(run(t, dir, "rev-parse", "HEAD"))
}

func commit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-qm", "change")
}

func TestChangedReportsAddedAndModifiedPaths(t *testing.T) {
	dir := repo(t)
	write(t, dir, "keep.go", "package a\n")
	b := base(t, dir)

	write(t, dir, "go/api/main.go", "package main\n")
	write(t, dir, "keep.go", "package a // edited\n")
	commit(t, dir)

	got, err := Changed(context.Background(), dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "go/api/main.go,keep.go"; join(got.All) != want {
		t.Errorf("All = %q, want %q", join(got.All), want)
	}
}

func TestChangedReportsDeletions(t *testing.T) {
	dir := repo(t)
	write(t, dir, "gone.go", "package a\n")
	b := base(t, dir)

	if err := os.Remove(filepath.Join(dir, "gone.go")); err != nil {
		t.Fatal(err)
	}
	commit(t, dir)

	got, err := Changed(context.Background(), dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "gone.go"; join(got.Deleted) != want {
		t.Errorf("Deleted = %q, want %q", join(got.Deleted), want)
	}
	// A deleted file still selects its component: the component lost source.
	if want := "gone.go"; join(got.All) != want {
		t.Errorf("All = %q, want %q", join(got.All), want)
	}
}

// TestChangedReportsBothSidesOfARename: moving a file between two components
// must run both. A reader of the destination alone runs the half of the change
// that cannot break.
func TestChangedReportsBothSidesOfARename(t *testing.T) {
	dir := repo(t)
	write(t, dir, "go/sdk/client.go", strings.Repeat("package sdk\n", 40))
	b := base(t, dir)

	if err := os.MkdirAll(filepath.Join(dir, "go", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "mv", "go/sdk/client.go", "go/api/client.go")
	commit(t, dir)

	got, err := Changed(context.Background(), dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := "go/api/client.go,go/sdk/client.go"; join(got.All) != want {
		t.Errorf("All = %q, want both sides %q", join(got.All), want)
	}
	if len(got.Renamed) != 1 || got.Renamed[0].From != "go/sdk/client.go" || got.Renamed[0].To != "go/api/client.go" {
		t.Errorf("Renamed = %+v, want one go/sdk/client.go -> go/api/client.go", got.Renamed)
	}
}

// TestChangedKeepsAwkwardPathsIntact guards the core.quotePath pinning. Without
// it git wraps a non-ASCII path in quotes with escaped bytes, and a prefix test
// against a component directory then fails while a "**" pattern still matches
// the mangled string — so the path silently stops selecting its component.
func TestChangedKeepsAwkwardPathsIntact(t *testing.T) {
	dir := repo(t)
	write(t, dir, "keep.go", "package a\n")
	b := base(t, dir)

	write(t, dir, "go/api/déploy.go", "package main\n")
	write(t, dir, "web/a file.ts", "export {}\n")
	commit(t, dir)

	got, err := Changed(context.Background(), dir, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go/api/déploy.go", "web/a file.ts"} {
		if !contains(got.All, want) {
			t.Errorf("All = %q, want it to contain %q verbatim", join(got.All), want)
		}
	}
}

// TestChangedIgnoresDiffRelative guards the diff.relative pinning. Set in a
// global gitconfig it scopes the diff to the directory git runs in and strips
// the prefix from what survives — so a change outside the scan root would
// vanish, which is exactly the change selection must never miss.
func TestChangedIgnoresDiffRelative(t *testing.T) {
	dir := repo(t)
	write(t, dir, "go/api/main.go", "package main\n")
	b := base(t, dir)

	run(t, dir, "config", "diff.relative", "true")
	write(t, dir, ".github/workflows/ci.yml", "on: push\n")
	write(t, dir, "go/api/main.go", "package main // edited\n")
	commit(t, dir)

	got, err := Changed(context.Background(), filepath.Join(dir, "go", "api"), b)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got.All, ".github/workflows/ci.yml") {
		t.Errorf("All = %q, want a workflow edit outside the scan root to survive", join(got.All))
	}
	if !contains(got.All, "go/api/main.go") {
		t.Errorf("All = %q, want repository-root-relative paths", join(got.All))
	}
}

// TestPrefix is how a repository-root-relative diff is mapped onto
// scan-root-relative component directories.
func TestPrefix(t *testing.T) {
	dir := repo(t)
	write(t, dir, "source/go/api/main.go", "package main\n")
	base(t, dir)

	for _, tc := range []struct{ at, want string }{
		{".", ""},
		{"source", "source/"},
		{"source/go/api", "source/go/api/"},
	} {
		got, err := Prefix(context.Background(), filepath.Join(dir, filepath.FromSlash(tc.at)))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("Prefix(%q) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestChangedOutsideARepositoryIsAnError(t *testing.T) {
	if _, err := Changed(context.Background(), t.TempDir(), "HEAD"); err == nil {
		t.Error("Changed outside a git repository returned no error; selection must refuse rather than narrow to nothing")
	}
}

func join(s []string) string { return strings.Join(s, ",") }

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestParseNameStatusCollectsRenamesAndDeletions works on git's output
// directly, because --name-status' rename records carry both paths behind a
// similarity-suffixed status letter and that shape is what a caller depends on.
func TestParseNameStatusCollectsRenamesAndDeletions(t *testing.T) {
	got, err := parseNameStatus("R100\tsrc/a_test.go\tsrc/a.go\nD\tsrc/b_test.go\nM\tREADME.md\n")
	if err != nil {
		t.Fatalf("parseNameStatus: %v", err)
	}
	if want := "README.md,src/a.go,src/a_test.go,src/b_test.go"; join(got.All) != want {
		t.Errorf("All = %q, want %q — a rename contributes both of its paths", join(got.All), want)
	}
	if len(got.Deleted) != 1 || got.Deleted[0] != "src/b_test.go" {
		t.Errorf("Deleted = %v, want [src/b_test.go]", got.Deleted)
	}
	if len(got.Renamed) != 1 || got.Renamed[0] != (Rename{From: "src/a_test.go", To: "src/a.go"}) {
		t.Errorf("Renamed = %+v, want one src/a_test.go -> src/a.go", got.Renamed)
	}
}

func TestRelMapsOntoTheScanRoot(t *testing.T) {
	for _, tc := range []struct {
		prefix, in, want string
		inside           bool
	}{
		{"", "go/api/main.go", "go/api/main.go", true},
		{"source/", "source/go/api/main.go", "go/api/main.go", true},
		{"source/", "source/README.md", "README.md", true},
		// Outside the scan root: not rewritten and not dropped. For
		// selection it matches no component, which widens.
		{"source/", ".github/workflows/ci.yml", ".github/workflows/ci.yml", false},
	} {
		got, inside := Rel(tc.prefix, tc.in)
		if got != tc.want || inside != tc.inside {
			t.Errorf("Rel(%q, %q) = %q,%v; want %q,%v", tc.prefix, tc.in, got, inside, tc.want, tc.inside)
		}
	}
}
