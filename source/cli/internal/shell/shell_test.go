package shell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/gitdiff"
)

// One run, in json1, reading no .shellcheckrc, over each script as a path that
// cannot read as an option.
func TestArgvIsOneJSON1RunOverEachScript(t *testing.T) {
	got := argv([]string{"a.sh", "-rf.sh", "sub/b.bash"})
	want := []string{"--format=json1", "--norc", "./a.sh", "./-rf.sh", "./sub/b.bash"}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestReportedVersionReadsTheVersionLine(t *testing.T) {
	out := "ShellCheck - shell script analysis tool\nversion: 0.11.0\nlicense: GNU General Public License, version 3\n"
	if got := reportedVersion(out); got != "0.11.0" {
		t.Errorf("reportedVersion = %q, want 0.11.0", got)
	}
	if got := reportedVersion("ShellCheck - shell script analysis tool\n"); got != "" {
		t.Errorf("reportedVersion = %q, want empty for output naming no version", got)
	}
}

// `shellcheck --version` prints the upstream release and never the packaging
// revision, so the pin is compared on its first three components — or every
// run reinstalls.
func TestThePinIsComparedOnTheUpstreamRelease(t *testing.T) {
	for pinned, want := range map[string]string{
		"0.11.0.1": "0.11.0",
		"0.11.0":   "0.11.0",
		"1.0":      "1.0",
	} {
		if got := upstreamVersion(pinned); got != want {
			t.Errorf("upstreamVersion(%q) = %q, want %q", pinned, got, want)
		}
	}
	if got := upstreamVersion(version); strings.Count(got, ".") != 2 {
		t.Errorf("upstreamVersion(%q) = %q, want the three-part release the pinned binary reports", version, got)
	}
}

// The scripts git knows about under the directory, relative to it: an ignored
// tree is not the repository's source, a path the index lists and the tree no
// longer holds would stop ShellCheck reading the rest, and a file in another
// language is not a script.
func TestScriptsUnderAreTheShellFilesGitKnows(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"svc/deploy.sh":              "#!/bin/sh\n",
		"svc/lib/helpers.BASH":       "true\n",
		"svc/main.go":                "package main\n",
		"svc/node_modules/x/post.sh": "#!/bin/sh\n",
		"svc/gone.sh":                "#!/bin/sh\n",
		"other/outside.sh":           "#!/bin/sh\n",
	} {
		write(t, root, rel, content)
	}
	write(t, root, ".gitignore", "node_modules/\n")
	gitIn(t, root, "init", "--quiet")
	gitIn(t, root, "add", "--", ".")
	if err := os.Remove(filepath.Join(root, "svc", "gone.sh")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "svc/untracked.sh", "#!/bin/sh\n")

	got, err := scriptsUnder(context.Background(), filepath.Join(root, "svc"))
	if err != nil {
		t.Fatalf("scriptsUnder: %v", err)
	}
	want := []string{"deploy.sh", "lib/helpers.BASH", "untracked.sh"}
	if !slices.Equal(got, want) {
		t.Errorf("scripts = %q, want %q", got, want)
	}
}

// Outside a repository there is no list to read, and the check says so rather
// than reading an empty list as nothing to check.
func TestScriptsUnderOutsideARepositoryIsAnError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	// A GIT_CEILING_DIRECTORIES at the temp dir's parent keeps git from
	// finding a repository the temp dir happens to sit inside.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	_, err := scriptsUnder(context.Background(), dir)
	if !errors.Is(err, gitdiff.ErrNoRepository) {
		t.Errorf("err = %v, want %v", err, gitdiff.ErrNoRepository)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
