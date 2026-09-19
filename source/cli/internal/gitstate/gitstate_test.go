package gitstate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/junit"
	"lydite/lydite/internal/ledger"
)

// An empty baseline must read back as a cache MISS, not as a baseline of
// nothing. coverage.Compute silently omits any language whose tooling it
// couldn't run, so a runner missing (say) cargo-llvm-cov computes `{}` — and
// once that lands on lydite it is indistinguishable from a real entry:
// every later PR hits it, reports every language as [NEW], and the gate
// enforces nothing, silently and forever. wardnet accumulated nine of these.
// Treating `{}` as a miss is what heals the already-written ones without a
// manual purge of the branch.
func TestReadBaselineTreatsEmptyAsCacheMiss(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()
	clone := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.Run(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v", args, r.Err)
		}
	}

	// A bare origin carrying a lydite branch with one empty and one
	// populated baseline — the exact shape wardnet's branch is in.
	run(origin, "init", "--bare", "-b", "main", ".")
	seed := t.TempDir()
	run(seed, "init", "-b", BranchName, ".")
	run(seed, "config", "user.email", "t@t")
	run(seed, "config", "user.name", "t")
	for key, content := range map[string]string{
		"empty":  "{}",
		"filled": `{"api":{"covered":117,"total":200}}`,
	} {
		path := filepath.Join(seed, filepath.FromSlash(StatePath(key)))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "baselines")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", BranchName)

	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)

	if snap, err := ReadSnapshot(ctx, clone, "empty"); err != nil || snap.Recorded() {
		t.Errorf("ReadSnapshot on an empty {} baseline: recorded=%v err=%v, want a cache miss", snap.Recorded(), err)
	}

	snap, err := ReadSnapshot(ctx, clone, "filled")
	report, hit := snap.Coverage, snap.Recorded()
	if err != nil {
		t.Fatalf("ReadSnapshot: %v", err)
	}
	if !hit || report["api"] != entry(117, 200) {
		t.Errorf("ReadSnapshot on a real baseline = (%v, hit=%v), want ({api:{117 200}}, hit=true)", report, hit)
	}
}

// gitRunner returns a t.Fatal-ing git helper bound to ctx, mirroring the
// inline helper the test above uses.
func gitRunner(t *testing.T, ctx context.Context) func(dir string, args ...string) {
	t.Helper()
	return func(dir string, args ...string) {
		t.Helper()
		if r := executil.Run(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
}

// seedStateBranch creates a bare origin whose lydite branch carries the
// given files, and returns the origin path.
func seedStateBranch(t *testing.T, ctx context.Context, files map[string]string) string {
	t.Helper()
	run := gitRunner(t, ctx)
	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main", ".")
	seed := t.TempDir()
	run(seed, "init", "-b", BranchName, ".")
	run(seed, "config", "user.email", "t@t")
	run(seed, "config", "user.name", "t")
	for key, content := range files {
		path := filepath.Join(seed, filepath.FromSlash(StatePath(key)))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "baselines")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", BranchName)
	return origin
}

// A revision the caller states is resolved in the checkout as it stands: no
// remote is configured here, so a resolution that fetched anything would fail
// rather than answer.
func TestAnExplicitRevisionIsResolvedWithoutAnyRemote(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	dir := t.TempDir()
	run(dir, "init", "-b", "main", ".")
	run(dir, "config", "user.email", "t@t")
	run(dir, "config", "user.name", "t")
	for _, message := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(dir, message), []byte(message), 0o600); err != nil {
			t.Fatal(err)
		}
		run(dir, "add", "-A")
		run(dir, "commit", "-m", message)
	}

	want, err := ResolveRevision(ctx, dir, "HEAD~1")
	if err != nil {
		t.Fatalf("a relative ref did not resolve: %v", err)
	}
	for _, revision := range []string{want, want[:8], "main~1"} {
		got, err := ResolveRevision(ctx, dir, revision)
		if err != nil {
			t.Fatalf("%s did not resolve: %v", revision, err)
		}
		if got != want {
			t.Errorf("%s resolved to %s, want %s", revision, got, want)
		}
	}

	// A revision this checkout does not hold is an error naming it and the
	// fix, never the empty string a caller would then mutate nothing against.
	for _, revision := range []string{"HEAD~9", "0000000000000000000000000000000000000000"} {
		got, err := ResolveRevision(ctx, dir, revision)
		if err == nil {
			t.Fatalf("%s resolved to %q", revision, got)
		}
		if got != "" {
			t.Errorf("a failed resolution answered %q as well as an error", got)
		}
		if !strings.Contains(err.Error(), revision) || !strings.Contains(err.Error(), "depth 0") {
			t.Errorf("error = %q, want it to name %s and the fix", err, revision)
		}
	}
	if _, err := ResolveRevision(ctx, dir, ""); err == nil || !strings.Contains(err.Error(), BaseSHAFlag) {
		t.Errorf("an empty revision gave %v, want an error naming %s", err, BaseSHAFlag)
	}
}

