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

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// reviewRepo commits baseFiles as the base revision, then headFiles on top,
// and returns the repo directory and the base SHA.
func reviewRepo(t *testing.T, baseFiles, headFiles map[string]string) (string, string) {
	t.Helper()
	return reviewRepoSaying(t, baseFiles, headFiles, "head")
}

// reviewRepoSaying is reviewRepo with the head commit's message spelled out,
// which is one of the two places a breaking change is declared.
func reviewRepoSaying(t *testing.T, baseFiles, headFiles map[string]string, message string) (string, string) {
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
	run("commit", "-m", message)
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

// An uncovered path and an unsatisfied condition are different things to act
// on, and the referral must say which happened: a declared exemption that
// simply does not cover the change must never be reported as one that
// covered it and then failed its condition.
func TestReviewNamesUncoveredPathsRatherThanAnUnsatisfiedCondition(t *testing.T) {
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a path no exemption covers must refer:\n%s", out)
	}
	if strings.Contains(out, "conditional exemption covers the paths") {
		t.Errorf("nothing declared covers this change, so no exemption's condition went unmet, got:\n%s", out)
	}
	if !strings.Contains(out, "path(s) covered by no exemption") || !strings.Contains(out, "src/auth.go") {
		t.Errorf("the referral must name the uncovered path, got:\n%s", out)
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

// A module whose exported API the gate compares, and the declaration that
// opts it in. `go 1.26` rather than a patch version so the comparison runs
// under whatever Go the machine already has, without provisioning one.
const (
	sdkGoMod = "module example.com/sdk\n\ngo 1.26\n"
	sdkAPI   = "package sdk\n\n// Do runs the thing.\nfunc Do(n int) error { return nil }\n"
	sdkOptIn = "components:\n  - name: sdk\n    dir: sdk\n    runner: go-test\n    api_surface: {}\n"
	sdkPlain = "components:\n  - name: sdk\n    dir: sdk\n    runner: go-test\n"
)

func sdkBase(componentsYML string) map[string]string {
	return map[string]string{
		"README.md":        "hello",
		component.FileName: componentsYML,
		"sdk/go.mod":       sdkGoMod,
		"sdk/api.go":       sdkAPI,
		referral.FileName:  "exemptions:\n  - name: sdk-source\n    reason: ordinary source edits\n    paths: [\"sdk/**\"]\n",
	}
}

// An undeclared break is a gate, not a referral: the author clears it by
// restoring the API or by declaring the break, and both are work they can do.
func TestReviewFailsAnUndeclaredAPIBreak(t *testing.T) {
	dir, base := reviewRepo(t, sdkBase(sdkOptIn),
		map[string]string{"sdk/api.go": "package sdk\n"})

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("an undeclared break must fail (exit 1), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "undeclared") || !strings.Contains(out, "sdk/api.go") {
		t.Errorf("the failure must name the break and where it was, got:\n%s", out)
	}
}

// The same break, declared, reaches a person instead: every breaking change
// should, and the declaration decides which verdict a break gets rather than
// whether it is reported at all.
func TestReviewRefersADeclaredAPIBreak(t *testing.T) {
	dir, base := reviewRepoSaying(t, sdkBase(sdkOptIn),
		map[string]string{"sdk/api.go": "package sdk\n"},
		"feat(sdk)!: drop Do")

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a declared break must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPIBreakDeclared) {
		t.Errorf("the referral must name the declaration it read, got:\n%s", out)
	}
	if strings.Contains(out, "undeclared") {
		t.Errorf("a declared break must not also fire the gate:\n%s", out)
	}
	// The disqualification row above is the verdict; this one is what a
	// reader actually reviews — the break itself, not the claim that there
	// is one. Removing it would leave the report saying something broke
	// without ever saying what.
	if !strings.Contains(out, gateAPISurface+"(sdk)") || !strings.Contains(out, ", declared") {
		t.Errorf("the per-component row naming the declared break is missing, got:\n%s", out)
	}
	if !strings.Contains(out, "merge-base sdk/api.go") {
		t.Errorf("a removed symbol must be located at its merge-base declaration, got:\n%s", out)
	}
}

// A symbol that changed but was not removed is located in the head's own
// tree, never the merge-base's — a reader who opens the path this names
// must find the line that means what the report says, and the merge-base's
// copy is not that line for anything still there.
func TestReviewLocatesAChangedSymbolAtHead(t *testing.T) {
	dir, base := reviewRepoSaying(t, sdkBase(sdkOptIn),
		map[string]string{"sdk/api.go": "package sdk\n\n// Do runs the thing.\nfunc Do(n int, extra string) error { return nil }\n"},
		"feat(sdk)!: widen Do's signature")

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a declared break must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "sdk/api.go:4") {
		t.Errorf("a changed-but-present symbol must be located in the head tree, got:\n%s", out)
	}
	if strings.Contains(out, "merge-base sdk/api.go") {
		t.Errorf("a symbol still in head must not be located at the merge-base, got:\n%s", out)
	}
}

