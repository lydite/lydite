package ledger_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// update rewrites testdata/golden from the writer's current output instead of
// comparing against it: go test ./internal/ledger -run Golden -update.
var update = flag.Bool("update", false, "rewrite testdata/golden from the ledger writer's output")

const goldenDir = "testdata/golden"

// TestGoldenLedgerTrees pins the bytes ledger.Append and
// ledger.EncodeOpenFindings produce. Each subdirectory of testdata/golden is
// one scenario, a complete worktree-shaped tree, and a reader in another
// language reads these files as its fixtures, so they are the writer's own
// output and never hand-written ledger lines. The one line not produced by
// Append is the garbage line the unparseable-line scenario appends to a
// partition Append wrote, since the writer cannot produce an unparseable line.
//
// Every input is fixed (UTC times, SHAs, fingerprints), so regenerating
// changes nothing unless the writer's output did.
func TestGoldenLedgerTrees(t *testing.T) {
	built := t.TempDir()
	for _, sc := range goldenScenarios() {
		root := filepath.Join(built, sc.name)
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		sc.build(t, root)
	}

	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		copyTree(t, built, goldenDir)
		return
	}
	compareTrees(t, goldenDir, built)
}

type goldenScenario struct {
	name  string
	build func(t *testing.T, root string)
}

func goldenScenarios() []goldenScenario {
	return []goldenScenario{
		{"rolled-partition", buildRolledPartition},
		{"unparseable-line", buildUnparseableLine},
		{"out-of-order-resolve", buildOutOfOrderResolve},
		{"gap-and-gap-only-day", buildGap},
		{"release-branch", buildReleaseBranch},
		{"snapshot-complete", buildSnapshotComplete},
		{"snapshot-multipart", buildSnapshotMultipart},
		{"snapshot-lagging", buildSnapshotLagging},
		{"snapshot-identity-only", buildSnapshotIdentityOnly},
	}
}

func day(month time.Month, d, hour int) time.Time {
	return time.Date(2026, month, d, hour, 0, 0, 0, time.UTC)
}

func sha(n int) string { return fmt.Sprintf("%040x", n*7919+1) }

func fingerprint(n int) string { return fmt.Sprintf("%016x", uint64(n)*0x9e3779b97f4a7c15) }

func scalars(covered int) map[string]ledger.Component {
	return map[string]ledger.Component{
		"api": {
			Coverage: &ledger.Lines{Covered: covered, Total: 1000},
			CRAP:     &ledger.CRAP{Above: 2, Worst: 31.5},
			Tests:    &junit.Counts{Total: 120, Failed: 0, Skipped: 3},
			Findings: map[string]int{"gosec": 1, "govulncheck": 0},
			Producer: "go-test",
		},
		"web": {
			Coverage: &ledger.Lines{Covered: covered / 2, Total: 400},
			Tests:    &junit.Counts{Total: 40},
			Findings: map[string]int{"biome": 2},
			Producer: "vitest",
		},
	}
}

func entry(at time.Time, commit, parent, branch string, covered int, events ...ledger.FindingEvent) ledger.Record {
	return ledger.Record{
		Kind:          ledger.KindEntry,
		At:            at,
		Commit:        commit,
		Parent:        parent,
		Tree:          "t" + commit[1:],
		Branch:        branch,
		Components:    scalars(covered),
		RootFindings:  map[string]int{"semgrep": 1},
		FindingEvents: events,
	}
}

func event(tr ledger.FindingTransition, n int) ledger.FindingEvent {
	return ledger.FindingEvent{
		Transition:  tr,
		Fingerprint: fingerprint(n),
		Gate:        "gosec",
		Component:   "api",
		Path:        fmt.Sprintf("internal/handler/file%d.go", n),
		Rule:        "G104",
	}
}