func TestWritePushesOverAStaleTrackingRef(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	origin := seedStateBranch(t, ctx, map[string]string{"first": `{"api":{"covered":10,"total":100}}`})

	// The caller's repo: fetches lydite once, then the remote advances.
	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)
	run(clone, "fetch", "origin", BranchName)

	// A concurrent run records a different SHA's baseline in the meantime.
	writer := t.TempDir()
	run(writer, "clone", "-b", BranchName, origin, ".")
	run(writer, "config", "user.email", "t@t")
	run(writer, "config", "user.name", "t")
	concurrent := filepath.Join(writer, filepath.FromSlash(StatePath("concurrent")))
	if err := os.MkdirAll(filepath.Dir(concurrent), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(concurrent, []byte(`{"api":{"covered":20,"total":100}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run(writer, "add", "-A")
	run(writer, "commit", "-m", "coverage baseline for concurrent")
	run(writer, "push", "origin", BranchName)

	if _, err := Write(ctx, clone, "stalerace", Snapshot{Coverage: Baseline{"api": entry(30, 100)}}, nil); err != nil {
		t.Fatalf("Write over a stale tracking ref: %v", err)
	}

	// Both the concurrent write and ours must be on the remote branch.
	verify := t.TempDir()
	run(verify, "clone", "-b", BranchName, origin, ".")
	for _, key := range []string{"first", "concurrent", "stalerace"} {
		if _, err := os.Stat(filepath.Join(verify, filepath.FromSlash(StatePath(key)))); err != nil {
			t.Errorf("%s missing from %s after Write: %v", StatePath(key), BranchName, err)
		}
	}
}

// A push that never lands must surface as an error so the caller can say
// "failed to record" instead of the misleading "recorded coverage baseline"
// wardnet's main run printed while the baseline was in fact lost.
func TestWriteReportsAPushThatNeverLands(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	origin := seedStateBranch(t, ctx, map[string]string{"first": `{"api":{"covered":10,"total":100}}`})

	// Reject every push from here on.
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)
	run(clone, "fetch", "origin", BranchName)

	if _, err := Write(ctx, clone, "rejected", Snapshot{Coverage: Baseline{"api": entry(30, 100)}}, nil); err == nil {
		t.Error("Write returned nil even though the push was rejected and the baseline never landed")
	}
}

// The property the whole tree-keying change rests on, tested directly rather
// than assumed: a squash merge lands a commit whose tree is the merged tree the
// pull request was built from. That is what lets a measurement taken on a pull
// request serve as the baseline for the commit it becomes.
//
// Verified against a real merge before this was written — tumika#25's gate
// recorded tree 783e0a44… and the squash commit that landed carried the same
// tree object — but a repository is cheap to build here, and this keeps the
// assumption honest if git's behaviour ever shifts.
func TestSquashMergePreservesTheMergedTree(t *testing.T) {
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.Run(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}

	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "base")

	run(repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "one")
	if err := os.WriteFile(filepath.Join(repo, "c.txt"), []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "two")

	// What GitHub builds for a pull request: refs/pull/N/merge, the merged
	// result of the branch into its base.
	run(repo, "checkout", "main")
	run(repo, "merge", "--no-ff", "-m", "merge ref", "feature")
	mergeTree, err := TreeSHA(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// What a squash merge lands: one commit on main carrying the same content.
	run(repo, "reset", "--hard", "HEAD~1")
	run(repo, "merge", "--squash", "feature")
	run(repo, "commit", "-m", "squashed")
	squashTree, err := TreeSHA(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if mergeTree != squashTree {
		t.Fatalf("merge tree %s != squash tree %s — a pull request's measurement "+
			"would not describe the commit it becomes, and tree-keyed baselines "+
			"would silently never hit", mergeTree, squashTree)
	}
}

// Tree keying exists so a measurement taken on a pull request is still there
// when that tree becomes main. Read must therefore find an entry written under
// the tree, and must still find pre-existing entries written under a commit
// SHA — or every repository recomputes on the first run after the change.
func TestReadBaselinePrefersTheTreeAndFallsBackToTheCommit(t *testing.T) {
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.Run(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}

	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main", ".")

	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "c")
	run(repo, "remote", "add", "origin", origin)
	run(repo, "push", "-u", "origin", "main")

	head, err := HeadSHA(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := TreeSHA(ctx, repo, head)
	if err != nil {
		t.Fatal(err)
	}

	// Only a commit-keyed entry, as written before this change.
	if _, err := Write(ctx, repo, head, Snapshot{Coverage: Baseline{"api": entry(11, 100)}}, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(ctx, repo, tree, head)
	got, hit := snap.Coverage, snap.Recorded()
	if err != nil || !hit {
		t.Fatalf("ReadSnapshot(tree, commit) hit=%v err=%v, want the legacy commit entry found", hit, err)
	}
	if got["api"].Covered != 11 {
		t.Errorf("api = %v, want the commit-keyed 11", got["api"])
	}

	// Now a tree-keyed entry as well: it must win, because it is the one a
	// pull request records and the one a later main commit shares.
	if _, err := Write(ctx, repo, tree, Snapshot{Coverage: Baseline{"api": entry(77, 100)}}, nil); err != nil {
		t.Fatal(err)
	}
	snap, err = ReadSnapshot(ctx, repo, tree, head)
	got, hit = snap.Coverage, snap.Recorded()
	if err != nil || !hit {
		t.Fatalf("ReadSnapshot hit=%v err=%v, want the tree entry found", hit, err)
	}
	if got["api"].Covered != 77 {
		t.Errorf("api = %v, want the tree-keyed 77 to take precedence over the commit-keyed 11", got["api"])
	}
}

// The premise of versioning stateDir is that an entry recorded under a
// superseded metric is never read as the current one. A baseline sitting at
// the branch root — where entries predating the version-keyed layout live —
// must therefore be a clean cache miss, not a hit whose number means
// something else.
func TestReadBaselineIgnoresEntriesOutsideTheStateDir(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)

	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "main", ".")
	seed := t.TempDir()
	run(seed, "init", "-b", BranchName, ".")
	run(seed, "config", "user.email", "t@t")
	run(seed, "config", "user.name", "t")
	// Deliberately at the branch root, not under StatePath's directory.
	if err := os.WriteFile(filepath.Join(seed, "deadbeef.json"), []byte(`{"api":{"covered":58,"total":100}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "baseline under the superseded layout")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", BranchName)

	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)

	if snap, err := ReadSnapshot(ctx, clone, "deadbeef"); err != nil || snap.Recorded() {
		t.Errorf("ReadSnapshot = (%v, recorded=%v, err=%v), want a cache miss — the entry is outside %s", snap.Coverage, snap.Recorded(), err, StatePath(""))
	}
}

// The default branch is discovered, not assumed. A repository whose default
// branch is `master` was refused outright by every gate that resolves a
// merge-base — the coverage baseline, affected selection, `scan --diff-base
// auto` and the referral diff all come through here — with an error that
// named neither the cause nor a fix.
//
// Discovery is deliberately not "try main, then master": a repository
// carrying both has not said which one is the default, and picking by
// precedence measures a change against a branch nobody chose. Every failure
// names the flag.
func TestBaseBranchIsDiscovered(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)

	// origin holds the branches; clone is where discovery runs.
	//
	// headless makes the remote report no HEAD, by pointing it at a branch
	// that does not exist. That is the only state in which the main/master
	// candidates are consulted at all — every ordinary remote answers `git
	// ls-remote --symref HEAD` with its own default, which is why the guess
	// below it is a last resort rather than the mechanism.
	newRepoWithHead := func(noHead bool, branches ...string) string {
		origin := t.TempDir()
		run(origin, "init", "--bare", "-b", branches[0], ".")
		seed := t.TempDir()
		run(seed, "init", "-b", branches[0], ".")
		run(seed, "config", "user.email", "t@t")
		run(seed, "config", "user.name", "t")
		if err := os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(seed, "add", "-A")
		run(seed, "commit", "-m", "seed")
		run(seed, "remote", "add", "origin", origin)
		for _, b := range branches {
			run(seed, "push", "origin", branches[0]+":"+b)
		}
		if noHead {
			run(origin, "symbolic-ref", "HEAD", "refs/heads/does-not-exist")
		}
		clone := t.TempDir()
		run(clone, "clone", "--no-checkout", origin, ".")
		// actions/checkout leaves no origin/HEAD, which is what makes the
		// remote's own answer the path CI actually takes. Removing it here is
		// what reproduces that.
		_ = executil.Run(ctx, clone, "git", "remote", "set-head", "origin", "--delete")
		return clone
	}
	// The ordinary remote: it states its own default.
	newRepo := func(branches ...string) string { return newRepoWithHead(false, branches...) }
	// A remote that reports no HEAD, so discovery falls to the candidates.
	headless := func(branches ...string) string { return newRepoWithHead(true, branches...) }

	t.Run("master alone is found", func(t *testing.T) {
		got, err := BaseBranch(ctx, headless("master"), "")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "master" {
			t.Errorf("BaseBranch = %q, want %q", got, "master")
		}
	})

	t.Run("main alone is found", func(t *testing.T) {
		got, err := BaseBranch(ctx, headless("main"), "")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "main" {
			t.Errorf("BaseBranch = %q, want %q", got, "main")
		}
	})

	t.Run("the remote's own HEAD outranks the candidates", func(t *testing.T) {
		// A repository whose default is `develop` and which still carries a
		// stale `master`. The candidate list has exactly one answer here and
		// it is the wrong one, so a discovery that stopped at the list would
		// silently measure every change against a branch nobody chose.
		got, err := BaseBranch(ctx, newRepo("develop", "master"), "")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "develop" {
			t.Errorf("BaseBranch = %q, want %q — the remote states its own default", got, "develop")
		}
	})

	t.Run("the remote's own HEAD outranks the candidates", func(t *testing.T) {
		// A repository whose default is `develop` and which still carries a
		// stale `master`. The candidate list has exactly one answer here and
		// it is the wrong one, so a discovery that stopped at the list would
		// silently measure every change against a branch nobody chose.
		got, err := BaseBranch(ctx, newRepo("develop", "master"), "")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "develop" {
			t.Errorf("BaseBranch = %q, want %q — the remote states its own default", got, "develop")
		}
	})

	t.Run("a branch merely containing a candidate is not one", func(t *testing.T) {
		got, err := BaseBranch(ctx, headless("master", "not-main"), "")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "master" {
			t.Errorf("BaseBranch = %q, want %q — refs are compared whole", got, "master")
		}
	})

	t.Run("both, on a remote reporting no HEAD, is an error naming the flag", func(t *testing.T) {
		_, err := BaseBranch(ctx, headless("main", "master"), "")
		if err == nil {
			t.Fatal("BaseBranch resolved a repository carrying both main and master")
		}
		if !strings.Contains(err.Error(), BaseBranchFlag) {
			t.Errorf("error %q does not name %s", err, BaseBranchFlag)
		}
	})

	t.Run("neither, on a remote reporting no HEAD, is an error naming the flag", func(t *testing.T) {
		_, err := BaseBranch(ctx, headless("trunk"), "")
		if err == nil {
			t.Fatal("BaseBranch resolved a repository with neither main nor master")
		}
		if !strings.Contains(err.Error(), BaseBranchFlag) {
			t.Errorf("error %q does not name %s", err, BaseBranchFlag)
		}
	})

	t.Run("an override outranks discovery", func(t *testing.T) {
		// A repository discovery would refuse, which is what shows the
		// override is consulted before anything is looked up.
		got, err := BaseBranch(ctx, headless("main", "master"), "trunk")
		if err != nil {
			t.Fatalf("BaseBranch: %v", err)
		}
		if got != "trunk" {
			t.Errorf("BaseBranch = %q, want %q", got, "trunk")
		}
	})
}