// Ordinary growth is not a break. An added function is a compatible change,
// and a gate that fired on one would fire on every release.
func TestReviewPassesAnAdditiveAPIChange(t *testing.T) {
	dir, base := reviewRepo(t, sdkBase(sdkOptIn),
		map[string]string{"sdk/api.go": sdkAPI + "\n// Also runs the thing.\nfunc Also() {}\n"})

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("an additive change must merge unattended, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "no incompatible change") {
		t.Errorf("a compared component must say it was compared, got:\n%s", out)
	}
}

// Nil means not measured. A component that did not ask has its API compared
// by nothing, and nothing is said about it — the comparison costs a worktree
// and a toolchain, and most components are binaries nobody imports.
func TestReviewComparesNothingForAComponentThatDidNotOptIn(t *testing.T) {
	dir, base := reviewRepo(t, sdkBase(sdkPlain),
		map[string]string{"sdk/api.go": "package sdk\n"})

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("a component that did not opt in must not be gated, got %v:\n%s", err, out)
	}
	if strings.Contains(out, gateAPISurface) {
		t.Errorf("nothing asked for a comparison, so nothing may be reported about one:\n%s", out)
	}
}

// Neither pass nor fail is true of a surface that could not be computed.
// Failing is a gate the author cannot clear, since the merge-base's tree is
// not theirs to fix, and passing is a gate that could not run rendering as
// one that ran and found nothing.
func TestReviewRefersASurfaceItCouldNotCompare(t *testing.T) {
	base := sdkBase(sdkOptIn)
	base["sdk/api.go"] = "package sdk\n\nfunc Do(n int) error { return \n"
	dir, baseSHA := reviewRepo(t, base,
		map[string]string{"sdk/api.go": sdkAPI})

	out, err := runReview(t, dir, baseSHA)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("an uncomputable surface must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPISurfaceUncomputable) || !strings.Contains(out, "sdk") {
		t.Errorf("the referral must name the component and why, got:\n%s", out)
	}
}

// A major bump is spelled as a new module path, so every package reads as
// removed and added again. The range is out of scope and the report says so,
// rather than reporting an entire module removed.
func TestReviewRefersAModulePathThatMoved(t *testing.T) {
	dir, base := reviewRepo(t, sdkBase(sdkOptIn),
		map[string]string{"sdk/go.mod": "module example.com/sdk/v2\n\ngo 1.26\n"})

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a moved module path must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "module path changed") {
		t.Errorf("the referral must name the module path as the reason, got:\n%s", out)
	}
}

