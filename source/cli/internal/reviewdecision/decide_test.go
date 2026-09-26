package reviewdecision

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"lydite/lydite/internal/depdelta"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/referral"
)

// readmeExemption covers the README alone.
const readmeExemption = "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"

// goSum renders a go.sum pinning each module at the version given.
func goSum(modules map[string]string) string {
	var b strings.Builder
	for name, version := range modules {
		fmt.Fprintf(&b, "%s %s h1:aaaa=\n%s %s/go.mod h1:bbbb=\n", name, version, name, version)
	}
	return b.String()
}

// bumpExemption covers the paths given, on the condition every version moved
// by a patch or a minor.
func bumpExemption(paths ...string) string {
	return "exemptions:\n  - name: dependency-bump\n    reason: routine version maintenance\n" +
		"    versions: " + referral.VersionsPatchAndMinor + "\n    paths: [\"" +
		strings.Join(paths, "\", \"") + "\"]\n"
}

func kinds(d referral.Decision) []string {
	var out []string
	for _, dq := range d.Disqualifications {
		out = append(out, dq.Kind)
	}
	return out
}

// The exemptions come from the base commit, so a change covered by what the
// base declares is not referred, and the file read is the base's own.
func TestDecideExemptsAChangeTheBaseCovers(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "hello again"})

	got, err := Decide(context.Background(), Input{Dir: dir, Base: base})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Decision.Referred || got.Decision.Exemption != "readme-only" {
		t.Errorf("decision = %+v, want exempt under readme-only", got.Decision)
	}
	if len(got.File.Exemptions) != 1 {
		t.Errorf("file = %+v, want the base's one exemption", got.File)
	}
	if got.BreakDeclared != "" || len(got.Warnings) != 0 {
		t.Errorf("result = %+v, want no declaration and no warning", got)
	}
}

// A file the branch widens for itself gets no benefit from its own widening:
// the base declared nothing, so the change is referred.
func TestDecideReadsExemptionsFromTheBaseNotTheBranch(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again", referral.FileName: readmeExemption})

	got, err := Decide(context.Background(), Input{Dir: dir, Base: base})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !got.Decision.Referred || len(got.File.Exemptions) != 0 {
		t.Errorf("result = %+v, want a referral under the base's empty file", got)
	}
}

// An exemptions file the base holds and that does not parse is an error, not
// an empty allowlist.
func TestDecideFailsOnAnUnparseableExemptionsFile(t *testing.T) {
	dir, base := repo(t,
		map[string]string{referral.FileName: "exemptions: [unterminated\n"},
		map[string]string{"README.md": "x"})
	if _, err := Decide(context.Background(), Input{Dir: dir, Base: base}); err == nil {
		t.Error("an unparseable exemptions file produced a decision")
	}
	if _, err := DecideFromDiff(context.Background(), dir, base); err == nil {
		t.Error("an unparseable exemptions file produced a queue decision")
	}
}

// A directory outside any repository has no base to read from at all.
func TestDecideFailsOutsideARepository(t *testing.T) {
	if _, err := Decide(context.Background(), Input{Dir: t.TempDir(), Base: "HEAD"}); err == nil {
		t.Error("a directory outside any repository produced a decision")
	}
}

// A base the diff cannot be read against is an error rather than an empty
// change.
func TestDecideFailsOnABaseTheDiffCannotBeReadAgainst(t *testing.T) {
	dir, _ := repo(t, map[string]string{"README.md": "hello"}, map[string]string{"README.md": "x"})
	if _, err := Decide(context.Background(), Input{Dir: dir, Base: "0000000000000000000000000000000000000000"}); err == nil {
		t.Error("an unreadable diff produced a decision")
	}
}

// A break declared in a commit refers the change even where an exemption
// covers every path and nothing was compared: the claim can only ever add a
// referral.
func TestDecideRefersABreakDeclaredInACommit(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "hello again"}, "feat!: drop everything")

	got, err := Decide(context.Background(), Input{Dir: dir, Base: base})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !got.Decision.Referred || got.BreakDeclared != "a commit in "+short(base)+"..HEAD" {
		t.Fatalf("result = %+v, want a referral naming the commit range", got)
	}
	want := []string{referral.DisqualificationAPIBreakDeclared}
	if fmt.Sprint(kinds(got.Decision)) != fmt.Sprint(want) {
		t.Errorf("disqualifications = %v, want %v", kinds(got.Decision), want)
	}
	if got.Decision.Disqualifications[0].Evidence != "declared in a commit in "+short(base)+"..HEAD" {
		t.Errorf("evidence = %q", got.Decision.Disqualifications[0].Evidence)
	}
}