// origin/HEAD is git's own record of the remote's default branch, and it
// outranks the listing: a repository whose default is `develop` while main
// also exists resolves to develop, where the listing alone would answer main.
func TestBaseBranchPrefersOriginHead(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	origin := t.TempDir()
	run(origin, "init", "--bare", "-b", "develop", ".")
	seed := t.TempDir()
	run(seed, "init", "-b", "develop", ".")
	run(seed, "config", "user.email", "t@t")
	run(seed, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(seed, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(seed, "add", "-A")
	run(seed, "commit", "-m", "seed")
	run(seed, "remote", "add", "origin", origin)
	run(seed, "push", "origin", "develop")
	run(seed, "push", "origin", "develop:main")
	clone := t.TempDir()
	run(clone, "clone", "--no-checkout", origin, ".")

	got, err := BaseBranch(ctx, clone, "")
	if err != nil {
		t.Fatalf("BaseBranch: %v", err)
	}
	if got != "develop" {
		t.Errorf("BaseBranch = %q, want %q — origin/HEAD is authoritative where it is set", got, "develop")
	}
}

// A lookup with no usable key found nothing, which is a cache miss. The zero
// executil.Result reports Ok, so without an explicit check the unmarshal below
// runs on an empty string and answers with a parse error — a hard failure
// where the question was only whether an entry exists.
func TestReadBaselineWithNoUsableKeyIsAMiss(t *testing.T) {
	ctx := context.Background()
	origin := seedStateBranch(t, ctx, map[string]string{"first": `{"api":{"covered":10,"total":100}}`})
	run := gitRunner(t, ctx)
	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)

	for _, keys := range [][]string{nil, {""}, {"", ""}} {
		snap, err := ReadSnapshot(ctx, clone, keys...)
		report, hit := snap.Coverage, snap.Recorded()
		if err != nil {
			t.Errorf("ReadSnapshot(%v) = %v, want a miss rather than an error", keys, err)
		}
		if hit || report != nil {
			t.Errorf("ReadSnapshot(%v) = (%v, hit=%v), want a miss", keys, report, hit)
		}
	}
}

// An unreadable cached entry is a miss, and never an error. Nothing rewrites
// the base tree's entry — a run records the tree it measured — so returning an
// error red-lines the gate for every change whose merge-base is that tree,
// permanently and with no way back. A hand-edit, a truncated object or an entry
// from a format this version does not know would all do it. A miss recomputes
// and overwrites, which is the self-healing the empty-entry rule already has.
func TestAnUnreadableBaselineIsAMissRatherThanAnError(t *testing.T) {
	ctx := context.Background()
	origin := seedStateBranch(t, ctx, map[string]string{
		"broken": `{"api":{"covered":`,
		"good":   `{"api":{"covered":10,"total":100}}`,
	})
	run := gitRunner(t, ctx)
	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)

	snap, err := ReadSnapshot(ctx, clone, "broken")
	report, hit := snap.Coverage, snap.Recorded()
	if err != nil {
		t.Errorf("ReadSnapshot on a truncated entry returned %v, want a miss", err)
	}
	if hit || report != nil {
		t.Errorf("ReadSnapshot = (%v, hit=%v), want a miss", report, hit)
	}
	// A readable entry beside it is unaffected, so this did not buy the
	// healing by treating everything as absent.
	if snap, err := ReadSnapshot(ctx, clone, "good"); err != nil || !snap.Recorded() {
		t.Errorf("ReadSnapshot on a good entry = (recorded=%v, %v), want a hit", snap.Recorded(), err)
	}
}

