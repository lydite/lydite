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

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
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

// A Rust crate whose public API the gate compares, the declaration that opts
// it in, and the config that keeps provisioning out of the run: the comparison
// is the stub below, so nothing here needs a rustup channel and none is
// downloaded to find that out.
const (
	crateCargoToml = "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
	crateAPI       = "pub fn do_thing(n: i32) -> i32 {\n    n\n}\n"
	crateOptIn     = "components:\n  - name: probe\n    dir: probe\n    runner: cargo-nextest\n    api_surface: {}\n"
	cratePlain     = "components:\n  - name: probe\n    dir: probe\n    runner: cargo-nextest\n"
	noProvisioning = "toolchain:\n  enabled: false\n"
)

func crateBase(componentsYML string) map[string]string {
	return map[string]string{
		"README.md":        "hello",
		component.FileName: componentsYML,
		config.FileName:    noProvisioning,
		"probe/Cargo.toml": crateCargoToml,
		"probe/src/lib.rs": crateAPI,
		referral.FileName:  "exemptions:\n  - name: probe-source\n    reason: ordinary source edits\n    paths: [\"probe/**\"]\n",
	}
}

// The three answers cargo-semver-checks gives, as the stub that gives them.
//
// Each is the exit code and the stdout shape internal/rustapisurface reads,
// which that package's own tests tie to runs recorded from the real tool. What
// these stand in for here is the tool, not its output: what is under test is
// the verdict each answer reaches.
const (
	// semverChecksBroken reports one removed function, located in the baseline
	// tree the invocation names — which is what makes the finding a removal,
	// and its line the merge-base's.
	semverChecksBroken = `#!/bin/sh
base=
while [ $# -gt 0 ]; do
  if [ "$1" = "--baseline-root" ]; then base=$2; fi
  shift
done
printf '%s\n' "--- failure function_missing: pub fn removed or renamed ---" "" "Failed in:" "  function probe::do_thing, previously in file $base/src/lib.rs:1"
exit 100
`
	semverChecksUnbroken = "#!/bin/sh\nexit 0\n"
	// semverChecksRefused is the crate with no library target: exit 101, and
	// the tool's own account of why on stderr.
	semverChecksRefused = "#!/bin/sh\necho 'error: no crates with library targets selected, nothing to semver-check' >&2\nexit 101\n"
)

// semverChecksStub puts a stand-in cargo-semver-checks in the version-keyed
// tool cache, so the comparison is a real invocation with nothing installed,
// nothing fetched and no cargo on the machine involved.
func semverChecksStub(t *testing.T, script string) {
	t.Helper()
	// rustup keeps its channels under RUSTUP_HOME, which defaults to a
	// directory beneath $HOME. Read before the home below replaces it: the
	// toolchain probe asks rustup which channel the component resolves to, and
	// pointed at an empty home rustup answers by downloading a whole toolchain.
	rustupHome := os.Getenv("RUSTUP_HOME")
	if rustupHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			rustupHome = filepath.Join(home, ".rustup")
		}
	}
	home := t.TempDir()
	// Both, because os.UserCacheDir reads XDG_CACHE_HOME on Linux and
	// $HOME/Library/Caches on macOS.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	if rustupHome != "" {
		t.Setenv("RUSTUP_HOME", rustupHome)
	}
	bin, err := (cargotool.Tool{Name: "cargo-semver-checks", Version: rust.CargoSemverChecksVersion}).Binary()
	if err != nil {
		t.Fatalf("locating the cached binary: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a stub the test is about to execute
		t.Fatalf("writing the stub: %v", err)
	}
}