func appendAll(t *testing.T, root string, recs ...ledger.Record) {
	t.Helper()
	for _, rec := range recs {
		if _, _, err := ledger.Append(root, []ledger.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}
}

func writeSnapshot(t *testing.T, root string, o ledger.OpenFindings) int {
	t.Helper()
	parts, err := ledger.EncodeOpenFindings(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		full := filepath.Join(root, filepath.FromSlash(p.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, p.Data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return len(parts)
}

// buildRolledPartition fills September's first part so the next record rolls
// into 2026-09.1.ndjson, and a later one follows it there.
func buildRolledPartition(t *testing.T, root string) {
	const fill = 880 * 1024
	big := entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700)
	for n := 0; ; n++ {
		b, err := json.Marshal(big)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) >= fill {
			break
		}
		big.FindingEvents = append(big.FindingEvents, event(ledger.FindingAppeared, n))
	}
	next := entry(day(time.September, 2, 9), sha(2), sha(1), "main", 710)
	for n := 0; n < 300; n++ {
		next.FindingEvents = append(next.FindingEvents, event(ledger.FindingResolved, n))
	}
	appendAll(t, root, big, next,
		entry(day(time.September, 3, 9), sha(3), sha(2), "main", 720))
}

// buildUnparseableLine appends a line no reader can parse between two records
// Append wrote.
func buildUnparseableLine(t *testing.T, root string) {
	appendAll(t, root, entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700))
	part := filepath.Join(root, "history", "v1", "2026-09.ndjson")
	f, err := os.OpenFile(part, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"kind\":\"entry\",\"commit\":\"not a complete reco\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	appendAll(t, root, entry(day(time.September, 2, 9), sha(2), sha(1), "main", 710))
}

// buildOutOfOrderResolve appends a resolution before the appearance it
// resolves, and an older commit after a newer one on the same day.
func buildOutOfOrderResolve(t *testing.T, root string) {
	appendAll(t, root,
		entry(day(time.September, 5, 15), sha(3), sha(2), "main", 730, event(ledger.FindingResolved, 1)),
		entry(day(time.September, 5, 9), sha(2), sha(1), "main", 720, event(ledger.FindingAppeared, 1), event(ledger.FindingAppeared, 2)),
		entry(day(time.September, 4, 9), sha(1), sha(0), "main", 710),
	)
}

// buildGap records a gap beside its entry, and a gap on a day that holds no
// entry at all.
func buildGap(t *testing.T, root string) {
	gap := func(at time.Time, commit string, from string, missing int, reason string) ledger.Record {
		return ledger.Record{
			Kind:   ledger.KindGap,
			At:     at,
			Commit: commit,
			Branch: "main",
			Gap:    &ledger.Gap{From: from, Missing: missing, Reason: reason},
		}
	}
	appendAll(t, root,
		entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700),
		entry(day(time.September, 2, 9), sha(5), sha(4), "main", 720),
		gap(day(time.September, 2, 9), sha(5), sha(1), 3, "3 commits between the previous recording and this one were never recorded"),
		gap(day(time.September, 3, 9), sha(6), "", 0, "no earlier recording within the lookback window, so the width of the break is unknown"),
		entry(day(time.September, 4, 9), sha(7), sha(6), "main", 730),
	)
}

// buildReleaseBranch records the same month for a default and a nested branch,
// and snapshots the nested one.
func buildReleaseBranch(t *testing.T, root string) {
	appendAll(t, root,
		entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700, event(ledger.FindingAppeared, 1)),
		entry(day(time.September, 1, 9), sha(1), sha(0), "release/1.x", 690, event(ledger.FindingAppeared, 1)),
		entry(day(time.September, 2, 9), sha(2), sha(1), "main", 710),
		entry(day(time.September, 2, 12), sha(3), sha(1), "release/1.x", 695),
	)
	writeSnapshot(t, root, ledger.OpenFindings{
		Branch: "release/1.x",
		Commit: sha(3),
		At:     day(time.September, 2, 12),
		Findings: []ledger.OpenFinding{{
			Gate: "gosec", Component: "api", Path: "internal/handler/file1.go", Line: 12,
			Rule: "G104", Severity: "medium", Message: "errors unhandled", Fingerprint: fingerprint(1),
		}},
	})
}

func fullFinding(n int) ledger.OpenFinding {
	return ledger.OpenFinding{
		Gate:        "gosec",
		Component:   "api",
		Path:        fmt.Sprintf("internal/handler/file%d.go", n),
		Line:        10 + n,
		EndLine:     12 + n,
		Rule:        "G104",
		Severity:    "medium",
		Message:     "errors unhandled",
		Ordinal:     n % 3,
		Fingerprint: fingerprint(n),
	}
}