// entry builds a baseline entry with no producer, which is what a test about
// storage and retrieval is asking about — the producer is compared by the
// gate, and nothing here is a gate.
func entry(covered, total int) Entry {
	return Entry{LineCount: coverage.LineCount{Covered: covered, Total: total}}
}

// The metrics land in one commit and read back independently. One commit
// because they describe the same tree and are recorded by the same job, so two
// pushes would double the retry loop and leave a window where the branch holds
// half a measurement. Independently because a repository upgrading to a lydite
// that computes CRAP has a coverage baseline and no CRAP one, and a miss there
// must not cost it the coverage baseline it already has.
func TestEveryMetricLandsInOneCommitAndMissesOnItsOwn(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	origin := seedStateBranch(t, ctx, map[string]string{"coverageonly": `{"api":{"covered":10,"total":100}}`})

	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)

	// The tree the seed recorded coverage for has no CRAP entry, which is
	// every repository's state the first time it runs a lydite that computes
	// one.
	seeded, err := ReadSnapshot(ctx, clone, "coverageonly")
	if err != nil || len(seeded.Coverage) == 0 {
		t.Fatalf("the coverage baseline = (%v, %v), want a hit", seeded.Coverage, err)
	}
	if len(seeded.CRAP) != 0 {
		t.Errorf("the CRAP baseline = %v, want a miss of its own", seeded.CRAP)
	}

	before := revCount(t, ctx, clone)
	if _, err := Write(ctx, clone, "both", Snapshot{
		Coverage: Baseline{"api": entry(30, 100)},
		CRAP:     CRAPBaseline{"api": {Above: 2, Worst: 156.25, Producer: "go1.26.6"}},
	}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := revCount(t, ctx, clone) - before; got != 1 {
		t.Errorf("the write added %d commits to %s, want 1 carrying both documents", got, BranchName)
	}
	both, err := ReadSnapshot(ctx, clone, "both")
	if err != nil || both.CRAP["api"] != (CRAPEntry{Above: 2, Worst: 156.25, Producer: "go1.26.6"}) {
		t.Errorf("the CRAP baseline = (%v, %v), want what was written", both.CRAP, err)
	}
	if both.Coverage["api"] != entry(30, 100) {
		t.Errorf("the coverage baseline = %v, want what was written beside it", both.Coverage)
	}

	// A metric with nothing to record writes no document, rather than an
	// empty object every reader treats as a miss anyway — which for a
	// repository lydite computes no CRAP for would be one such file per tree,
	// forever.
	if _, err := Write(ctx, clone, "nocrap", Snapshot{Coverage: Baseline{"api": entry(40, 100)}}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	verify := t.TempDir()
	run(verify, "clone", "-b", BranchName, origin, ".")
	if _, err := os.Stat(filepath.Join(verify, filepath.FromSlash(CRAPStatePath("nocrap")))); err == nil {
		t.Errorf("%s was written for a snapshot holding no CRAP entry", CRAPStatePath("nocrap"))
	}
}

// revCount is how many commits the remote's state branch carries.
func revCount(t *testing.T, ctx context.Context, dir string) int {
	t.Helper()
	if r := executil.RunQuiet(ctx, dir, "git", "fetch", "origin", BranchName); !r.Ok() {
		t.Fatalf("fetch: %v", r.Err)
	}
	r := executil.RunQuiet(ctx, dir, "git", "rev-list", "--count", "origin/"+BranchName)
	if !r.Ok() {
		t.Fatalf("rev-list: %v", r.Err)
	}
	n := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(r.Output), "%d", &n); err != nil {
		t.Fatalf("rev-list output %q: %v", r.Output, err)
	}
	return n
}