// removeSemverChecksStub deletes the stub semverChecksStub installed, so a
// test can prove a later step never runs cargo-semver-checks again: with
// nothing left to execute, a step that tried would fail outright.
func removeSemverChecksStub(t *testing.T) {
	t.Helper()
	bin, err := (cargotool.Tool{Name: "cargo-semver-checks", Version: rust.CargoSemverChecksVersion}).Binary()
	if err != nil {
		t.Fatalf("locating the cached binary: %v", err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatalf("removing the stub: %v", err)
	}
}

// An undeclared break is a gate for a Rust component exactly as it is for a Go
// one: the author clears it by restoring the API or by declaring the break,
// and both are work they can do.
func TestReviewFailsAnUndeclaredRustAPIBreak(t *testing.T) {
	semverChecksStub(t, semverChecksBroken)
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("an undeclared break must fail (exit 1), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "undeclared") || !strings.Contains(out, "probe/src/lib.rs") {
		t.Errorf("the failure must name the break and where it was, got:\n%s", out)
	}
}

// The same break, declared, reaches a person instead. The declaration decides
// which verdict a break gets and never whether it is reported at all: the
// break is still named, and the run is still not one that may merge unread.
func TestReviewRefersADeclaredRustAPIBreak(t *testing.T) {
	semverChecksStub(t, semverChecksBroken)
	dir, base := reviewRepoSaying(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"},
		"feat(probe)!: drop do_thing")

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
	// The claim adds a referral and removes nothing: the break itself is still
	// the row a reader reviews, located at its merge-base declaration. A
	// declaration that made the break disappear from the report would be the
	// author clearing a gate by writing a title.
	if !strings.Contains(out, gateAPISurface+"(probe)") || !strings.Contains(out, ", declared") {
		t.Errorf("the per-component row naming the declared break is missing, got:\n%s", out)
	}
	if !strings.Contains(out, "merge-base probe/src/lib.rs:1") {
		t.Errorf("a removed symbol must be located at its merge-base declaration, got:\n%s", out)
	}
}

// A declaration with no break behind it is not free. It is honoured on its own
// evidence — the author's — and the component that was compared says what the
// comparison found, which is nothing.
func TestReviewRefersARustDeclarationWithNoBreak(t *testing.T) {
	semverChecksStub(t, semverChecksUnbroken)
	dir, base := reviewRepoSaying(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": crateAPI + "\npub fn also() {}\n"},
		"feat(probe)!: say it breaks")

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a declaration with nothing behind it must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPIBreakDeclared) {
		t.Errorf("the referral must name the declaration, got:\n%s", out)
	}
	if !strings.Contains(out, "no incompatible change") {
		t.Errorf("a compared component must say it was compared, got:\n%s", out)
	}
}

// Ordinary growth, undeclared, is the one combination that merges unattended:
// the comparison ran, and it found nothing.
func TestReviewPassesAnAdditiveRustAPIChange(t *testing.T) {
	semverChecksStub(t, semverChecksUnbroken)
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": crateAPI + "\npub fn also() {}\n"})

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("an additive change must merge unattended, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "no incompatible change") {
		t.Errorf("a compared component must say it was compared, got:\n%s", out)
	}
}

// A run the tool would not make is neither pass nor fail. Failing is a gate
// the author cannot clear, and passing is a comparison that never happened
// rendering as one that happened and found nothing.
func TestReviewRefersARustSurfaceItCouldNotCompare(t *testing.T) {
	semverChecksStub(t, semverChecksRefused)
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})

	out, err := runReview(t, dir, base)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("an uncomputable surface must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPISurfaceUncomputable) || !strings.Contains(out, "probe") {
		t.Errorf("the referral must name the component and why, got:\n%s", out)
	}
	if !strings.Contains(out, "nothing to semver-check") {
		t.Errorf("the referral must carry the tool's own account, got:\n%s", out)
	}
}

// Nil means not measured, for Rust as for Go. A component that did not ask has
// its API compared by nothing — the stub here would report a break, and is
// never run.
func TestReviewComparesNothingForARustComponentThatDidNotOptIn(t *testing.T) {
	semverChecksStub(t, semverChecksBroken)
	dir, base := reviewRepo(t, crateBase(cratePlain),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("a component that did not opt in must not be gated, got %v:\n%s", err, out)
	}
	if strings.Contains(out, gateAPISurface) {
		t.Errorf("nothing asked for a comparison, so nothing may be reported about one:\n%s", out)
	}
}

// review compare and review --surfaces reach the exact verdict a single
// review invocation would, without --surfaces ever running the comparison
// again: the stub is removed before that step runs, so a second invocation
// would fail outright rather than silently repeat the answer.
func TestReviewCompareAndSurfacesReachTheSameVerdictAsOneInvocation(t *testing.T) {
	semverChecksStub(t, semverChecksBroken)
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})

	compare := newReviewCompareCmd()
	var compareOut bytes.Buffer
	compare.SetOut(&compareOut)
	compare.SetErr(&compareOut)
	surfacesPath := filepath.Join(t.TempDir(), "surfaces.json")
	compare.SetArgs([]string{"--dir", dir, "--base", base, "--write-surfaces", surfacesPath})
	if err := compare.Execute(); err != nil {
		t.Fatalf("review compare: %v: %s", err, compareOut.String())
	}

	// Proves --surfaces never re-runs the comparison: the stub that would
	// answer it is gone, so a second invocation has nothing to execute.
	removeSemverChecksStub(t)

	out, err := runReview(t, dir, base, "--surfaces", surfacesPath)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("an undeclared break must still fail (exit 1), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "undeclared") || !strings.Contains(out, "probe/src/lib.rs") {
		t.Errorf("the failure must name the break and where it was, got:\n%s", out)
	}
}