// The declaration is honoured on its own, with no corroboration from a
// surface diff and in a repository where no component opted in at all. That
// is the one-way ratchet the marker is only safe under: the claim adds a
// referral and can never remove one, so a change an exemption covers is
// referred the moment its author says it breaks something.
func TestReviewRefersADeclarationWithNothingCompared(t *testing.T) {
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	dir, base := reviewRepoSaying(t,
		map[string]string{referral.FileName: exemptions, "README.md": "hello"},
		map[string]string{"README.md": "hello again"},
		"docs: reword\n\nBREAKING CHANGE: the wording is load-bearing\n")

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a declared break must be referred even with nothing compared (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPIBreakDeclared) {
		t.Errorf("the referral must name the declaration, got:\n%s", out)
	}
}

// d.Empty alone is not "nothing to report": the API-surface check can refer a
// change from its commit messages with no path in the diff at all — an empty
// commit, or (since the edited trigger re-runs the whole check) a title
// edited after the last push with no new commit either. The summary row must
// not read as a pass next to the refer row the declaration produced.
func TestReviewRefersADeclaredBreakEvenWithAnEmptyDiff(t *testing.T) {
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
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, referral.FileName)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, referral.FileName), []byte(exemptions), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	// No path changes at all — an empty commit, carrying only the declaration.
	run("commit", "--allow-empty", "-m", "feat!: nothing changed, but say so anyway")

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a declared break must be referred even with an empty diff (exit 2), got %v:\n%s", err, out)
	}
	if strings.Contains(out, "no changes against the base") {
		t.Errorf("the summary must not read as a pass beside the refer row it contradicts, got:\n%s", out)
	}
	// The reason and detail text are their own claims, not just the row's
	// status: naming an exemption outcome ("no exemption matched") would
	// describe a step Decide never ran, since it returns before that loop on
	// an empty diff, and the remedy must not tell the author the declaration
	// is undroppable when they wrote it themselves.
	if !strings.Contains(out, "no path in the diff at all") {
		t.Errorf("the reason must not describe the exemption match, which never ran, got:\n%s", out)
	}
	if strings.Contains(out, "no exemption matched") || strings.Contains(out, "no exemptions declared") {
		t.Errorf("the reason must not claim the exemption match ran, got:\n%s", out)
	}
	if strings.Contains(out, "not annotations a change can drop") {
		t.Errorf("the remedy must not claim the declaration cannot be dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "does not clear a real break") {
		t.Errorf("the remedy must say what dropping the declaration actually does, got:\n%s", out)
	}
}

// Squash merge makes the title the commit that lands, so a break declared
// only there — in commits that are about to be squashed away — still refers.
func TestReviewReadsTheDeclarationFromThePullRequestTitle(t *testing.T) {
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "README.md": "hello"},
		map[string]string{"README.md": "hello again"})
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"number":7,"pull_request":{"title":"refactor!: move the thing"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runReview(t, dir, base, "--event", event)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a title-declared break must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "the pull request title") {
		t.Errorf("the referral must name the source it read, got:\n%s", out)
	}
}

// A malformed .lydite/components.yml is review's to refuse, the same way
// scan and test already do — silently ignoring it would mean api_surface
// opted a component in without anyone knowing whether the file was even
// read.
func TestReviewErrorsOnAMalformedComponentsFile(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName:  "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n",
			component.FileName: "components:\n  - name: sdk\n    dir: sdk\n    runner: go-test\n    not_a_real_key: true\n",
			"README.md":        "hello",
		},
		map[string]string{"README.md": "hello again"})

	_, err := runReview(t, dir, base)
	if err == nil {
		t.Fatal("a malformed components.yml must fail the run, got nil")
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Errorf("a load error is not a verdict and must not carry an ExitError, got %v", err)
	}
}

// A malformed .lydite/config.yml is only reached once a component opts in —
// api_surface is the first thing in review that needs it at all.
func TestReviewErrorsOnAMalformedConfigFile(t *testing.T) {
	dir, base := reviewRepo(t, sdkBase(sdkOptIn),
		map[string]string{
			"sdk/api.go":    sdkAPI,
			config.FileName: "coverage:\n  tolerance: -1\n",
		})

	_, err := runReview(t, dir, base)
	if err == nil {
		t.Fatal("a malformed config.yml must fail the run once a component opts in, got nil")
	}
}

