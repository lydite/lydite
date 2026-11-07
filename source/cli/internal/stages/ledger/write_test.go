package ledgerstages

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// A recording with neither a baseline nor history has nothing to land, and
// reaches no branch at all.
func TestWriteStateWithNothingToLandLandsNothing(t *testing.T) {
	out, err := WriteState(context.Background(), WriteStateIn{Dir: t.TempDir(), Head: "deadbeef"})
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if out.Landed != nil {
		t.Errorf("Landed = %+v, want nothing", out.Landed)
	}
}

// The baseline and the records land together, and the records that landed are
// the outcome — so the same recording made twice lands no record the second
// time.
func TestWriteStateLandsTheBaselineAndTheRecordsInOneWrite(t *testing.T) {
	dir, tree := ledgerRemote(t)
	snap := gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": ledgerEntry(1, 2)}}
	rec := ledger.Record{
		Kind: ledger.KindEntry, At: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Commit: "0123456789abcdef0123456789abcdef01234567", Branch: "main",
		Components: map[string]ledger.Component{"svc": {Tests: &junit.Counts{Total: 1}}},
	}
	var asked []string
	records := func(worktree string) (gitstate.Recording, error) {
		asked = append(asked, worktree)
		return gitstate.Recording{Records: []ledger.Record{rec}}, nil
	}

	out, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Snapshot: snap, Records: records})
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if !reflect.DeepEqual(out.Landed, []ledger.Record{rec}) {
		t.Errorf("Landed = %+v, want the one record offered", out.Landed)
	}
	if len(asked) != 1 {
		t.Errorf("the records were asked for %d times, want once for the one attempt that landed", len(asked))
	}
	held, err := gitstate.ReadSnapshot(context.Background(), dir, tree)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(held.Coverage, snap.Coverage) {
		t.Errorf("the branch holds %+v, want %+v", held.Coverage, snap.Coverage)
	}

	again, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Snapshot: snap, Records: records})
	if err != nil {
		t.Fatalf("WriteState again: %v", err)
	}
	if again.Landed != nil {
		t.Errorf("Landed = %+v the second time, want nothing: the record is already on the branch", again.Landed)
	}
}

// The history ComposeRecords composes reaches the branch only through the
// write, asked of the branch the write fetched.
func TestComposedRecordsLandThroughTheWrite(t *testing.T) {
	dir, tree := ledgerRemote(t)
	head, err := gitstate.DescribeCommit(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	history, err := ComposeRecords(context.Background(), ComposeRecordsIn{Dir: dir, Components: ledgerScalar()})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}

	out, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Records: history.Records})
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if len(out.Landed) != 1 || out.Landed[0].Commit != head.SHA || out.Landed[0].Branch != "main" {
		t.Errorf("Landed = %+v, want this commit's entry on main", out.Landed)
	}
}

// A write that never lands is the stage's error, exactly as gitstate.Write
// gave it, and never an outcome claiming anything landed.
func TestWriteStateReturnsAWriteThatNeverLandedAsItsError(t *testing.T) {
	dir, tree := ledgerRepo(t, map[string]string{"README.md": "a repository with no remote\n"})
	snap := gitstate.Snapshot{Coverage: gitstate.Baseline{"svc": ledgerEntry(1, 2)}}

	out, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Snapshot: snap})

	prefix := "pushing the recording for " + tree + " to " + gitstate.BranchName + " (3 attempts): "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("err = %v, want gitstate.Write's own, starting %q", err, prefix)
	}
	if out.Landed != nil {
		t.Errorf("Landed = %+v from a write that never landed", out.Landed)
	}
}

// The open-findings snapshot a recording writes carries tool text and a
// location, and never source: a finding's site — here a secret, and the value
// a component's declared env: holds — appears nowhere in what lands on the
// branch. A commit recorded again writes no snapshot at all.
func TestTheOpenFindingsSnapshotLandsWithoutSourceAndOnlyOnce(t *testing.T) {
	dir, tree := ledgerRemote(t)
	const site, declared = "AKIAIOSFODNN7EXAMPLE-site-text", "s3cr3t-declared-env-value"
	leak := finding.Finding{Gate: "gitleaks", Path: "config/app.env", Line: 3, Rule: "aws-access-token",
		Message: "AWS access token", Site: site}
	env := finding.Finding{Gate: "gitleaks", Path: ".lydite/components.yml", Line: 9, Rule: "generic-api-key",
		Message: "generic API key", Site: "API_TOKEN: " + declared}
	history, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: dir, Components: ledgerScalar(), RootFindings: map[string]int{"gitleaks": 2},
		Scope: map[ledger.FindingBucket]bool{{Gate: "gitleaks"}: true},
		Found: []finding.Finding{leak, env},
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}

	out, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Records: history.Records})
	if err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	if out.OpenFindingsParts != 1 {
		t.Errorf("OpenFindingsParts = %d, want 1", out.OpenFindingsParts)
	}
	path, err := ledger.OpenFindingsPath("main", 0)
	if err != nil {
		t.Fatal(err)
	}
	ledgerGit(t, dir, "fetch", "--quiet", "origin", gitstate.BranchName)
	written := ledgerGit(t, dir, "show", "origin/"+gitstate.BranchName+":"+path)
	for _, want := range []string{leak.Fingerprint(), env.Fingerprint(), "config/app.env", "generic API key"} {
		if !strings.Contains(written, want) {
			t.Errorf("%s does not carry %q:\n%s", path, want, written)
		}
	}
	for _, source := range []string{site, declared} {
		if strings.Contains(written, source) {
			t.Errorf("%s carries the source text %q:\n%s", path, source, written)
		}
	}

	again, err := WriteState(context.Background(), WriteStateIn{Dir: dir, Head: tree, Records: history.Records})
	if err != nil {
		t.Fatalf("WriteState again: %v", err)
	}
	if again.Landed != nil || again.OpenFindingsParts != 0 {
		t.Errorf("WriteState again = %+v, want no record landed and no snapshot written for a commit already recorded", again)
	}
}
