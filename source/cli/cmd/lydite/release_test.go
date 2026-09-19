package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/ui"
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
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	for i, c := range commits {
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(c.message+string(rune('a'+i))), 0o600); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-m", c.message)
		if c.tag != "" {
			run("tag", c.tag)
		}
	}
	return dir
}

// releaseShallowClone clones origin to its last commit alone and writes tag's
// ref back without the commit it names, which is the checkout a clone that
// fetched refs without the history behind them leaves.
func releaseShallowClone(t *testing.T, origin, tag string) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if r := executil.RunQuiet(ctx, origin, "git", "clone", "--depth", "1", "file://"+origin, dir); !r.Ok() {
		t.Fatalf("git clone: %v\n%s", r.Err, r.Output)
	}
	r := executil.RunQuiet(ctx, origin, "git", "rev-parse", tag)
	if !r.Ok() {
		t.Fatalf("git rev-parse %s: %v\n%s", tag, r.Err, r.Output)
	}
	refs := filepath.Join(dir, ".git", "refs", "tags")
	if err := os.MkdirAll(refs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refs, tag), []byte(strings.TrimSpace(r.Output)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runReleaseCheckCmd(t *testing.T, dir string, extra ...string) (string, error) {
	t.Helper()
	cmd := newReleaseCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"check", "--dir", dir, "--no-color"}, extra...))
	err := cmd.Execute()
	return out.String(), err
}

// The bump rule is ADR 0010's, in both eras: a break lands where the leftmost
// non-zero component of the previous version increases. Each case names the
// era it is testing, because the two halves of the rule disagree about exactly
// the same minor bump.
func TestBumpAdmitsBreakOnlyWhereTheLeftmostNonZeroComponentIncreases(t *testing.T) {
	for _, c := range []struct {
		previous, tag string
		want          bool
		why           string
	}{
		{"v0.2.0", "v0.3.0", true, "in 0.x the leftmost non-zero component is the minor"},
		{"v0.2.0", "v0.2.1", false, "a patch bump carries no break in any era"},
		{"v0.2.0", "v0.10.0", true, "the minor is compared numerically, not lexically"},
		{"v0.9.0", "v1.0.0", true, "the release that leaves 0.x may carry one"},
		{"v0.2.0", "v1.0.0", true, "crossing eras, though the minor it is measured on went 2 to 0"},
		{"v1.2.0", "v1.3.0", false, "from 1.0.0 onward a minor bump is backward-compatible"},
		{"v1.2.0", "v1.2.1", false, "nor does a patch bump in 1.x"},
		{"v1.2.0", "v2.0.0", true, "from 1.0.0 onward the leftmost non-zero component is the major"},
		{"v1.2.0", "v10.0.0", true, "the major is compared numerically too"},
		// A prerelease is the line it leads to arriving early, so it carries
		// that line's bump: the marker is not a version component.
		{"v0.2.0", "v0.3.0-rc.1", true, "a prerelease of an admitting bump admits one"},
		{"v0.2.0", "v0.2.1-rc.1", false, "a prerelease of a patch bump admits none"},
		{"v1.2.0", "v2.0.0-rc.1", true, "a prerelease of a major bump admits one"},
		// Below 0.1.0 the leftmost non-zero component is the patch, which is
		// the same rule read one position further right.
		{"v0.0.3", "v0.0.4", true, "in 0.0.x the leftmost non-zero component is the patch"},
		{"v0.0.3", "v0.1.0", true, "a minor bump out of 0.0.x increases it too"},
		{"v0.0.3", "v0.0.3", false, "no bump at all admits nothing, even in 0.0.x"},
	} {
		if got := bumpAdmitsBreak(c.previous, c.tag); got != c.want {
			t.Errorf("bumpAdmitsBreak(%q, %q) = %v, want %v — %s", c.previous, c.tag, got, c.want, c.why)
		}
	}
}

// A declared break on the bump that admits it publishes, and the declaring
// commits are still named: what the release carries is worth reading even when
// nothing refuses it.
func TestReleaseCheckPassesWhenTheBumpAdmitsTheDeclaredBreak(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "feat(api)!: the verdict is a status, not a boolean"},
		releaseCommit{message: "fix: a leader dot", tag: "v0.3.0"},
	)

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.3.0")
	if err != nil {
		t.Fatalf("a minor bump in 0.x admits a declared break: %v\n%s", err, out)
	}
	if !strings.Contains(out, "the verdict is a status, not a boolean") {
		t.Errorf("the declaring commit must be named:\n%s", out)
	}
}