// A snapshot holding nothing has recorded nothing. The predicate is what
// decides whether a recording merges onto what a tree already holds or writes
// afresh, and asked as a length it reads as true for the empty snapshot — which
// is the one case it exists to answer no to.
func TestAnEmptySnapshotHasRecordedNothing(t *testing.T) {
	t.Parallel()
	if (Snapshot{}).Recorded() {
		t.Error("an empty snapshot reports something recorded")
	}
	// A repository lydite scores no component of records no CRAP document at
	// all, so a snapshot carrying one and not the other is still a recording.
	if !(Snapshot{Coverage: Baseline{"api": entry(1, 2)}}).Recorded() {
		t.Error("a snapshot holding a coverage baseline reports nothing recorded")
	}
	// A CRAP document with no readable coverage one is still a recording:
	// the question is whether there is something to merge onto, and there is —
	// a recording that skipped it would leave that document holding entries
	// for components the declaration no longer has.
	if !(Snapshot{CRAP: CRAPBaseline{"api": {Above: 1}}}).Recorded() {
		t.Error("a snapshot holding a CRAP baseline reports nothing recorded")
	}
}

// The baseline and the history land in ONE commit. Two writes would double the
// retry loop against a shared, busy branch and leave a window where the branch
// holds one half of a recording — and a second writer is a second place state
// can reach the branch, which is the invariant `lydite test record` rests on.
func TestABaselineAndItsHistoryLandInOneCommit(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)

	before := revCount(t, ctx, clone)
	rec := ledger.Record{
		Kind: ledger.KindEntry, At: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		Commit: "cafe", Branch: "main",
		Components: map[string]ledger.Component{"api": {Coverage: &ledger.Lines{Covered: 30, Total: 100}}},
	}
	_, err := Write(ctx, clone, "tree1", Snapshot{Coverage: Baseline{"api": entry(30, 100)}},
		func(string) ([]ledger.Record, error) { return []ledger.Record{rec}, nil })
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := revCount(t, ctx, clone) - before; got != 1 {
		t.Errorf("the write added %d commits to %s, want 1 carrying the baseline and the record", got, BranchName)
	}
	for _, path := range []string{StatePath("tree1"), ledger.Dir + "/2026-03.ndjson", ledger.Dir + "/daily/main/2026.ndjson"} {
		if r := executil.RunQuiet(ctx, clone, "git", "show", "origin/"+BranchName+":"+path); !r.Ok() {
			t.Errorf("%s missing from %s after the write", path, BranchName)
		}
	}
}

// A recording with no baseline still lands its history. The two are different
// policies over one branch: a baseline is refused whenever it would be
// partial, and what a run measured happened whether or not it adds up to one —
// so a run whose suites went red records its test counts and no entry.
func TestHistoryLandsWithNoBaselineBesideIt(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)
	rec := ledger.Record{
		Kind: ledger.KindEntry, At: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC),
		Commit: "cafe", Branch: "main",
		Components: map[string]ledger.Component{"api": {Tests: &junit.Counts{Total: 40, Failed: 3}}},
	}
	_, err := Write(ctx, clone, "tree1", Snapshot{},
		func(string) ([]ledger.Record, error) { return []ledger.Record{rec}, nil })
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if r := executil.RunQuiet(ctx, clone, "git", "show", "origin/"+BranchName+":"+ledger.Dir+"/2026-03.ndjson"); !r.Ok() {
		t.Fatalf("the history did not land without a baseline beside it")
	}
	if r := executil.RunQuiet(ctx, clone, "git", "show", "origin/"+BranchName+":"+StatePath("tree1")); r.Ok() {
		t.Error("an empty snapshot wrote a baseline document")
	}
}

// The records are asked for once per push attempt, against the branch as that
// attempt fetched it. An answer computed once, ahead of the retry loop, would
// have a retry write its append against the first attempt's view — silently
// dropping whatever a concurrent run landed in between, and declaring a gap
// that run had just filled.
func TestTheRecordsAreRecomputedForEveryAttempt(t *testing.T) {
	ctx := context.Background()
	clone := originAndCloneRejectingPushes(t, ctx)
	asked := 0
	_, err := Write(ctx, clone, "tree1", Snapshot{Coverage: Baseline{"api": entry(30, 100)}},
		func(string) ([]ledger.Record, error) {
			asked++
			return nil, nil
		})
	if err == nil {
		t.Fatal("Write reported success even though every push was rejected")
	}
	if asked < 2 {
		t.Errorf("the records were asked for %d time(s) across the retries, want one per attempt", asked)
	}
}

// A caller with nothing to say passes nil, which is not the same as a caller
// whose records were all already on the branch — and a nil must not be a write
// of its own.
func TestARecordingWithNothingToSayWritesNothing(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)
	before := revCount(t, ctx, clone)
	if _, err := Write(ctx, clone, "tree1", Snapshot{}, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := revCount(t, ctx, clone); got != before {
		t.Errorf("a recording with nothing in it added %d commits", got-before)
	}
}

// originAndClone is a seeded state branch and a working repository pointed at
// it, which is what every write test needs before it can write anything.
func originAndClone(t *testing.T, ctx context.Context) string {
	t.Helper()
	return cloneOf(t, ctx, seedStateBranch(t, ctx, map[string]string{"seed": `{"api":{"covered":10,"total":100}}`}))
}