// The document review compare writes names the base it actually compared
// against, so review --surfaces answers against that commit rather than
// re-resolving "auto" and risking a different answer if origin's default
// branch moved between the two invocations.
func TestReviewCompareRecordsTheBaseItResolved(t *testing.T) {
	semverChecksStub(t, semverChecksUnbroken)
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\npub fn also() {}\n"})

	compare := newReviewCompareCmd()
	var compareOut bytes.Buffer
	compare.SetOut(&compareOut)
	compare.SetErr(&compareOut)
	surfacesPath := filepath.Join(t.TempDir(), "surfaces.json")
	compare.SetArgs([]string{"--dir", dir, "--base", base, "--write-surfaces", surfacesPath})
	if err := compare.Execute(); err != nil {
		t.Fatalf("review compare: %v: %s", err, compareOut.String())
	}

	doc, err := readSurfaces(surfacesPath)
	if err != nil {
		t.Fatalf("readSurfaces: %v", err)
	}
	if doc.Base != base {
		t.Errorf("recorded base = %q, want %q", doc.Base, base)
	}
	if len(doc.Results) != 1 || doc.Results[0].Component != "probe" {
		t.Errorf("results = %+v, want one result for probe", doc.Results)
	}
}

// A --surfaces document that cannot be read at all — missing, or the job
// that would have written it never ran — must still be referred, not answer
// with a bare error: main.go maps any error that is not a ui.ExitError to
// exit 1, indistinguishable from a gate the author can clear, and a workflow
// step that only re-fails a job past exit 2 would then let this pass with
// referral-publish's own credential having posted nothing at all.
func TestReviewWithAnUnreadableSurfacesDocumentRefers(t *testing.T) {
	dir, base := reviewRepo(t, crateBase(crateOptIn),
		map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})

	out, err := runReview(t, dir, base, "--surfaces", filepath.Join(t.TempDir(), "does-not-exist.json"))
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("an unreadable surfaces document must be referred (exit 2), got %v:\n%s", err, out)
	}
	if !strings.Contains(out, referral.DisqualificationAPISurfaceUncomputable) || !strings.Contains(out, "probe") {
		t.Errorf("the referral must name the component and why, got:\n%s", out)
	}
	if !strings.Contains(out, "the comparison document could not be read") {
		t.Errorf("the referral must say why, got:\n%s", out)
	}
}

// A document that opens fine but decodes into nothing readable, or names no
// base commit, is exactly as unreadable as a missing file: readSurfaces
// refuses both rather than handing review a document with nothing in it.
func TestReadSurfacesRejectsAMalformedDocument(t *testing.T) {
	for name, raw := range map[string]string{
		"invalid json": `{not json`,
		"no base":      `{"results":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "surfaces.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readSurfaces(path); err == nil {
				t.Errorf("readSurfaces(%s) was accepted, want it refused", raw)
			}
		})
	}
}

// writeSurfaces reports the underlying failure rather than losing it: a path
// under a directory that does not exist cannot be created, and the caller
// needs that reason, not a silent success.
func TestWriteSurfacesReportsAnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "surfaces.json")
	if err := writeSurfaces(path, "deadbeef", nil); err == nil {
		t.Error("writeSurfaces under a missing directory was accepted")
	}
}

// review compare with no destination has nowhere to put what it computed,
// so it refuses before running any comparison rather than doing the work
// and discarding the result.
func TestReviewCompareNeedsAWriteSurfacesFlag(t *testing.T) {
	cmd := newReviewCompareCmd()
	cmd.SetArgs([]string{"--dir", t.TempDir()})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Error("review compare with no --write-surfaces was accepted")
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

// component.Load refuses api_surface on every language but Go and Rust, so a
// component reaching compareSurface under a third language is a defensive
// path rather than one reachable through review — this exercises it directly
// against a TypeScript runner, which resolves to neither.
func TestCompareSurfaceHasNoComparisonForAThirdLanguage(t *testing.T) {
	c := component.Component{Name: "web", Dir: "web", Runner: runner.Vitest}
	findings, uncomputable := compareSurface(context.Background(), newReviewCmd(), c, t.TempDir(), t.TempDir(), toolchain.Envs(nil))
	if findings != nil {
		t.Errorf("findings = %+v, want none", findings)
	}
	if uncomputable != "no public-API comparison exists for this component's language" {
		t.Errorf("uncomputable = %q, want the language named as having no comparison", uncomputable)
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