// The failure this command exists for: the claim and the number disagree, and
// the tag does not become a release.
func TestReleaseCheckFailsWhenTheBumpAdmitsNoDeclaredBreak(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "refactor!: the report owns the exit code\n\nBREAKING CHANGE: callers read Err()"},
		releaseCommit{message: "fix: a leader dot", tag: "v0.2.1"},
	)

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.2.1")
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("a patch bump carrying a declared break must fail, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "the report owns the exit code") {
		t.Errorf("every declaring commit is named:\n%s", out)
	}
}

// Nothing declared is a pass that says what it read, so a range nobody looked
// at cannot be mistaken for one that held nothing.
func TestReleaseCheckPassesWhenNothingInTheRangeDeclares(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "fix: a leader dot"},
		releaseCommit{message: "docs: the grammar", tag: "v0.2.1"},
	)

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.2.1")
	if err != nil {
		t.Fatalf("a range declaring nothing passes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 commits in v0.2.0..v0.2.1") {
		t.Errorf("the range and its size are reported:\n%s", out)
	}
}

// The first tag has no predecessor. The range is empty by definition, and the
// report says so rather than reporting a clean range — an empty range and an
// unresolvable one are not the same answer.
func TestReleaseCheckReportsTheFirstReleasesRangeAsEmpty(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat!: everything, all at once", tag: "v0.1.0"},
	)

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.1.0")
	if err != nil {
		t.Fatalf("the first release passes: %v\n%s", err, out)
	}
	if !strings.Contains(out, "first release") || !strings.Contains(out, "empty") {
		t.Errorf("an empty range says so:\n%s", out)
	}
}

// A shallow checkout that never fetched the tags below the one being released
// reads exactly like a genuine first release: PreviousTag finds no candidate
// either way. Reporting it as a pass would be a gate that could not run
// rendering as one that did — the shallow checkout must be told apart from
// the first release and refused, not silently accepted.
func TestReleaseCheckRefusesAFirstLookingRangeInAShallowCheckout(t *testing.T) {
	origin := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.1.0"},
		releaseCommit{message: "fix: a leader dot"},
		releaseCommit{message: "feat: the second release", tag: "v0.2.0"},
	)
	ctx := context.Background()
	dir := t.TempDir()
	if r := executil.RunQuiet(ctx, origin, "git", "clone", "--depth", "1", "file://"+origin, dir); !r.Ok() {
		t.Fatalf("git clone --depth 1: %v\n%s", r.Err, r.Output)
	}
	// The clone's own tag listing must actually miss v0.1.0 for this to test
	// what it claims to: a shallow clone fetches a tag pointing at the one
	// commit it holds, but not one further back than its depth.
	if r := executil.RunQuiet(ctx, dir, "git", "tag", "-l", "v*"); !r.Ok() || strings.Contains(r.Output, "v0.1.0") {
		t.Fatalf("fixture is not shallow enough: tags are %q (err %v)", r.Output, r.Err)
	}

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.2.0")
	if err == nil {
		t.Fatalf("a shallow checkout with no lower tag must not pass as a first release:\n%s", out)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("an unresolvable range is an error, not a verdict, got %v", err)
	}
	if !strings.Contains(err.Error(), "fetch-depth: 0") {
		t.Errorf("the error names the fix: %v", err)
	}
}

// A tag that is not a version leaves the range unresolvable, and an
// unresolvable range is an error naming the fix — never a pass.
func TestReleaseCheckRefusesATagThatIsNotAVersion(t *testing.T) {
	dir := releaseRepo(t, releaseCommit{message: "feat: the first release", tag: "v0.1.0"})

	out, err := runReleaseCheckCmd(t, dir, "--tag", "release-2")
	if err == nil {
		t.Fatalf("a tag that is not a version cannot be checked:\n%s", out)
	}
	var exit ui.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("an unresolvable range is an error, not a verdict, got %v", err)
	}
	if !strings.Contains(err.Error(), "vMAJOR.MINOR.PATCH") {
		t.Errorf("the error names the spelling expected: %v", err)
	}
}