func buildSnapshotComplete(t *testing.T, root string) {
	appendAll(t, root, entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700,
		event(ledger.FindingAppeared, 1), event(ledger.FindingAppeared, 2), event(ledger.FindingAppeared, 3)))
	n := writeSnapshot(t, root, ledger.OpenFindings{
		Branch:   "main",
		Commit:   sha(1),
		At:       day(time.September, 1, 9),
		Findings: []ledger.OpenFinding{fullFinding(3), fullFinding(1), fullFinding(2)},
	})
	if n != 1 {
		t.Fatalf("complete snapshot is %d parts, want 1", n)
	}
}

// buildSnapshotMultipart grows the open set until it no longer fits one part.
func buildSnapshotMultipart(t *testing.T, root string) {
	appendAll(t, root, entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700))
	o := ledger.OpenFindings{Branch: "main", Commit: sha(1), At: day(time.September, 1, 9)}
	for {
		for range 200 {
			o.Findings = append(o.Findings, fullFinding(len(o.Findings)))
		}
		parts, err := ledger.EncodeOpenFindings(o)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) > 1 {
			break
		}
	}
	if n := writeSnapshot(t, root, o); n != 2 {
		t.Fatalf("multi-part snapshot is %d parts, want 2", n)
	}
}

// buildSnapshotLagging leaves the snapshot at a commit older than the newest
// ledger entry.
func buildSnapshotLagging(t *testing.T, root string) {
	appendAll(t, root,
		entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700, event(ledger.FindingAppeared, 1)),
		entry(day(time.September, 3, 9), sha(2), sha(1), "main", 710, event(ledger.FindingAppeared, 2)),
	)
	writeSnapshot(t, root, ledger.OpenFindings{
		Branch:   "main",
		Commit:   sha(1),
		At:       day(time.September, 1, 9),
		Findings: []ledger.OpenFinding{fullFinding(1)},
	})
}

// buildSnapshotIdentityOnly snapshots findings that carry fingerprint, gate and
// component and nothing else.
func buildSnapshotIdentityOnly(t *testing.T, root string) {
	appendAll(t, root, entry(day(time.September, 1, 9), sha(1), sha(0), "main", 700,
		event(ledger.FindingAppeared, 1), event(ledger.FindingAppeared, 2)))
	writeSnapshot(t, root, ledger.OpenFindings{
		Branch: "main",
		Commit: sha(1),
		At:     day(time.September, 1, 9),
		Findings: []ledger.OpenFinding{
			{Gate: "gosec", Component: "api", Fingerprint: fingerprint(1)},
			{Gate: "biome", Component: "web", Fingerprint: fingerprint(2)},
		},
	})
}

// listFiles returns every regular file under root, slash-separated and sorted.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	for _, rel := range listFiles(t, from) {
		data, err := os.ReadFile(filepath.Join(from, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(to, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func compareTrees(t *testing.T, want, got string) {
	t.Helper()
	const hint = "; regenerate with: go test ./internal/ledger -run Golden -update"
	wantFiles, gotFiles := listFiles(t, want), listFiles(t, got)
	wantSet := map[string]bool{}
	for _, f := range wantFiles {
		wantSet[f] = true
	}
	gotSet := map[string]bool{}
	for _, f := range gotFiles {
		gotSet[f] = true
		if !wantSet[f] {
			t.Errorf("the writer produced %s, which %s does not hold%s", f, want, hint)
		}
	}
	for _, f := range wantFiles {
		if !gotSet[f] {
			t.Errorf("%s/%s is no longer produced by the writer%s", want, f, hint)
			continue
		}
		a, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs from the writer's output: %s%s", f, firstDifference(a, b), hint)
		}
	}
}

func firstDifference(want, got []byte) string {
	n := min(len(want), len(got))
	i := 0
	for i < n && want[i] == got[i] {
		i++
	}
	excerpt := func(b []byte) string {
		end := min(i+60, len(b))
		return strings.ReplaceAll(string(b[min(i, len(b)):end]), "\n", `\n`)
	}
	return fmt.Sprintf("%d bytes on disk, %d produced; first difference at byte %d: on disk %q, produced %q",
		len(want), len(got), i, excerpt(want), excerpt(got))
}