// originAndCloneRejectingPushes is the same, with the remote refusing every
// push — the shape a branch under contention has when a run loses the race
// every time.
func originAndCloneRejectingPushes(t *testing.T, ctx context.Context) string {
	t.Helper()
	origin := seedStateBranch(t, ctx, map[string]string{"seed": `{"api":{"covered":10,"total":100}}`})
	hook := filepath.Join(origin, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { // #nosec G306 -- a hook this test needs to be executable
		t.Fatal(err)
	}
	return cloneOf(t, ctx, origin)
}

func cloneOf(t *testing.T, ctx context.Context, origin string) string {
	t.Helper()
	run := gitRunner(t, ctx)
	clone := t.TempDir()
	run(clone, "init", "-b", "main", ".")
	run(clone, "remote", "add", "origin", origin)
	run(clone, "fetch", "origin", BranchName)
	return clone
}

// A gap is measured from the newest recorded ancestor, so how many commits lie
// between it and this one is the one thing a reader of the partition cannot
// work out alone — and the honest answer when the two are not on one line is
// that it cannot be established, never a number.
func TestCommitsBetweenCountsOnlyAlongAnAncestryChain(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	commit := func(msg string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(msg), 0o600); err != nil {
			t.Fatal(err)
		}
		run(repo, "add", "-A")
		run(repo, "commit", "-m", msg)
		r := executil.RunQuiet(ctx, repo, "git", "rev-parse", "HEAD")
		if !r.Ok() {
			t.Fatal(r.Err)
		}
		return strings.TrimSpace(r.Output)
	}
	first := commit("one")
	commit("two")
	third := commit("three")
	fourth := commit("four")

	// Two commits lie strictly between, which is what the gap record reports.
	if n, ok := CommitsBetween(ctx, repo, first, fourth); !ok || n != 2 {
		t.Errorf("CommitsBetween(first, fourth) = (%d, %v), want (2, true)", n, ok)
	}
	// An adjacent pair has nothing between it and is still an answer: that is
	// what tells a merge whose first parent is not the recorded commit from a
	// hole with commits in it, and the two get different gap reasons.
	if n, ok := CommitsBetween(ctx, repo, third, fourth); !ok || n != 0 {
		t.Errorf("CommitsBetween(third, fourth) = (%d, %v), want (0, true)", n, ok)
	}
	// A range with no commits in it at all is not an answer.
	if n, ok := CommitsBetween(ctx, repo, first, first); ok || n != 0 {
		t.Errorf("CommitsBetween(first, first) = (%d, %v), want (0, false) — the range is empty", n, ok)
	}

	// An unrelated history is what a force-push looks like from here, and its
	// width cannot be established. A number invented for it would be worse
	// than the honest absence.
	run(repo, "checkout", "--quiet", "--orphan", "elsewhere")
	run(repo, "rm", "-rf", "--ignore-unmatch", ".")
	orphan := commit("unrelated")
	// Every refusal answers zero as well as false, so a caller that reads the
	// count without the bool is not handed a width nothing established.
	if n, ok := CommitsBetween(ctx, repo, orphan, fourth); ok || n != 0 {
		t.Errorf("CommitsBetween across unrelated histories = (%d, %v), want (0, false)", n, ok)
	}
	if n, ok := CommitsBetween(ctx, repo, "", fourth); ok || n != 0 {
		t.Errorf("CommitsBetween with no `from` = (%d, %v), want (0, false)", n, ok)
	}
	if n, ok := CommitsBetween(ctx, repo, first, ""); ok || n != 0 {
		t.Errorf("CommitsBetween with no `to` = (%d, %v), want (0, false)", n, ok)
	}
}

// The branch a recording is filed under is the caller's own statement before
// it is a discovery, because a detached HEAD is the normal shape of a CI
// checkout and discovery answers nothing there.
func TestBranchTakesTheCallersStatementFirst(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "one")

	if got := Branch(ctx, repo, ""); got != "main" {
		t.Errorf("Branch on a checked-out branch = %q, want main", got)
	}
	if got := Branch(ctx, repo, "release/1.x"); got != "release/1.x" {
		t.Errorf("Branch with an override = %q, want the override to win", got)
	}

	run(repo, "checkout", "--quiet", "--detach", "HEAD")
	// Empty, never a guess: a record filed under a branch this checkout is not
	// on puts one line's points on another line.
	if got := Branch(ctx, repo, ""); got != "" {
		t.Errorf("Branch on a detached HEAD = %q, want it to say it cannot tell", got)
	}
	if got := Branch(ctx, repo, "main"); got != "main" {
		t.Errorf("Branch on a detached HEAD with an override = %q, want the override", got)
	}
}

// Records that cannot be built fail the write rather than landing a baseline
// with no history beside it. The two are one commit precisely so a tree's
// state cannot half-land, and an error from the closure is the case where it
// would.
func TestRecordsThatCannotBeBuiltFailTheWrite(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)
	before := revCount(t, ctx, clone)

	_, err := Write(ctx, clone, "tree1", Snapshot{Coverage: Baseline{"api": entry(30, 100)}},
		func(string) ([]ledger.Record, error) { return nil, errors.New("the history could not be described") })
	if err == nil {
		t.Fatal("Write reported success though the records could not be built")
	}
	if !strings.Contains(err.Error(), "could not be described") {
		t.Errorf("error = %q, want it to carry what went wrong", err)
	}
	if got := revCount(t, ctx, clone); got != before {
		t.Errorf("the write added %d commits despite failing", got-before)
	}
}

// A record the ledger refuses is the same kind of failure, and must not leave
// the baseline landed on its own.
func TestARecordTheLedgerRefusesFailsTheWrite(t *testing.T) {
	ctx := context.Background()
	clone := originAndClone(t, ctx)
	before := revCount(t, ctx, clone)

	_, err := Write(ctx, clone, "tree1", Snapshot{Coverage: Baseline{"api": entry(30, 100)}},
		func(string) ([]ledger.Record, error) {
			// No commit, which nothing could ever read back.
			return []ledger.Record{{Kind: ledger.KindEntry, Branch: "main",
				At: time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)}}, nil
		})
	if err == nil {
		t.Fatal("Write accepted a record the ledger refuses")
	}
	if got := revCount(t, ctx, clone); got != before {
		t.Errorf("the write added %d commits despite failing", got-before)
	}
}

// A revision git cannot resolve is an error naming it, not a zero commit that
// would then be recorded as this tree's history.
func TestDescribeCommitReportsARevisionGitCannotResolve(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")

	got, err := DescribeCommit(ctx, repo, "refs/heads/nothing-here")
	if err == nil {
		t.Fatalf("DescribeCommit resolved a revision that does not exist: %+v", got)
	}
	if !strings.Contains(err.Error(), "nothing-here") {
		t.Errorf("error = %q, want it to name the revision", err)
	}
}