// The title is read once, after the exemptions and the diff, and a break it
// declares is named as the title's.
func TestDecideReadsTheDeclarationFromTheTitle(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "hello again"})

	calls := 0
	got, err := Decide(context.Background(), Input{Dir: dir, Base: base, Title: func() string {
		calls++
		return "feat!: break the thing"
	}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if calls != 1 {
		t.Errorf("Title called %d times, want once", calls)
	}
	if got.BreakDeclared != "the pull request title" || !got.Decision.Referred {
		t.Errorf("result = %+v, want a referral naming the title", got)
	}
}

// A run that stops at the exemptions file never resolves the title: resolving
// it can cost a request to the platform, and a run that already failed has no
// declaration left to read.
func TestDecideDoesNotResolveTheTitleForARunThatFailed(t *testing.T) {
	dir, base := repo(t,
		map[string]string{referral.FileName: "exemptions: [unterminated\n"},
		map[string]string{"README.md": "x"})
	called := false
	if _, err := Decide(context.Background(), Input{Dir: dir, Base: base, Title: func() string {
		called = true
		return ""
	}}); err == nil {
		t.Fatal("an unparseable exemptions file produced a decision")
	}
	if called {
		t.Error("the title was resolved for a run that failed before the declaration was read")
	}
}

// A surface that could not be compared refers under its own component, naming
// the reason and what was left out; one compared and clean adds nothing, and
// an undeclared break adds nothing either — that is a gate the caller renders,
// not a referral.
func TestDecideRefersOnlyASurfaceThatCouldNotBeCompared(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "hello again"})

	got, err := Decide(context.Background(), Input{Dir: dir, Base: base, Surfaces: []SurfaceComparison{
		{Component: "web", Dir: "web", Uncomputable: "the base did not build", Skipped: []string{"@web/tools"}},
		{Component: "sdk", Dir: "sdk"},
		{Component: "api", Dir: "api", Findings: []finding.Finding{{Message: "Removed: Do"}}},
	}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !got.Decision.Referred || len(got.Decision.Disqualifications) != 1 {
		t.Fatalf("decision = %+v, want one referral for web", got.Decision)
	}
	dq := got.Decision.Disqualifications[0]
	if dq.Kind != referral.DisqualificationAPISurfaceUncomputable || dq.Path != "web" {
		t.Errorf("disqualification = %+v, want web's uncomputable surface", dq)
	}
	want := "web: the base did not build; not compared, because it names no entry point: @web/tools"
	if dq.Evidence != want {
		t.Errorf("evidence = %q, want %q", dq.Evidence, want)
	}
}

// A package the merge-base did not pin refers, capped in its evidence, and a
// manifest nothing can read refers under its own kind; the delta each carries
// is returned for the caller to render.
func TestDecideRefersAnAddedDependencyAndAnUnreadableManifest(t *testing.T) {
	added := map[string]string{"github.com/spf13/cobra": "v1.10.1"}
	for i := 0; i < ListCap+2; i++ {
		added[fmt.Sprintf("example.com/m%d", i)] = "v1.0.0"
	}
	dir, base := repo(t,
		map[string]string{
			"go.sum":    goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
			"yarn.lock": "# yarn lockfile v1\n",
		},
		map[string]string{
			"go.sum":    goSum(added),
			"yarn.lock": "# yarn lockfile v1\nleft-pad@^1.0.0:\n  version \"1.0.1\"\n",
		})

	got, err := Decide(context.Background(), Input{Dir: dir, Base: base})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(got.Dependencies) != 2 {
		t.Fatalf("dependencies = %+v, want both manifests", got.Dependencies)
	}
	byKind := map[string]referral.Disqualification{}
	for _, dq := range got.Decision.Disqualifications {
		byKind[dq.Kind] = dq
	}
	add, ok := byKind[referral.DisqualificationDependencyAdded]
	if !ok || add.Path != "go.sum" || !strings.Contains(add.Evidence, "…and 2 more") {
		t.Errorf("added = %+v, want go.sum's additions, capped", add)
	}
	unread, ok := byKind[referral.DisqualificationDependencyDeltaUnmeasured]
	if !ok || unread.Path != "yarn.lock" || !strings.Contains(unread.Evidence, "yarn") {
		t.Errorf("unmeasured = %+v, want yarn.lock named with its ecosystem", unread)
	}
}

// A manifest renamed onto itself appears twice in the diff and is compared once.
func TestMeasureDependenciesComparesAManifestOnce(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"})},
		map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": "v1.10.2"})})
	got := measureDependencies(context.Background(), dir, base, []string{"go.sum", "go.sum", "README.md"})
	if len(got) != 1 || got[0].Unmeasured != "" {
		t.Errorf("measureDependencies = %+v, want go.sum compared once", got)
	}
}

// A version bump is exempt only when the versions moved a little and the scan
// evidence was given and passed; the evidence is asked for only once the
// versions have already passed their half.
func TestDecideMeetsAVersionConditionOnlyWithScanEvidence(t *testing.T) {
	bump := func(t *testing.T, to string) (string, string) {
		t.Helper()
		return repo(t,
			map[string]string{
				referral.FileName: bumpExemption("go.sum"),
				"go.sum":          goSum(map[string]string{"github.com/spf13/cobra": "v1.10.1"}),
			},
			map[string]string{"go.sum": goSum(map[string]string{"github.com/spf13/cobra": to})})
	}
	ctx := context.Background()

	dir, base := bump(t, "v1.10.2")
	got, err := Decide(ctx, Input{Dir: dir, Base: base})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !got.Decision.Referred || len(got.Decision.Unsatisfied) != 1 {
		t.Errorf("decision = %+v, want the condition unmet with no scan evidence", got.Decision)
	}

	got, err = Decide(ctx, Input{Dir: dir, Base: base, ScanEvidence: func() bool { return true }})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.Decision.Referred || got.Decision.Exemption != "dependency-bump" {
		t.Errorf("decision = %+v, want exempt under dependency-bump", got.Decision)
	}

	major, majorBase := bump(t, "v2.0.0")
	asked := false
	got, err = Decide(ctx, Input{Dir: major, Base: majorBase, ScanEvidence: func() bool {
		asked = true
		return true
	}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if asked {
		t.Error("the scan evidence was asked for a change whose versions already failed the condition")
	}
	if !got.Decision.Referred {
		t.Errorf("decision = %+v, want a major bump referred", got.Decision)
	}
}

// versionsPatchAndMinor fails for a manifest nothing could measure, because
// lydite cannot say how far a version moved in a file it could not read.
func TestVersionsPatchAndMinorFailsAnUnmeasuredManifest(t *testing.T) {
	if versionsPatchAndMinor([]ManifestDelta{{Path: "yarn.lock", Unmeasured: "no reader"}}) {
		t.Error("an unmeasured manifest passed the version condition")
	}
	if !versionsPatchAndMinor([]ManifestDelta{{Path: "go.sum", Delta: depdelta.Delta{}}}) {
		t.Error("a manifest that moved nothing failed the version condition")
	}
}

// The queue's decision is the diff and the base's exemptions alone: a break a
// commit declares, which Decide refers, adds nothing here.
func TestDecideFromDiffReadsNoDeclaration(t *testing.T) {
	dir, base := repo(t,
		map[string]string{"README.md": "hello", referral.FileName: readmeExemption},
		map[string]string{"README.md": "hello again"}, "feat!: drop everything")

	got, err := DecideFromDiff(context.Background(), dir, base)
	if err != nil {
		t.Fatalf("DecideFromDiff: %v", err)
	}
	if got.Referred || got.Exemption != "readme-only" {
		t.Errorf("decision = %+v, want exempt under readme-only", got)
	}
}

// Commits that cannot be read are warned about rather than swallowed, since a
// break declared only in one of them is then never seen.
func TestBreakDeclarationWarnsWhenTheCommitsCannotBeRead(t *testing.T) {
	where, warning := breakDeclaration(context.Background(), t.TempDir(), "0000000000000000000000000000000000000000", "")
	if where != "" {
		t.Errorf("where = %q, want nothing declared", where)
	}
	if !strings.HasPrefix(warning, "warning: could not read the commits in 000000000000..HEAD") || strings.HasSuffix(warning, "\n") {
		t.Errorf("warning = %q, want one line naming the unread range", warning)
	}
}

// ResolveBase refuses every base that would turn the gate into a rubber stamp,
// and returns the full SHA of one it accepts.
func TestResolveBase(t *testing.T) {
	dir, base := repo(t, map[string]string{"README.md": "hello"}, map[string]string{"README.md": "x"})
	ctx := context.Background()
	head := strings.TrimSpace(executil.RunQuiet(ctx, dir, "git", "rev-parse", "HEAD").Output)

	got, err := ResolveBase(ctx, dir, base[:12], "")
	if err != nil || got != base {
		t.Errorf("ResolveBase(short) = %q, %v, want %q", got, err, base)
	}
	for name, requested := range map[string]string{
		"empty":        "",
		"not a commit": "--output=/tmp/x",
		"unknown":      "0000000000000000000000000000000000000000",
	} {
		if _, err := ResolveBase(ctx, dir, requested, ""); err == nil {
			t.Errorf("ResolveBase(%s) was accepted", name)
		}
	}

	// A commit off to the side of HEAD is a commit, and still not this
	// branch's base.
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
	run("checkout", "--quiet", "-b", "side", base)
	run("commit", "--quiet", "--allow-empty", "-m", "side")
	side := strings.TrimSpace(executil.RunQuiet(ctx, dir, "git", "rev-parse", "HEAD").Output)
	run("checkout", "--quiet", head)
	if _, err := ResolveBase(ctx, dir, side, ""); err == nil || !strings.Contains(err.Error(), "not an ancestor") {
		t.Errorf("ResolveBase(side) = %v, want it refused as no ancestor", err)
	}

	if _, err := ResolveBase(ctx, dir, "auto", ""); err == nil || !strings.Contains(err.Error(), "--base auto") {
		t.Errorf("ResolveBase(auto) with no origin = %v, want it refused", err)
	}
}
