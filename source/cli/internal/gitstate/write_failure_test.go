package gitstate

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/ledger"
)

// seedStateBranchFiles is seedStateBranch with the files named by their path
// on the branch, for a test that needs the branch to hold something in the way
// of a write.
func seedStateBranchFiles(t *testing.T, ctx context.Context, files map[string]string) string {
	t.Helper()
	run := gitRunner(t, ctx)
	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main", ".")
	seed := t.TempDir()
	run(seed, "init", "-b", BranchName, ".")
	run(seed, "config", "user.email", "t@t")
	run(seed, "config", "user.name", "t")
	for rel, content := range files {
		path := filepath.Join(seed, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(seed, "add", "-A", "-f")
	run(seed, "commit", "-m", "seed")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", BranchName)
	return origin
}

func recordingWith(open *ledger.OpenFindings) Records {
	at := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
	return func(string) (Recording, error) {
		return Recording{
			Records: []ledger.Record{{Kind: ledger.KindEntry, At: at, Commit: "cafe", Branch: "main",
				Components: map[string]ledger.Component{"api": {Findings: map[string]int{"gosec": 1}}}}},
			OpenFindings: open,
		}, nil
	}
}

func openOn(branch string) *ledger.OpenFindings {
	return &ledger.OpenFindings{Branch: branch, Commit: "cafe", At: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		Findings: []ledger.OpenFinding{{Gate: "gosec", Component: "api", Fingerprint: "v1:a"}}}
}

// Every way a write can fail between the fetch and the push is an error the
// caller sees, and none of them leaves the branch changed.
func TestAWriteThatCannotBeStagedOrCommittedFails(t *testing.T) {
	ctx := context.Background()
	stateDir := strings.Split(StatePath("k"), "/")[0]

	cases := []struct {
		name    string
		origin  map[string]string
		prepare func(t *testing.T, clone string)
		snap    Snapshot
		records Records
		want    string
	}{
		{
			name:    "a CRAP figure that cannot be encoded",
			snap:    Snapshot{CRAP: CRAPBaseline{"api": {Worst: math.NaN()}}},
			records: nil,
			want:    "NaN",
		},
		{
			name:    "a baseline path held by a file",
			origin:  map[string]string{stateDir: "x"},
			snap:    Snapshot{Coverage: Baseline{"api": entry(1, 2)}},
			records: nil,
			want:    "not a directory",
		},
		{
			name:    "an open-findings snapshot for a branch that is no path",
			records: recordingWith(openOn("../x")),
			want:    "cannot be used as a snapshot path",
		},
		{
			name:    "an open-findings directory held by a file",
			origin:  map[string]string{"snapshot": "x"},
			records: recordingWith(openOn("main")),
			want:    "not a directory",
		},
		{
			name:    "a stale snapshot part that cannot be removed",
			origin:  map[string]string{"snapshot/v1/main/findings.1.json/keep": "x"},
			records: recordingWith(openOn("main")),
			want:    "directory not empty",
		},
		{
			name:    "a record path the branch ignores",
			origin:  map[string]string{".gitignore": "history/\n"},
			records: recordingWith(nil),
			want:    "git add",
		},
		{
			name: "a commit hook that refuses",
			prepare: func(t *testing.T, clone string) {
				hooks := t.TempDir()
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- a hook this test needs to be executable
					t.Fatal(err)
				}
				gitRunner(t, ctx)(clone, "config", "core.hooksPath", hooks)
			},
			records: recordingWith(nil),
			want:    "git commit",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{"README": "state branch"}
			for k, v := range c.origin {
				files[k] = v
			}
			clone := cloneOf(t, ctx, seedStateBranchFiles(t, ctx, files))
			if c.prepare != nil {
				c.prepare(t, clone)
			}
			before := revCount(t, ctx, clone)
			_, err := Write(ctx, clone, "tree1", c.snap, c.records)
			if err == nil {
				t.Fatal("Write reported success")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to contain %q", err, c.want)
			}
			if got := revCount(t, ctx, clone); got != before {
				t.Errorf("the write added %d commits despite failing", got-before)
			}
		})
	}
}

// A temporary directory that cannot be made fails the write before it touches
// the repository.
func TestAWriteWithNowhereToStageFails(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := Write(ctx, clone, "tree1", Snapshot{Coverage: Baseline{"api": entry(1, 2)}}, nil); err == nil {
		t.Fatal("Write reported success with no temporary directory")
	}
}

// A directory that is not a repository cannot host the staging worktree.
func TestAWriteOutsideARepositoryFails(t *testing.T) {
	ctx := context.Background()
	if _, err := Write(ctx, t.TempDir(), "tree1", Snapshot{Coverage: Baseline{"api": entry(1, 2)}}, nil); err == nil {
		t.Fatal("Write reported success outside a repository")
	}
}