// The tag being released need not exist: the range is read to HEAD, which at
// tag time is the commit the tag was pushed at and before it is the commit the
// release is about to be cut from.
func TestReleaseCheckReadsTheRangeOfATagNotYetCreated(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "feat(api)!: the verdict is a status, not a boolean"},
		releaseCommit{message: "fix: a leader dot"},
	)

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.3.0")
	if err != nil {
		t.Fatalf("a minor bump in 0.x admits a declared break: %v\n%s", err, out)
	}
	if !strings.Contains(out, "the verdict is a status, not a boolean") {
		t.Errorf("the declaring commit must be named:\n%s", out)
	}

	out, err = runReleaseCheckCmd(t, dir, "--tag", "v0.2.1")
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("a patch bump carrying a declared break must fail, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "v0.2.0..v0.2.1") {
		t.Errorf("the report names the version range the verdict is about:\n%s", out)
	}
}

// A previous tag whose commit this checkout does not hold — a clone whose refs
// arrived without the history behind them — is an error naming the fix. A pass
// there would report a range nothing ever read as a clean one.
func TestReleaseCheckRefusesARangeItCannotRead(t *testing.T) {
	dir := releaseShallowClone(t, releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.1.0"},
		releaseCommit{message: "fix: a leader dot"},
	), "v0.1.0")

	out, err := runReleaseCheckCmd(t, dir, "--tag", "v0.2.0")
	if err == nil {
		t.Fatalf("a range whose start this checkout does not hold cannot be read:\n%s", out)
	}
	if !strings.Contains(err.Error(), "fetch-depth: 0") {
		t.Errorf("the error names the fetch depth to set: %v", err)
	}
}

// With no tag anywhere to read, the command cannot begin, and says which of
// the three ways to name one is available.
func TestReleaseCheckNeedsATagItCanName(t *testing.T) {
	t.Setenv("GITHUB_REF_TYPE", "")
	t.Setenv("GITHUB_REF_NAME", "")
	dir := releaseRepo(t, releaseCommit{message: "feat: untagged"})

	_, err := runReleaseCheckCmd(t, dir)
	if err == nil || !strings.Contains(err.Error(), "--tag") {
		t.Fatalf("the error names --tag, got %v", err)
	}
}

// A tag-triggered workflow names the tag in its environment, which is how the
// release job invokes this with no argument at all.
func TestReleaseCheckReadsTheTagFromATagTriggeredRun(t *testing.T) {
	// HEAD itself carries no tag, so the environment is the only source that
	// can name v0.2.1 here: a git-describe fallback would find nothing and
	// error before a verdict is even reached, which is what tells this test
	// apart from one where either source would answer the same.
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "feat!: the verdict is a status"},
	)
	t.Setenv("GITHUB_REF_TYPE", "tag")
	t.Setenv("GITHUB_REF_NAME", "v0.2.1")

	out, err := runReleaseCheckCmd(t, dir)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("the tag in the environment is the one checked, got %v:\n%s", err, out)
	}
}

// GITHUB_REF_NAME holds a branch name on a branch build, so it is read only
// when the ref is a tag; the checked-out tag answers instead.
func TestReleaseCheckIgnoresABranchRefAndReadsTheCheckedOutTag(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "feat!: the verdict is a status", tag: "v0.2.1"},
	)
	t.Setenv("GITHUB_REF_TYPE", "branch")
	t.Setenv("GITHUB_REF_NAME", "main")

	out, err := runReleaseCheckCmd(t, dir)
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("HEAD's own tag is what is checked, got %v:\n%s", err, out)
	}
}

// Anything automated reads the document, so the verdict travels there as a
// status rather than as a glyph.
func TestReleaseCheckJSONCarriesTheVerdict(t *testing.T) {
	dir := releaseRepo(t,
		releaseCommit{message: "feat: the first release", tag: "v0.2.0"},
		releaseCommit{message: "feat!: the verdict is a status", tag: "v0.2.1"},
	)

	out, _ := runReleaseCheckCmd(t, dir, "--tag", "v0.2.1", "--json")
	var doc struct {
		Command string `json:"command"`
		Rows    []struct {
			Status string   `json:"status"`
			Label  string   `json:"label"`
			Detail []string `json:"detail"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the document must parse: %v\n%s", err, out)
	}
	if doc.Command != "release" || len(doc.Rows) != 1 {
		t.Fatalf("one row, under the command's own name: %s", out)
	}
	if doc.Rows[0].Status != string(ui.StatusFail) {
		t.Errorf("a declared break on a patch bump is a fail: %s", out)
	}
	if len(doc.Rows[0].Detail) == 0 || !strings.Contains(doc.Rows[0].Detail[0], "the verdict is a status") {
		t.Errorf("the declaring commits travel in the document: %s", out)
	}
}
