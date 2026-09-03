package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// reviewRepo commits baseFiles as the base revision, then headFiles on top,
// and returns the repo directory and the base SHA.
func reviewRepo(t *testing.T, baseFiles, headFiles map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write(baseFiles)
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write(headFiles)
	run("add", "-A")
	run("commit", "-m", "head")
	return dir, base
}

func runReview(t *testing.T, dir, base string, extra ...string) (string, error) {
	t.Helper()
	cmd := newReviewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--dir", dir, "--base", base, "--no-color"}, extra...))
	err := cmd.Execute()
	return out.String(), err
}

// The exemption set is read from the base commit, never from the branch. A
// change that widens the gate must get no benefit from its own widening —
// otherwise one pull request can declare itself exempt, which is the entire
// attack this ordering removes.
func TestReviewReadsExemptionsFromTheBaseNotTheBranch(t *testing.T) {
	selfServing := "exemptions:\n  - name: anything\n    reason: it is fine, trust me\n    paths: [\"**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{referral.FileName: selfServing, "src/auth.go": "package src"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a branch that exempts itself must not pass:\n%s", out)
	}
	// The isolation gate fires first here, because a branch writing itself
	// an exemption alongside the code it wants exempted is exactly the shape
	// isolation refuses. What this pins either way is that the branch's own
	// file never granted anything.
	if strings.Contains(out, "exempt:") {
		t.Errorf("the branch's own exemption file must never grant a pass:\n%s", out)
	}
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("expected a non-zero verdict, got %v", err)
	}
}

// With the exemption in place at the base, the same shape of change goes
// through unattended.
func TestReviewPassesWhenTheBaseDeclaresAMatchingExemption(t *testing.T) {
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("expected an unattended pass, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "exempt: readme-only") {
		t.Errorf("a pass must name the declaration that allowed it, got:\n%s", out)
	}
}

// Day one: no file at all. Every change is referred, and the report says why
// rather than leaving the reader to guess that the feature is broken.
func TestReviewWithNoExemptionsFileRefersAndSaysSo(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("with nothing declared, everything is referred:\n%s", out)
	}
	if !strings.Contains(out, "no exemptions declared") {
		t.Errorf("expected the report to name the empty set, got:\n%s", out)
	}
}

// A disqualifier vetoes a match, and the report distinguishes that from
// "nothing matched" — the two have different remedies, and only one of them
// is a remedy the author can apply.
func TestReviewDisqualifierVetoesAMatchAndNamesTheEvidence(t *testing.T) {
	exemptions := "exemptions:\n  - name: go-source\n    reason: ordinary source edits\n    paths: [\"src/**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "src/a.go": "package src\n"},
		map[string]string{"src/a.go": "package src\n\nvar x = eval() // #nosec G204\n"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a net-new suppression must veto the match:\n%s", out)
	}
	if !strings.Contains(out, "suppression added") || !strings.Contains(out, "src/a.go") {
		t.Errorf("expected the veto to name its evidence, got:\n%s", out)
	}
	if !strings.Contains(out, "go-source matched, then disqualified") {
		t.Errorf("expected the report to distinguish a vetoed match from no match, got:\n%s", out)
	}
}