// A payload that cannot be read is warned about and treated as no title,
// never fatal — the title can only add a referral, so failing the whole run
// over an unreadable one would turn an additive source into a blocker.
func TestPullRequestTitleWarnsOnAMalformedEvent(t *testing.T) {
	var warn bytes.Buffer
	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	if got := pullRequestTitle(&warn, missing); got != "" {
		t.Errorf("pullRequestTitle(missing) = %q, want empty", got)
	}
	if !strings.Contains(warn.String(), "warning:") {
		t.Errorf("a missing event file must be warned about, got %q", warn.String())
	}

	warn.Reset()
	malformed := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(malformed, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := pullRequestTitle(&warn, malformed); got != "" {
		t.Errorf("pullRequestTitle(malformed) = %q, want empty", got)
	}
	if !strings.Contains(warn.String(), "warning:") {
		t.Errorf("a malformed event file must be warned about, got %q", warn.String())
	}
}

// A finding the loader could not place carries no path, and locate must
// report its message on its own rather than joining an empty path onto the
// component's directory, which would point at the directory as though it
// were the file.
func TestLocateReportsAnUnplacedFindingsMessageAlone(t *testing.T) {
	got := locate([]finding.Finding{{Message: "Removed: removed"}}, "sdk")
	if len(got) != 1 || got[0] != "Removed: removed" {
		t.Errorf("locate = %v, want the bare message", got)
	}
}

// A directory that is not a git repository at all cannot be entered at any
// prefix, so the failure is reported before a worktree is ever attempted.
func TestBaseWorktreeFailsOutsideAGitRepository(t *testing.T) {
	root, _, err := baseWorktree(context.Background(), t.TempDir(), "HEAD")
	if err == nil {
		t.Fatal("baseWorktree over a non-repository directory must fail")
	}
	if root != "" {
		t.Errorf("root = %q on error, want empty — a caller must not act on it", root)
	}
}

// A base that resolves the repository but names no real commit fails at the
// worktree checkout itself, and the temp directory it made is cleaned up
// rather than left behind.
func TestBaseWorktreeFailsOnAnUnknownCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "one")

	root, _, err := baseWorktree(ctx, dir, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("baseWorktree over an unknown commit must fail")
	}
	if root != "" {
		t.Errorf("root = %q on error, want empty — a caller must not act on it", root)
	}
}

// goSum renders a go.sum pinning each module at the version given, both the
// module line and the go.mod line git would carry for it.
func goSum(modules map[string]string) string {
	var b strings.Builder
	for name, version := range modules {
		fmt.Fprintf(&b, "%s %s h1:aaaa=\n%s %s/go.mod h1:bbbb=\n", name, version, name, version)
	}
	return b.String()
}

// dependencyExemption covers the manifest paths outright, so what the run
// reports about them is this check's verdict and not the absence of a
// declaration.
func dependencyExemption(paths ...string) string {
	return "exemptions:\n  - name: dependency-maintenance\n    reason: lockfile maintenance\n    paths: [\"" +
		strings.Join(paths, "\", \"") + "\"]\n"
}

// A package at HEAD the merge-base did not pin refers, and the evidence names
// it: the gap an advisory database leaves is code nobody has looked at
// entering the tree, which no clean SCA run says anything about.
func TestReviewRefersAnAddedDependency(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: dependencyExemption("go.sum"),
			"go.sum":          goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{
			"github.com/spf13/cobra":         "v1.10.1",
			"github.com/google/licensecheck": "v0.3.1",
		})},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("an added dependency must be referred:\n%s", out)
	}
	if !strings.Contains(out, referral.DisqualificationDependencyAdded) {
		t.Errorf("expected the added-dependency veto, got:\n%s", out)
	}
	if !strings.Contains(out, "github.com/google/licensecheck") {
		t.Errorf("the veto must name the package that arrived, got:\n%s", out)
	}
}

