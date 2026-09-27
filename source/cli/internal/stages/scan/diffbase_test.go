package scanstages

import (
	"context"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/semgrep"
)

// gitRepoWithOneCommit is a repository on main holding one commit, and that
// commit's SHA.
func gitRepoWithOneCommit(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(context.Background(), repo, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
	git("init", "-b", "main", ".")
	write(t, repo, "a.txt", "one\n")
	git("add", "a.txt")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "one")
	head := strings.TrimSpace(executil.RunQuiet(context.Background(), repo, "git", "rev-parse", "HEAD").Output)
	return repo, head
}

// The "auto" path needs an origin to resolve against, so it is left to the
// integration surface; what is pinned here is that nothing reaches git as the
// caller wrote it.
//
// A SEMGREP_APP_TOKEN does not short-circuit this. The base has a second
// reader — a finding's anchor, which decides whether a claim becomes a review
// thread — and a token says only that `semgrep ci` scopes itself.
func TestResolveDiffBase(t *testing.T) {
	repo, head := gitRepoWithOneCommit(t)
	resolve := func(diffBase string) (string, error) {
		out, err := ResolveDiffBase(context.Background(), ResolveDiffBaseIn{Dir: repo, DiffBase: diffBase})
		return out.SHA, err
	}

	t.Run("unset means scan everything", func(t *testing.T) {
		got, err := resolve("")
		if err != nil || got != "" {
			t.Errorf("ResolveDiffBase(\"\") = %q, %v, want \"\", nil", got, err)
		}
	})

	t.Run("a ref resolves to the commit it names", func(t *testing.T) {
		got, err := resolve("main")
		if err != nil {
			t.Fatalf("ResolveDiffBase(\"main\") returned %v", err)
		}
		if got != head {
			t.Errorf("ResolveDiffBase(\"main\") = %q, want the SHA %q — a tool must be given the commit, not the caller's string", got, head)
		}
	})

	t.Run("a token does not take the base away — the anchor reads it too", func(t *testing.T) {
		t.Setenv(semgrep.AppTokenEnv, "tok")
		got, err := resolve("main")
		if err != nil || got != head {
			t.Errorf("ResolveDiffBase(\"main\") = %q, %v, want the SHA", got, err)
		}
	})

	// The anchor hands this base to `git diff <base>..HEAD`, where a value
	// beginning with `-` is a position git parses as an option:
	// `--diff-base --output=/tmp/x` would make git write the diff to a path of
	// the caller's choosing.
	t.Run("an option-shaped base is refused rather than handed to git", func(t *testing.T) {
		for _, base := range []string{"--output=/tmp/pwned", "-x", "--upload-pack=touch /tmp/pwned"} {
			got, err := resolve(base)
			if err == nil {
				t.Errorf("ResolveDiffBase(%q) = %q, want an error", base, got)
			}
			// No base alongside the error. A caller that reads the value
			// before the error must scan everything rather than diff against
			// a string git refused.
			if got != "" {
				t.Errorf("ResolveDiffBase(%q) = %q beside its error, want no base", base, got)
			}
		}
	})

	t.Run("a ref that names no commit is refused", func(t *testing.T) {
		got, err := resolve("no/such/ref")
		if err == nil {
			t.Error("ResolveDiffBase accepted a ref that names no commit")
		}
		if got != "" {
			t.Errorf("ResolveDiffBase = %q beside its error, want no base", got)
		}
		if want := `--diff-base "no/such/ref" does not name a commit`; err != nil && err.Error() != want {
			t.Errorf("ResolveDiffBase error = %q, want %q", err, want)
		}
	})
}

// With no base there is no change to read, and git is never asked: the
// directory here is not a repository, so asking would fail.
func TestReadChangedLinesWithNoBaseIsEmpty(t *testing.T) {
	out, err := ReadChangedLines(context.Background(), ReadChangedLinesIn{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("ReadChangedLines with no base: %v", err)
	}
	if len(out.Changed) != 0 {
		t.Errorf("ReadChangedLines with no base = %v, want nothing changed", out.Changed)
	}
}

func TestReadChangedLinesReadsTheChangeSinceTheBase(t *testing.T) {
	repo, head := gitRepoWithOneCommit(t)
	write(t, repo, "a.txt", "one\ntwo\nthree\n")
	if r := executil.RunQuiet(context.Background(), repo, "git",
		"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-am", "two"); !r.Ok() {
		t.Fatalf("git commit: %v\n%s", r.Err, r.Stderr)
	}
	out, err := ReadChangedLines(context.Background(), ReadChangedLinesIn{Dir: repo, BaseSHA: head})
	if err != nil {
		t.Fatalf("ReadChangedLines: %v", err)
	}
	if got := out.Changed["a.txt"]; !slices.Equal(got, []int{2, 3}) {
		t.Errorf("ReadChangedLines[a.txt] = %v, want [2 3] — the lines added since the base", got)
	}
}

func TestReadChangedLinesRefusesABaseGitCannotDiffAgainst(t *testing.T) {
	repo, _ := gitRepoWithOneCommit(t)
	if _, err := ReadChangedLines(context.Background(), ReadChangedLinesIn{Dir: repo, BaseSHA: strings.Repeat("0", 40)}); err == nil {
		t.Error("ReadChangedLines read a change against a commit that does not exist")
	}
}

// `semgrep ci` derives its own diff base from the CI environment, so passing
// --baseline-commit on top of that is redundant. The rule is Semgrep's alone,
// and lives where Semgrep is invoked rather than where the base is resolved.
func TestSemgrepBase(t *testing.T) {
	cases := []struct {
		name, base, want string
		appTokenSet      bool
	}{
		{"no token: Semgrep gets the base", "origin/release", "origin/release", false},
		{"a token: semgrep ci scopes itself", "origin/release", "", true},
		{"no base to give", "", "", false},
		{"no base and a token", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := semgrepBase(tc.base, tc.appTokenSet); got != tc.want {
				t.Errorf("semgrepBase(%q, %v) = %q, want %q", tc.base, tc.appTokenSet, got, tc.want)
			}
		})
	}
}