// A root commit has no parent, which is the first record a repository can ever
// have — not an error, and not a gap, because nothing precedes it.
func TestDescribeCommitAcceptsARootCommit(t *testing.T) {
	ctx := context.Background()
	run := gitRunner(t, ctx)
	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-m", "root")

	got, err := DescribeCommit(ctx, repo, "HEAD")
	if err != nil {
		t.Fatalf("DescribeCommit on a root commit: %v", err)
	}
	if got.Parent != "" {
		t.Errorf("a root commit named parent %q", got.Parent)
	}
	if got.SHA == "" || got.Tree == "" || got.At.IsZero() {
		t.Errorf("DescribeCommit = %+v, want a commit, a tree and a date", got)
	}
}

// The declaration a change makes about breaking an API can live in a commit
// footer, so the whole message has to come back — a reader given subjects alone
// reports a range that declared a break as declaring nothing.
func TestCommitMessagesReturnsWholeMessagesOldestFirst(t *testing.T) {
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		if r := executil.Run(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
	write := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(dir, "add", "-A")
	}

	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	write(repo, "base.txt")
	run(repo, "commit", "-m", "chore: base")
	base, err := HeadSHA(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}

	write(repo, "one.txt")
	run(repo, "commit", "-m", "feat: widen Config\n\nThe timeout is a duration now.\n\nBREAKING CHANGE: Config.Timeout changed from int to int64")
	write(repo, "two.txt")
	run(repo, "commit", "-m", "chore: tidy")

	got, err := CommitMessages(ctx, repo, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("CommitMessages = %q, want the two commits after the base", got)
	}
	if !strings.HasPrefix(got[0], "feat: widen Config") {
		t.Errorf("CommitMessages[0] = %q, want the oldest commit first", got[0])
	}
	if !strings.Contains(got[0], "BREAKING CHANGE: Config.Timeout changed from int to int64") {
		t.Errorf("CommitMessages[0] = %q, want the footer from the body", got[0])
	}
	if !strings.Contains(got[0], "The timeout is a duration now.") {
		t.Errorf("CommitMessages[0] = %q, want the body a blank line separates from the subject", got[0])
	}
	if got[1] != "chore: tidy" {
		t.Errorf("CommitMessages[1] = %q, want the newest commit last", got[1])
	}

	// A range with nothing in it is empty, not an error: a branch level with
	// its base has declared nothing, which is a fact and not a failure.
	if got, err := CommitMessages(ctx, repo, "HEAD", "HEAD"); err != nil || len(got) != 0 {
		t.Errorf("CommitMessages over an empty range = %q, %v, want no messages and no error", got, err)
	}
}

// taggedRepo is a repository whose tags are created in an order deliberately
// unlike their version order, each on its own commit — so a selection tied to
// tag-creation order or to the commit graph answers differently from one
// ordered by semver.
func taggedRepo(t *testing.T, ctx context.Context, tags ...string) string {
	t.Helper()
	run := gitRunner(t, ctx)
	repo := t.TempDir()
	run(repo, "init", "-b", "main", ".")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	for _, tag := range tags {
		if err := os.WriteFile(filepath.Join(repo, "f"), []byte(tag), 0o600); err != nil {
			t.Fatal(err)
		}
		run(repo, "add", "-A")
		run(repo, "commit", "-m", "release "+tag)
		run(repo, "tag", tag)
	}
	return repo
}

// The previous release is the semver-highest tag below this one, whatever order
// the tags were created in and whatever the commit graph says. A hotfix cut
// from an older commit, or a tag applied late, has a parent that records where
// the work sat rather than which release preceded it.
func TestPreviousTagIsTheSemverHighestBelow(t *testing.T) {
	ctx := context.Background()
	// Created newest-version-first and with v0.1.5 tagged after v0.2.0, so
	// creation order, commit order and version order all disagree.
	repo := taggedRepo(t, ctx, "v0.2.0", "v0.1.5", "v0.10.0", "v0.1.0")

	for _, c := range []struct {
		tag  string
		want string
	}{
		{"v0.10.0", "v0.2.0"},
		{"v0.2.0", "v0.1.5"},
		{"v0.1.5", "v0.1.0"},
		// A tag the repository does not hold is still resolvable: the version
		// is what places it, which is what lets a release be checked from the
		// commit it is about to be cut at.
		{"v0.3.0", "v0.2.0"},
		{"v1.0.0", "v0.10.0"},
	} {
		got, ok, err := PreviousTag(ctx, repo, c.tag)
		if err != nil || !ok {
			t.Fatalf("PreviousTag(%s) = (%q, ok=%v, %v), want %s", c.tag, got, ok, err, c.want)
		}
		if got != c.want {
			t.Errorf("PreviousTag(%s) = %q, want %q", c.tag, got, c.want)
		}
	}
}

// A prerelease is checked like any other release and is never a predecessor. A
// stable tag compares against the last stable one rather than against its own
// release candidates, whose ranges it wholly contains, and a candidate compares
// against the last stable tag too.
func TestPreviousTagNeverSelectsAPrerelease(t *testing.T) {
	ctx := context.Background()
	repo := taggedRepo(t, ctx, "v0.1.0", "v0.2.0", "v0.3.0-rc.1", "v0.3.0-rc.2", "v0.3.0")

	for _, c := range []struct {
		tag  string
		want string
	}{
		{"v0.3.0", "v0.2.0"},
		{"v0.3.0-rc.1", "v0.2.0"},
		{"v0.3.0-rc.2", "v0.2.0"},
		{"v0.4.0", "v0.3.0"},
	} {
		got, ok, err := PreviousTag(ctx, repo, c.tag)
		if err != nil || !ok {
			t.Fatalf("PreviousTag(%s) = (%q, ok=%v, %v), want %s", c.tag, got, ok, err, c.want)
		}
		if got != c.want {
			t.Errorf("PreviousTag(%s) = %q, want %q — a prerelease is never the previous release", c.tag, got, c.want)
		}
	}
}