// A version move on a name both sides pin is not an addition. It may still be
// referred by other means; what it is never referred for is this.
func TestReviewDoesNotReferAVersionBumpAsAnAddition(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: dependencyExemption("go.sum"),
			"go.sum":          goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.2"})},
	)

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("a bump of a package already pinned adds nothing: %v\n%s", err, out)
	}
	if strings.Contains(out, referral.DisqualificationDependencyAdded) {
		t.Errorf("a version move is not an addition, got:\n%s", out)
	}
	if !strings.Contains(out, gateDependencies+"(go.sum)") {
		t.Errorf("a manifest that was compared must say so under its own name, got:\n%s", out)
	}
}

// A removal is the opposite of new untrusted code arriving, and carries none
// of the supply-chain risk the veto exists for.
func TestReviewDoesNotReferARemovedDependency(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: dependencyExemption("go.sum"),
			"go.sum": goSum(map[string]string{
				"github.com/spf13/cobra":         "v1.10.1",
				"github.com/google/licensecheck": "v0.3.1",
			}),
		},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"})},
	)

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("dropping a dependency must not be referred: %v\n%s", err, out)
	}
	if strings.Contains(out, referral.DisqualificationDependencyAdded) {
		t.Errorf("a removal is not an addition, got:\n%s", out)
	}
}

// An ecosystem with no reader is a gap the report says out loud. "lydite does
// not know whether this change added a dependency" must not reach the verdict
// "it did not".
func TestReviewRefersAManifestItCannotRead(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: dependencyExemption("yarn.lock"),
			"yarn.lock":       "# yarn lockfile v1\nleft-pad@^1.0.0:\n  version \"1.0.0\"\n",
		},
		map[string]string{"yarn.lock": "# yarn lockfile v1\nleft-pad@^1.0.0:\n  version \"1.0.1\"\n"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a manifest nothing here parses must be referred:\n%s", out)
	}
	if !strings.Contains(out, referral.DisqualificationDependencyDeltaUnmeasured) {
		t.Errorf("expected the unmeasured-delta veto, got:\n%s", out)
	}
	if !strings.Contains(out, "yarn") {
		t.Errorf("the veto must name the ecosystem that went unread, got:\n%s", out)
	}
}

// A manifest the merge-base does not have pins nothing there, so every
// package in it is arriving with this change.
func TestReviewRefersEveryPackageInANewManifest(t *testing.T) {
	cargoLock := "version = 4\n\n[[package]]\nname = \"anstyle\"\nversion = \"1.0.13\"\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: dependencyExemption("Cargo.lock")},
		map[string]string{"Cargo.lock": cargoLock},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a manifest with no base-side content adds everything in it:\n%s", out)
	}
	if !strings.Contains(out, referral.DisqualificationDependencyAdded) || !strings.Contains(out, "anstyle") {
		t.Errorf("expected every package in the new manifest to be named, got:\n%s", out)
	}
}

// package-lock.json is the one manifest whose reader cannot tolerate empty
// content the way go.mod, go.sum and Cargo.lock can: json.Unmarshal on nil or
// empty bytes is an error, not an empty document. A manifest the merge-base
// does not have must therefore never reach that reader at all — the absent
// path has to short-circuit to the empty set before Extract is ever called,
// and this is the one case that tells "short-circuited" apart from "called
// with nil content".
func TestReviewRefersEveryPackageInANewNPMManifest(t *testing.T) {
	lock := `{"packages":{"node_modules/left-pad":{"version":"1.3.0","resolved":"https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"}}}`
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: dependencyExemption("package-lock.json")},
		map[string]string{"package-lock.json": lock},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a manifest with no base-side content adds everything in it:\n%s", out)
	}
	if !strings.Contains(out, referral.DisqualificationDependencyAdded) || !strings.Contains(out, "left-pad") {
		t.Errorf("expected every package in the new manifest to be named, not reported as unmeasured, got:\n%s", out)
	}
}