func TestReviewJSONCarriesTheVerdict(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, _ := runReview(t, dir, base, "--json")
	var got struct {
		Command string `json:"command"`
		Verdict string `json:"verdict"`
		Exit    int    `json:"exit"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the machine report must be valid JSON: %v\n%s", err, out)
	}
	if got.Command != "review" || got.Verdict != "refer" || got.Exit != 2 {
		t.Errorf("got %+v, want review/refer/2", got)
	}
}

// The verdict is computed from HEAD so it matches the one CI will reach.
// Silently deciding on HEAD while the developer is looking at edited files is
// the one way this command gives a confidently wrong answer, so it says so.
func TestReviewWarnsWhenTheWorkingTreeIsDirty(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)
	if err := os.WriteFile(filepath.Join(dir, "src.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := runReview(t, dir, base)
	if !strings.Contains(out, "uncommitted changes") {
		t.Errorf("expected the report to exclude and name uncommitted work, got:\n%s", out)
	}
}

// A base that does not name a commit is refused rather than passed to git.
//
// Each of these turns the gate into a rubber stamp: an empty base makes the
// range read "..HEAD", which is an empty diff that nothing can disqualify,
// and makes the exemptions spec read ":<path>", which git resolves against
// the index — handing the branch the allowlist the merge-base read exists to
// deny it. A base beginning with "-" lands where git reads an option.
func TestReviewRefusesABaseThatIsNotACommit(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, base := range []string{"", "--output=/tmp/lydite-should-not-exist", "refs/heads/nope"} {
		t.Run(base, func(t *testing.T) {
			out, err := runReview(t, dir, base)
			if err == nil {
				t.Fatalf("base %q must be refused, got a verdict:\n%s", base, out)
			}
			var exit ui.ExitError
			if errors.As(err, &exit) {
				t.Fatalf("base %q produced a verdict (exit %d) rather than an error", base, exit.Code)
			}
		})
	}
	if _, err := os.Stat("/tmp/lydite-should-not-exist"); err == nil {
		t.Fatal("a base beginning with \"-\" reached git as an option and wrote a file")
	}
}

// The diff has to describe what this branch introduces, so a base off the
// branch's own history is refused.
func TestReviewRefusesABaseThatIsNotAnAncestor(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	run("checkout", "-q", "-b", "sibling", "HEAD~1")
	if err := os.WriteFile(filepath.Join(dir, "other.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "sibling")
	sibling := run("rev-parse", "HEAD")
	run("checkout", "-q", "main")

	if out, err := runReview(t, dir, sibling); err == nil {
		t.Fatalf("a base off this branch's history must be refused:\n%s", out)
	}
}

// --base defaults to "auto", which is what every CI invocation uses, so the
// merge-base path needs exercising and not just the explicit-SHA one.
func TestReviewAutoResolvesTheMergeBase(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	ctx := context.Background()
	origin := t.TempDir()
	// origin/main carries the base commit only, so HEAD is genuinely ahead
	// of it and the merge-base is something to gate against. Pushing HEAD
	// would make the merge-base HEAD itself, which is a run on main — a
	// different case, and the one that legitimately passes.
	for _, args := range [][]string{
		{"init", "--bare", "-b", "main", origin},
		{"remote", "add", "origin", origin},
		{"push", "-q", "origin", "HEAD~1:refs/heads/main"},
	} {
		if r := executil.RunQuiet(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}

	out, err := runReview(t, dir, "auto")
	if err == nil {
		t.Fatalf("an unexempt change must be referred:\n%s", out)
	}
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("--base auto must reach a referral verdict, got %v:\n%s", err, out)
	}
}

// An unresolvable "auto" is an error rather than a silent fallback: guessing
// a base would quietly change which paths the verdict was computed from.
func TestReviewAutoWithNoOriginIsAnError(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)
	out, err := runReview(t, dir, "auto")
	if err == nil {
		t.Fatalf("an unresolvable auto must error, got a verdict:\n%s", out)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("an unresolvable auto produced a verdict (exit %d) rather than an error", exit.Code)
	}
}

// A referral on a large change names a few examples rather than every path:
// a verdict a reader has to scroll past hundreds of lines to reach is one
// they stop reading.
func TestCappedTruncatesWithACount(t *testing.T) {
	var items []string
	for i := 0; i < listCap+5; i++ {
		items = append(items, fmt.Sprintf("path/%d.go", i))
	}
	got := capped(items)
	if len(got) != listCap+1 {
		t.Fatalf("got %d entries, want %d plus a tail", len(got), listCap)
	}
	if got[len(got)-1] != "…and 5 more" {
		t.Errorf("tail = %q, want a count of what is not shown", got[len(got)-1])
	}
	// The three-index slice keeps the tail out of the caller's backing
	// array, so a second call cannot see the first call's tail.
	if items[listCap] != fmt.Sprintf("path/%d.go", listCap) {
		t.Errorf("capped overwrote its input: %q", items[listCap])
	}
	if short := []string{"a", "b"}; len(capped(short)) != 2 {
		t.Errorf("a list within the cap must pass through unchanged")
	}
}

// Bundling a widening of the exemption set into a larger change fails,
// rather than being referred: splitting the pull request is work the author
// can do, which is what makes this a gate and not a referral.
func TestReviewFailsWhenAnExemptionChangeIsBundled(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{
			referral.FileName: "exemptions:\n  - name: wide\n    reason: r\n    paths: [\"**\"]\n",
			"src/app.go":      "package src",
		},
	)

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("a bundled exemption change must fail (exit 1), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "not isolated") || !strings.Contains(out, "src/app.go") {
		t.Errorf("the failure must name what rode along, got:\n%s", out)
	}
}

// On its own it is the isolated change the rule asks for, so it is referred
// for a human to read — not failed.
func TestReviewRefersAnIsolatedExemptionChange(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{referral.FileName: "exemptions:\n  - name: wide\n    reason: r\n    paths: [\"**\"]\n"},
	)

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("an isolated exemption change must be referred (exit 2), got %v:\n%s", err, out)
	}
	if strings.Contains(out, "not isolated") {
		t.Errorf("an isolated change must not report a bundling failure:\n%s", out)
	}
}

// An exemptions file that exists at the base and cannot be read is not the
// same as one that is absent. Collapsing them is safe only while the
// allowlist is empty, because the safe answer happens to coincide.
func TestReviewErrorsWhenTheExemptionsFileCannotBeRead(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello", referral.FileName: "exemptions: [not-a-list-of-maps]\n"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("an unparseable exemptions file must be an error, got a verdict:\n%s", out)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("an unreadable allowlist produced a verdict (exit %d) rather than an error", exit.Code)
	}
}
