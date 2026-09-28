package ledgerstages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// ledgerGit runs git in dir and fails the test when it does not succeed.
func ledgerGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	r := executil.RunQuiet(context.Background(), dir, "git", args...)
	if !r.Ok() {
		t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
	}
	return strings.TrimSpace(r.Output)
}

// ledgerRepo is a repository with one commit holding files, and the tree
// that commit points at.
func ledgerRepo(t *testing.T, files map[string]string) (dir, tree string) {
	t.Helper()
	dir = t.TempDir()
	ledgerGit(t, dir, "init", "--quiet", "-b", "main")
	ledgerGit(t, dir, "config", "user.email", "t@example.com")
	ledgerGit(t, dir, "config", "user.name", "t")
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ledgerGit(t, dir, "add", "-A")
	ledgerGit(t, dir, "commit", "--quiet", "-m", "measured")
	return dir, ledgerGit(t, dir, "rev-parse", "HEAD^{tree}")
}

// ledgerRemote is a repository with one commit pushed to a file:// origin —
// the remote the state branch is fetched from — and the tree it checks out.
func ledgerRemote(t *testing.T) (dir, tree string) {
	t.Helper()
	dir, tree = ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	origin := t.TempDir()
	ledgerGit(t, origin, "init", "--quiet", "--bare", "-b", "main", ".")
	ledgerGit(t, dir, "remote", "add", "origin", "file://"+origin)
	ledgerGit(t, dir, "push", "--quiet", "-u", "origin", "main")
	return dir, tree
}

// ledgerCommit commits every change in dir and describes the commit made.
func ledgerCommit(t *testing.T, dir, message string) gitstate.Commit {
	t.Helper()
	ledgerGit(t, dir, "add", "-A")
	ledgerGit(t, dir, "commit", "--quiet", "--allow-empty", "-m", message)
	c, err := gitstate.DescribeCommit(context.Background(), dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ledgerEntry is a coverage entry of covered lines out of total, measured by
// the one instrument every fixture here uses.
func ledgerEntry(covered, total int) gitstate.Entry {
	return gitstate.Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}, Producer: "go 1.26"}
}

// ledgerStore is a ledger directory holding one entry per commit named, on
// branch main, each filed an hour apart and the last an hour before now.
func ledgerStore(t *testing.T, now time.Time, commits ...string) string {
	t.Helper()
	store := t.TempDir()
	for i, commit := range commits {
		rec := ledger.Record{
			Kind:       ledger.KindEntry,
			At:         now.Add(-time.Duration(len(commits)-i) * time.Hour),
			Commit:     commit,
			Branch:     "main",
			Components: map[string]ledger.Component{"svc": {Tests: &junit.Counts{Total: 1}}},
		}
		if _, _, err := ledger.Append(store, []ledger.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// ledgerScalar is components carrying one scalar, which is enough for a
// recording to have history to append.
func ledgerScalar() map[string]ledger.Component {
	return map[string]ledger.Component{"svc": {Tests: &junit.Counts{Total: 3}}}
}

func ledgerGosecClaim(path, site string) finding.Finding {
	return finding.Finding{Gate: "gosec", Component: "cli", Path: path, Rule: "G101", Site: site}
}

// ledgerFindingEvents is the finding events the record this commit appends
// carries, diffed against a ledger already holding prior.
//
// The ledger is a directory of its own rather than the state branch, because
// what is under test is the diff against whatever the fetched branch holds,
// and the closure ComposeRecords returns is handed exactly that directory.
// Every prior record is filed an hour before this commit, which is what makes
// it history rather than this commit's own events read back.
func ledgerFindingEvents(t *testing.T, components map[string]ledger.Component, root map[string]int,
	scope map[ledger.FindingBucket]bool, found []finding.Finding, prior ...[]ledger.FindingEvent) []ledger.FindingEvent {
	t.Helper()
	repo, _ := ledgerRepo(t, map[string]string{"README.md": "a repository\n"})
	head, err := gitstate.DescribeCommit(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	store := t.TempDir()
	for i, events := range prior {
		rec := ledger.Record{
			Kind:          ledger.KindEntry,
			At:            head.At.Add(-time.Duration(len(prior)-i) * time.Hour),
			Commit:        "prior" + string(rune('a'+i)),
			Branch:        "main",
			FindingEvents: events,
		}
		if _, _, err := ledger.Append(store, []ledger.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := ComposeRecords(context.Background(), ComposeRecordsIn{
		Dir: repo, BranchOverride: "main", Components: components,
		RootFindings: root, Scope: scope, Found: found,
	})
	if err != nil {
		t.Fatalf("ComposeRecords: %v", err)
	}
	if out.Records == nil {
		t.Fatalf("no record to append: reason %#v", out.Reason)
	}
	recs, err := out.Records(store)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.Kind == ledger.KindEntry {
			return rec.FindingEvents
		}
	}
	t.Fatalf("no entry among %+v", recs)
	return nil
}