// A path that is no manifest is compared over by nothing, and the report says
// nothing about it.
func TestReviewComparesNoDependenciesForAPathThatIsNoManifest(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: dependencyExemption("README.md"), "README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("expected an unattended pass, got %v:\n%s", err, out)
	}
	if strings.Contains(out, gateDependencies+"(") {
		t.Errorf("no manifest was touched, so no manifest row belongs in the report:\n%s", out)
	}
}

// bumpExemption covers the manifest paths on the condition ADR 0047 defines:
// every version moves by a patch or a minor, with the licence and SCA rows
// passing.
func bumpExemption(paths ...string) string {
	return "exemptions:\n  - name: dependency-bump\n    reason: routine version maintenance, no new dependency\n" +
		"    versions: " + referral.VersionsPatchAndMinor + "\n    paths: [\"" +
		strings.Join(paths, "\", \"") + "\"]\n"
}

// scanReports writes a report directory holding a scan document whose rows
// carry the statuses given, keyed by label.
func scanReports(t *testing.T, rows map[string]ui.Status) string {
	t.Helper()
	dir := t.TempDir()
	type jsonRow struct {
		Status ui.Status `json:"status"`
		Label  string    `json:"label"`
	}
	doc := struct {
		Command string    `json:"command"`
		Verdict string    `json:"verdict"`
		Rows    []jsonRow `json:"rows"`
	}{Command: "scan", Verdict: "pass"}
	for label, status := range rows {
		doc.Rows = append(doc.Rows, jsonRow{Status: status, Label: label})
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, documentName("scan")), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// cleanScan is a Go component whose licence gate and advisory check both ran
// and passed — the evidence a version-bump exemption's condition asks for.
func cleanScan(t *testing.T) string {
	t.Helper()
	return scanReports(t, map[string]ui.Status{
		"gosec(cli)":       ui.StatusPass,
		"govulncheck(cli)": ui.StatusPass,
		"licence(cli)":     ui.StatusPass,
	})
}

// bumpRepo is a patch bump of one module already pinned, under an exemption
// conditioned on patch-and-minor.
func bumpRepo(t *testing.T) (string, string) {
	t.Helper()
	return reviewRepo(t,
		map[string]string{
			referral.FileName: bumpExemption("go.sum"),
			"go.sum":          goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.2"})},
	)
}

// The whole condition met: every version moved by a patch, and the licence and
// SCA rows for every component ran and passed in the scan document.
func TestReviewExemptsAPatchBumpWithPassingLicenceAndSCARows(t *testing.T) {
	dir, base := bumpRepo(t)

	out, err := runReview(t, dir, base, "--reports", cleanScan(t))
	if err != nil {
		t.Fatalf("expected an unattended pass, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "exempt: dependency-bump") {
		t.Errorf("a pass must name the declaration that allowed it, got:\n%s", out)
	}
}

// Without the evidence the condition cannot be met, so every local run — and
// any CI job that does not pass the flag — refers the same change.
func TestReviewRefersAVersionBumpWithNoReports(t *testing.T) {
	dir, base := bumpRepo(t)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a condition with no evidence behind it must refer:\n%s", out)
	}
	if !strings.Contains(out, "dependency-bump covers these paths") {
		t.Errorf("the referral must name the exemption whose condition went unmet, got:\n%s", out)
	}
	if strings.Contains(out, "exempt:") {
		t.Errorf("an unmet condition grants nothing, got:\n%s", out)
	}
}

// A clean SCA run says something about advisories and nothing about licences,
// and the bump that introduces a copyleft dependency is precisely a
// lockfile-only change with a clean SCA run.
func TestReviewRefersAVersionBumpWhoseLicenceGateFailed(t *testing.T) {
	dir, base := bumpRepo(t)
	reports := scanReports(t, map[string]ui.Status{
		"gosec(cli)":       ui.StatusPass,
		"govulncheck(cli)": ui.StatusPass,
		"licence(cli)":     ui.StatusFail,
	})

	out, err := runReview(t, dir, base, "--reports", reports)
	if err == nil {
		t.Fatalf("a failing licence gate must not satisfy the condition:\n%s", out)
	}
	if strings.Contains(out, "exempt:") {
		t.Errorf("an unmet condition grants nothing, got:\n%s", out)
	}
}

// A component the scan document names with no licence row at all is a
// component nothing measured, which must not read as one that passed.
func TestReviewRefersAVersionBumpWithNoLicenceRowForAComponent(t *testing.T) {
	dir, base := bumpRepo(t)
	reports := scanReports(t, map[string]ui.Status{
		"gosec(cli)":       ui.StatusPass,
		"govulncheck(cli)": ui.StatusPass,
		"licence(cli)":     ui.StatusPass,
		"biome(web)":       ui.StatusPass,
	})

	out, err := runReview(t, dir, base, "--reports", reports)
	if err == nil {
		t.Fatalf("a component with no licence row must not satisfy the condition:\n%s", out)
	}
}

// A component whose advisory check is missing from the document is evidence
// nobody took, and the condition asks for evidence.
func TestReviewRefersAVersionBumpWithNoAdvisoryRowForAComponent(t *testing.T) {
	dir, base := bumpRepo(t)
	reports := scanReports(t, map[string]ui.Status{
		"gosec(cli)":   ui.StatusPass,
		"licence(cli)": ui.StatusPass,
	})

	out, err := runReview(t, dir, base, "--reports", reports)
	if err == nil {
		t.Fatalf("a Go component with no govulncheck row must not satisfy the condition:\n%s", out)
	}
}

// cliComponent declares one Go component named cli, rooted at the repository
// root, so a run can be held to needing its licence and advisory rows even
// where the scan document names it for nothing at all.
const cliComponent = "components:\n  - name: cli\n    dir: .\n    runner: go-test\n"

// A component switched off in .lydite/config.yml, or a scan that never ran,
// carries no row of any kind — and that must not read the same as one whose
// rows all passed.
func TestReviewRefersAVersionBumpWithADeclaredComponentMissingFromTheScan(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName:  bumpExemption("go.sum"),
			component.FileName: cliComponent,
			"go.sum":           goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.2"})},
	)
	reports := scanReports(t, map[string]ui.Status{})

	out, err := runReview(t, dir, base, "--reports", reports)
	if err == nil {
		t.Fatalf("a declared component absent from the scan must not satisfy the condition:\n%s", out)
	}
}

// A declared Go component's advisory row can be missing even where nothing
// in the document hints at its language at all — no gosec row to infer from
// — and the declaration is what has to catch it.
func TestReviewRefersAVersionBumpWithADeclaredGoComponentMissingItsAdvisoryRow(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName:  bumpExemption("go.sum"),
			component.FileName: cliComponent,
			"go.sum":           goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.2"})},
	)
	reports := scanReports(t, map[string]ui.Status{"licence(cli)": ui.StatusPass})

	out, err := runReview(t, dir, base, "--reports", reports)
	if err == nil {
		t.Fatalf("a declared Go component with a passing licence row and no advisory row must not satisfy the condition:\n%s", out)
	}
}