// The first release has no predecessor. The range is empty by definition, which
// is a fact about the repository rather than a failure to look — so it is false
// with no error, and a caller can tell it from a range it could not read.
func TestPreviousTagReportsTheFirstReleaseHasNone(t *testing.T) {
	ctx := context.Background()

	got, ok, err := PreviousTag(ctx, taggedRepo(t, ctx, "v0.1.0", "v0.2.0"), "v0.1.0")
	if err != nil {
		t.Fatalf("PreviousTag on the first tag: %v", err)
	}
	if ok || got != "" {
		t.Errorf("PreviousTag(v0.1.0) = (%q, ok=%v), want no predecessor", got, ok)
	}

	// A repository whose only tags are prereleases has none either: every
	// candidate is excluded, and the answer is still not an error.
	got, ok, err = PreviousTag(ctx, taggedRepo(t, ctx, "v0.1.0-rc.1"), "v0.1.0")
	if err != nil {
		t.Fatalf("PreviousTag over prereleases alone: %v", err)
	}
	if ok || got != "" {
		t.Errorf("PreviousTag(v0.1.0) = (%q, ok=%v), want no predecessor", got, ok)
	}

	// A repository with no tags at all is the same answer, not an error: the
	// caller that cannot proceed without tags names the fetch depth as the fix.
	got, ok, err = PreviousTag(ctx, taggedRepo(t, ctx), "v0.1.0")
	if err != nil {
		t.Fatalf("PreviousTag with no tags: %v", err)
	}
	if ok || got != "" {
		t.Errorf("PreviousTag with no tags = (%q, ok=%v), want no predecessor", got, ok)
	}
}

// A version lydite cannot order is refused, on either side. The tag it cannot
// place may be exactly the predecessor, so narrowing the listing silently would
// widen the range a release is checked over and report a pass over commits it
// never read.
func TestPreviousTagRefusesAVersionItCannotOrder(t *testing.T) {
	ctx := context.Background()
	repo := taggedRepo(t, ctx, "v0.1.0", "v0.2.0")

	for _, tag := range []string{"v1.2.3.4", "vfoo", "", "0.1.0"} {
		got, ok, err := PreviousTag(ctx, repo, tag)
		if err == nil {
			t.Fatalf("PreviousTag(%q) = (%q, ok=%v), want an error", tag, got, ok)
		}
		if got != "" || ok {
			t.Errorf("PreviousTag(%q) answered (%q, ok=%v) as well as an error", tag, got, ok)
		}
	}

	// A stray tag among the candidates is refused the same way, naming it.
	stray := taggedRepo(t, ctx, "v0.1.0", "v1.2.3.4", "v0.2.0")
	got, ok, err := PreviousTag(ctx, stray, "v0.3.0")
	if err == nil {
		t.Fatal("PreviousTag resolved over a tag it cannot order")
	}
	if got != "" || ok {
		t.Errorf("PreviousTag over a stray candidate answered (%q, ok=%v) as well as an error", got, ok)
	}
	if !strings.Contains(err.Error(), "v1.2.3.4") {
		t.Errorf("error = %q, want it to name the tag that cannot be ordered", err)
	}

	// A tag matching nothing lydite reads as a version is not a candidate at
	// all, so it neither refuses the listing nor becomes a predecessor.
	unmatched := taggedRepo(t, ctx, "v0.1.0", "release-2", "v0.2.0")
	previous, ok, err := PreviousTag(ctx, unmatched, "v0.3.0")
	if err != nil || !ok || previous != "v0.2.0" {
		t.Errorf("PreviousTag = (%q, ok=%v, %v), want v0.2.0 — only %s is read as a version", previous, ok, err, TagPattern)
	}
}

// Two candidates ranking equal under semver — build metadata is not compared —
// keep the first one encountered rather than the last. `git tag -l` returns
// its output sorted lexically, so this is deterministic across runs: without
// it, which of two equally-ranked tags a caller sees would depend on nothing
// meaningful.
func TestPreviousTagKeepsTheFirstOfTwoEquallyRankedCandidates(t *testing.T) {
	ctx := context.Background()
	repo := taggedRepo(t, ctx, "v0.1.0", "v0.2.0+a", "v0.2.0+b")

	got, ok, err := PreviousTag(ctx, repo, "v0.3.0")
	if err != nil || !ok {
		t.Fatalf("PreviousTag(v0.3.0) = (%q, ok=%v, %v)", got, ok, err)
	}
	if got != "v0.2.0+a" {
		t.Errorf("PreviousTag(v0.3.0) = %q, want v0.2.0+a — the lexically first of two equally-ranked tags", got)
	}
}

// Only the release tags are listed, and the listing itself is not an ordering:
// version order is semver's answer, so a caller sorts rather than trusting
// git's lexical output.
func TestTagsListsOnlyTheReleaseTags(t *testing.T) {
	ctx := context.Background()
	repo := taggedRepo(t, ctx, "v0.2.0", "release-2", "v0.10.0")

	got, err := Tags(ctx, repo)
	if err != nil {
		t.Fatalf("Tags: %v", err)
	}
	want := map[string]bool{"v0.2.0": true, "v0.10.0": true}
	if len(got) != len(want) {
		t.Fatalf("Tags = %q, want %v", got, want)
	}
	for _, tag := range got {
		if !want[tag] {
			t.Errorf("Tags returned %q, which does not match %s", tag, TagPattern)
		}
	}
}

// A range naming a revision git cannot resolve is an error, not an empty
// slice — the two read alike to a caller that only checks length, and one of
// them means "nothing declared a break" while the other means the range was
// never read at all.
func TestCommitMessagesErrorsOnAnUnresolvableRange(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	if r := executil.Run(ctx, repo, "git", "init", "-b", "main", "."); !r.Ok() {
		t.Fatalf("git init: %v\n%s", r.Err, r.Output)
	}
	if _, err := CommitMessages(ctx, repo, "0000000000000000000000000000000000000000", "HEAD"); err == nil {
		t.Error("CommitMessages over an unknown revision must error")
	}
}