// A components.yml that will not parse is review's to refuse elsewhere, but
// dependencyGatesPassed must fail its own half closed rather than let a load
// error read as evidence.
func TestDependencyGatesPassedFailsClosedOnAnUnreadableComponentsFile(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{
			component.FileName: "components:\n  - name: cli\n    dir: .\n    runner: go-test\n    not_a_real_key: true\n",
			"README.md":        "hello",
		},
	)
	var warn bytes.Buffer
	if dependencyGatesPassed(dir, []string{cleanScan(t)}, &warn) {
		t.Fatal("an unreadable components.yml must fail the condition, not satisfy it")
	}
	if warn.Len() == 0 {
		t.Error("a load that failed should warn, not fail silently")
	}
}

// Two report directories naming one gate keep the worse of the two answers,
// whichever order they are given in: a pass a later job's failure overturns
// must not be forgotten because it was seen first.
func TestDependencyGatesPassedKeepsTheWorseOfTwoReportsForOneGate(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{component.FileName: cliComponent, "README.md": "hello again"},
	)
	passing := scanReports(t, map[string]ui.Status{
		"gosec(cli)":       ui.StatusPass,
		"govulncheck(cli)": ui.StatusPass,
		"licence(cli)":     ui.StatusPass,
	})
	failing := scanReports(t, map[string]ui.Status{"licence(cli)": ui.StatusFail})

	var warn bytes.Buffer
	if dependencyGatesPassed(dir, []string{passing, failing}, &warn) {
		t.Error("a passing report followed by a failing one for the same gate must not satisfy the condition")
	}
}

// A label with no component at all — no "(" at all, or one opening at the
// very first character, which names an empty gate rather than a missing
// component — carries nothing this attributes, and is told apart from a
// well-formed "gate(component)" label.
func TestSplitGateLabel(t *testing.T) {
	cases := []struct {
		label           string
		gate, component string
		ok              bool
	}{
		{"licence(cli)", "licence", "cli", true},
		{"cargo clippy(api)", "cargo clippy", "api", true},
		{"scan", "", "", false},
		{"(cli)", "", "", false},
		{"licence()", "", "", false},
		{"licence(cli", "", "", false},
	}
	for _, tc := range cases {
		gate, component, ok := splitGateLabel(tc.label)
		if gate != tc.gate || component != tc.component || ok != tc.ok {
			t.Errorf("splitGateLabel(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.label, gate, component, ok, tc.gate, tc.component, tc.ok)
		}
	}
}

// A change patching one dependency and majoring another is not boring as a
// whole, whatever the scan says about either.
func TestReviewRefersAMajorBumpWithPassingRows(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: bumpExemption("go.sum"),
			"go.sum": goSum(map[string]string{
				"github.com/spf13/cobra":      "v1.10.1",
				"github.com/go-git/go-git/v5": "v5.11.0",
			}),
		},
		map[string]string{"go.sum": goSum(map[string]string{
			"github.com/spf13/cobra":      "v1.10.2",
			"github.com/go-git/go-git/v5": "v6.0.0",
		})},
	)

	out, err := runReview(t, dir, base, "--reports", cleanScan(t))
	if err == nil {
		t.Fatalf("a major bump must not be covered by a patch-and-minor condition:\n%s", out)
	}
	if strings.Contains(out, "exempt:") {
		t.Errorf("an unmet condition grants nothing, got:\n%s", out)
	}
}

// A disqualifier vetoes any match, condition met or not: an added package is
// new code entering the tree, and no version arithmetic and no clean scan says
// anything about that.
func TestReviewRefersAnAddedDependencyDespiteTheCondition(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{
			referral.FileName: bumpExemption("go.sum"),
			"go.sum":          goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
		},
		map[string]string{"go.sum": goSum(map[string]string{
			"github.com/spf13/cobra":         "v1.10.2",
			"github.com/google/licensecheck": "v0.3.1",
		})},
	)

	out, err := runReview(t, dir, base, "--reports", cleanScan(t))
	if err == nil {
		t.Fatalf("an added dependency must be referred whatever the exemption says:\n%s", out)
	}
	if !strings.Contains(out, referral.DisqualificationDependencyAdded) {
		t.Errorf("expected the added-dependency veto, got:\n%s", out)
	}
}
