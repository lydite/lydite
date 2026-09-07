// Package gitstate stores and retrieves the baselines lydite gates against on a
// dedicated `lydite` branch, keyed by the TREE they were computed against.
//
// One document per metric, each under its own key: a component's coverage
// counts, and its CRAP scalars. Separate rather than one wider entry, because
// the two are different quantities with different producers and different
// costs to re-measure — so a change to what one records must not cost every
// consumer a cache miss in the other. One writer, though, and one commit: they
// describe the same tree and are landed by the same job.
//
// A coverage baseline is one component's covered and total line counts, per component.
// Counts rather than percentages, because a percentage cannot be re-weighted:
// the per-language and global figures lydite gates are sums over these
// entries, and a run that measured only some components composes the rest from
// what is already here. It carries no language, because the component
// declaration already states that and a second statement could only drift.
//
// The tree, not the commit, because coverage is a property of content and
// because the tree is the one identifier a pull request shares with the commit
// it becomes: GitHub builds a pull request as refs/pull/N/merge, and a squash
// merge lands a commit carrying that same tree. Keyed by commit those two are
// unrelated, so a number measured on a pull request — by the pipeline that
// knows how to measure it — was thrown away, and the next pull request fell
// back to recomputing it in a bare worktree. Commit-keyed entries are still
// read, so state written before this keeps resolving. This is deliberately a branch, not a commit on main: it's
// bot-owned generated cache data, not source, so it needs no PR/review
// ceremony and never pollutes main's history. Lookups are lazy — there is no
// "on merge to main" trigger; the first PR against a new main SHA computes
// and caches the baseline, every subsequent PR against that SHA reuses it.
package gitstate

// Every git invocation here goes through executil.RunQuiet rather than
// executil.Run. Run streams a command's output live, which is right for a
// scanner whose findings are the point and wrong for plumbing whose output is
// data: `git rev-parse` would print two SHAs into the middle of the command's
// report, and under --json into the middle of the document.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
)

// BranchName is the dedicated branch coverage baselines live on.
const BranchName = "lydite"

// stateDir is the directory inside BranchName that baselines are written to
// and read from, and it is keyed to the coverage metric rather than to the
// file format. A baseline is only comparable to one produced by the same
// metric: today's entry is one component's covered and total line counts, and
// diffing it against a figure computed some other way measures the change of
// definition, not a change in coverage — which surfaces as a step change of
// several points that the gate reads as a regression.
//
// So any change to what is measured, or to the unit it is measured over, must
// bump this constant. Entries at the superseded directory are then simply never found,
// every consumer takes one clean cache miss, and the gate recovers by
// recording afresh.
//
// A gained field bumps it too, whenever an entry lacking that field would
// still be read as a hit. An entry carrying no Producer matches only a
// measurement lydite could not attribute, so under a directory whose entries
// predate the field every component reports as new and the gate enforces
// nothing — while ReadBaseline reports a hit, which is the failure mode the
// empty-entry rule exists for arriving through the reader instead. A clean
// miss measures the base tree and records a complete entry, which is slower
// and correct.
//
// A directory rather than a marker inside the file: the entries stay a plain
// component -> counts object, which is what makes them readable by hand on the
// branch, and the superseded ones stay in place to be inspected rather than
// overwritten.
const stateDir = "v4"

// StatePath is the path, inside BranchName, of the coverage baseline for key
// (a tree or commit SHA).
func StatePath(key string) string {
	return stateDir + "/" + key + ".json"
}

// crapStateDir is where CRAP baselines live, and it is a directory of its own
// for the reason stateDir is keyed to its metric: the two are different
// quantities, measured over different units, and an entry recorded under one
// definition is not comparable to one recorded under another.
//
// Separate documents rather than a wider entry under stateDir, and the
// separation is load-bearing in both directions. A v4 entry carries no CRAP
// scalars, so a widened entry read back would give them their zero value — the
// delta would be `current − 0`, which is the absolute count, so the first run
// after an upgrade would fail every repository over debt it has always had.
// That is the exact thing gating on a delta exists to prevent, arriving through
// the mechanism meant to prevent it. Widening therefore forces a version bump,
// and a bump there costs every consumer a full coverage cache miss for a
// feature that is not coverage — coupling two quantities with different
// producers and different costs to re-measure, so that every later change to
// one is a cache miss in the other.
//
// The threshold is not stored, because it is not configurable: crap.Threshold
// is the value the definition names, and moving it would change what the count
// means — which is a change to what is measured, and so a bump of this
// constant rather than a field beside the number.
const crapStateDir = "crap/v1"

// CRAPStatePath is the path, inside BranchName, of the CRAP baseline for key.
func CRAPStatePath(key string) string {
	return crapStateDir + "/" + key + ".json"
}

// TreeSHA resolves the tree a commit points at.
//
// Baselines are keyed by tree rather than by commit because the tree is what
// coverage is actually a property of, and because it is the one identifier
// shared by a pull request and the commit it becomes. GitHub builds a pull
// request as refs/pull/N/merge — the merged result — and a squash merge lands a
// commit with that same tree, so the number measured on the pull request is
// already the number for the main commit it produces. Keyed by commit SHA those
// two are unrelated, and the measurement is thrown away.
func TreeSHA(ctx context.Context, dir, commit string) (string, error) {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", commit+"^{tree}")
	if !r.Ok() {
		return "", fmt.Errorf("git rev-parse %s^{tree}: %w", commit, r.Err)
	}
	return strings.TrimSpace(r.Output), nil
}

// HeadSHA resolves the commit currently checked out. `lydite coverage`
// compares it against BaseSHA to tell a PR run (HEAD is ahead of the
// merge-base — compare against a baseline) from a main run (HEAD *is* the
// merge-base — there is nothing to compare against, but the coverage measured
// right now IS that commit's baseline, and recording it is the whole point).
func HeadSHA(ctx context.Context, dir string) (string, error) {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "HEAD")
	if !r.Ok() {
		return "", fmt.Errorf("git rev-parse HEAD: %w", r.Err)
	}
	return strings.TrimSpace(r.Output), nil
}

// BaseBranchFlag names the flag every command that resolves a merge-base
// offers, so an error raised here can name the fix without each caller
// restating it.
const BaseBranchFlag = "--base-branch"

// remote is the remote every base ref is resolved against.
//
// Hardcoded, deliberately. A repository with two remotes is a real thing and
// lydite cannot guess which one a pull request targets; discovering it would
// be a second inference layered on the one below, with the same failure mode
// and no flag to escape it. Naming the limit is better than half-solving it:
// a repository whose upstream is not called `origin` gets an error saying so,
// rather than a merge-base resolved against a fork.
const remote = "origin"

// BaseBranch resolves which branch on the remote a change is measured
// against, in descending order of how explicitly it was stated:
//
//  1. override — what a caller passed, which is the caller's own statement
//     and outranks anything discovered.
//  2. refs/remotes/origin/HEAD — git's own local record of the remote's
//     default branch. Free and offline where a clone has it, and
//     `actions/checkout` does not set it.
//  3. the remote's own HEAD, asked for over the network. This is the
//     authoritative answer and the one that works in CI, which is why the
//     guess below is very nearly unreachable.
//  4. whichever of main and master the remote actually has. A last resort for
//     a remote that reports no HEAD at all, and exactly one of the two or this
//     is an error: a repository carrying both has not said which one is the
//     default, and picking by a hardcoded precedence would measure a change
//     against a branch nobody chose.
//
// Every failure names the flag. The alternative — falling back to `main`
// whatever the remote holds — is how a repository whose default branch is
// `master` came to have --affected, `scan --diff-base auto` and the coverage
// gate all fail with a merge-base error that named neither the cause nor the
// fix.
//
// Step 3 is what keeps step 4 from being that same guess wearing a different
// hat. A repository whose default is `develop` and which still carries a stale
// `master` has exactly one candidate, so step 4 alone would answer `master`
// with no diagnostic at all — silently measuring every change against a branch
// nobody chose, which is worse than the hardcoded `main` it replaced.
func BaseBranch(ctx context.Context, dir, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if r := executil.RunQuiet(ctx, dir, "git", "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD"); r.Ok() {
		if name := strings.TrimPrefix(strings.TrimSpace(r.Output), remote+"/"); name != "" {
			return name, nil
		}
	}
	if name := remoteHead(ctx, dir); name != "" {
		return name, nil
	}
	// One query for both candidates rather than one each: a second network
	// round trip to learn the same thing is pure latency.
	r := executil.RunQuiet(ctx, dir, "git", "ls-remote", "--heads", remote, "main", "master")
	if !r.Ok() {
		return "", fmt.Errorf("listing %s's branches to find the default: %w\n       name it with %s", remote, r.Err, BaseBranchFlag)
	}
	// Each line is "<sha>\trefs/heads/<name>". Compared whole rather than by
	// substring: a branch called `not-main` contains `main`, and a listing
	// matched loosely would report both candidates present and refuse to run
	// on a repository that has exactly one.
	present := map[string]bool{}
	for _, line := range strings.Split(r.Output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		present[strings.TrimPrefix(fields[len(fields)-1], "refs/heads/")] = true
	}
	var found []string
	for _, name := range []string{"main", "master"} {
		if present[name] {
			found = append(found, name)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("%s has neither a main nor a master branch, so the default branch could not be discovered — name it with %s", remote, BaseBranchFlag)
	default:
		return "", fmt.Errorf("%s has both a main and a master branch, so which one is the default is not lydite's to guess — name it with %s", remote, BaseBranchFlag)
	}
}

// remoteHead asks the remote which branch its HEAD points at, which is the
// authoritative statement of its default branch and needs nothing set up
// locally — unlike refs/remotes/origin/HEAD, which a CI checkout does not
// create.
//
// `git ls-remote --symref origin HEAD` answers with a "ref: refs/heads/<name>
// HEAD" line before the SHA line. An empty answer is a remote that reports no
// HEAD, and is left to the caller's last resort rather than being an error:
// this is a discovery step, and the caller has the better message.
func remoteHead(ctx context.Context, dir string) string {
	r := executil.RunQuiet(ctx, dir, "git", "ls-remote", "--symref", remote, "HEAD")
	if !r.Ok() {
		return ""
	}
	for _, line := range strings.Split(r.Output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "ref:" {
			continue
		}
		if name := strings.TrimPrefix(fields[1], "refs/heads/"); name != fields[1] {
			return name
		}
	}
	return ""
}

// BaseSHA resolves the commit on the base branch this branch diverged from,
// which is what every gate comparing against "before this change" measures
// from: the coverage baseline lookup, affected selection, Semgrep's
// --baseline-commit and the referral diff.
//
// branch is what BaseBranch resolved. It is passed in rather than resolved
// here so a command resolves it once and reports it once, and so a run that
// cannot discover it fails with that error rather than with a merge-base one
// that names neither the cause nor the fix.
func BaseSHA(ctx context.Context, dir, branch string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("no base branch to resolve the merge-base against — name one with %s", BaseBranchFlag)
	}
	if r := executil.RunQuiet(ctx, dir, "git", "fetch", remote, branch); !r.Ok() {
		return "", fmt.Errorf("fetch %s %s: %w", remote, branch, r.Err)
	}
	ref := remote + "/" + branch
	r := executil.RunQuiet(ctx, dir, "git", "merge-base", "HEAD", ref)
	if !r.Ok() {
		return "", fmt.Errorf("git merge-base HEAD %s: %w", ref, r.Err)
	}
	return strings.TrimSpace(r.Output), nil
}

// ResolveBaseSHA discovers the base branch and resolves the merge-base
// against it in one step, for a caller that has nothing to say about either
// beyond the override it was given.
func ResolveBaseSHA(ctx context.Context, dir, override string) (string, error) {
	branch, err := BaseBranch(ctx, dir, override)
	if err != nil {
		return "", err
	}
	return BaseSHA(ctx, dir, branch)
}

// Entry is one component's measurement: its line counts, and what produced
// them.
//
// The counts are embedded rather than held in a named field so that an entry
// reads as the counts it mostly is: Covered, Total and Percent are promoted,
// and the JSON is a flat object rather than a nested one.
type Entry struct {
	coverage.LineCount
	// Producer names the instrument that wrote the report these counts came
	// from — the Go toolchain, cargo-llvm-cov and the Rust toolchain, or a
	// JavaScript workspace's own test runner and coverage provider.
	//
	// It is here because the gate's whole claim is "is this worse than it
	// was", which means something only if both sides measured the same
	// quantity. A runner or provider bump changes what a line is: vitest
	// 3.2.7 to 4.1.11 took one workspace from 345 lines to 152 over an
	// identical tree, 185 of the 193 dropped lines being covered ones, and
	// the gate reported the fall as a regression by the author of the bump.
	// Compared verbatim, so any difference reports the component new rather
	// than regressed — see docs/adr/0025.
	//
	// Empty when lydite could not identify what measured a component, which
	// is possible only for JavaScript: it is the one language whose measuring
	// instrument lydite deliberately does not pin, since installing one into
	// the tree it is about to gate would have lydite change what the
	// repository resolves to. An empty producer matches only another empty
	// one, so a component whose instrument lydite cannot name is still
	// compared rather than permanently reported new.
	Producer string `json:"producer,omitempty"`
}

// Baseline is one tree's measurement: each component's entry, keyed by the
// name the component declaration gives it.
//
// The name and not the directory, because a repository may legitimately
// declare two components over one directory tree and the name is the only one
// of the two that is unique by construction.
type Baseline map[string]Entry

// CRAPEntry is one component's CRAP scalars: how many of its functions score
// above the threshold, and the highest score any of them reached.
//
// Two numbers and not the functions themselves. The gate is the delta of the
// count, and a list of names cannot be compared across trees — a function
// renamed or moved would read as one fixed and one introduced. The names
// belong in the row a run renders, where a reader can act on them.
type CRAPEntry struct {
	// Above is how many functions exceed the threshold, and is the only
	// number the gate compares. A change may not raise it.
	Above int `json:"above"`
	// Worst is the highest CRAP value in the component. Recorded because a
	// quality ledger cannot recompute it after the fact, and never gated: a
	// change taking the worst function from 400 to 380 has improved nothing
	// anybody can act on, and one adding a well-tested complex function
	// raises it without adding a thing to fix.
	Worst float64 `json:"worst"`
	// Producer names the instrument that measured the coverage half of the
	// score, for the reason Entry carries one: a toolchain that counts
	// statements differently moves every function's coverage, and the count
	// on the far side of that is a different quantity rather than a
	// regression. Compared verbatim, so a difference reports the component
	// new.
	Producer string `json:"producer,omitempty"`
}

// CRAPBaseline is one tree's CRAP measurement, keyed by component name exactly
// as Baseline is.
type CRAPBaseline map[string]CRAPEntry

// Snapshot is everything recorded for one tree: one document per metric, each
// under its own key and each with its own miss.
//
// It exists so that there is one writer. Both documents describe the same tree
// and are landed by the same command in the same job, so writing them in one
// commit is one push, one retry loop and one thing that can half-happen — and
// a second WriteBaseline would be a second place a baseline can reach the
// branch, which is the invariant `lydite test record` rests on.
type Snapshot struct {
	// Coverage is each component's line counts.
	Coverage Baseline
	// CRAP is each component's complexity scalars, empty for a repository
	// with no component lydite computes them for.
	CRAP CRAPBaseline
}

// Recorded reports whether anything was ever recorded for this tree, under any
// metric.
//
// Any, and not the coverage document alone. It answers one question — is there
// something here to merge onto — and a tree carrying a CRAP document and no
// readable coverage one still has something: a recording that skipped the merge
// there would leave that document holding entries for components the
// declaration no longer has, which nothing later reads and nothing later
// clears.
func (s Snapshot) Recorded() bool { return len(s.Coverage) > 0 || len(s.CRAP) > 0 }

// ReadSnapshot returns what the branch holds for the first key that resolves,
// across every metric.
//
// One call and one fetch, because a caller wanting one metric for a tree wants
// the other for the same tree: two readers each refreshing `origin/lydite`
// would double the network round trips on every gated run to learn one thing.
//
// Each metric still misses on its own. A repository that has a coverage
// baseline and no CRAP one — every repository, the first time it runs a lydite
// that computes CRAP — is not a coverage miss, so it pays nothing to re-measure
// coverage it already has; its components read `new` for CRAP and gate nothing
// for one change, which is the shape a changed producer already has. Ask
// `Recorded` or the individual maps which of them resolved.
func ReadSnapshot(ctx context.Context, dir string, keys ...string) (Snapshot, error) {
	// A missing remote branch is the expected first-ever-run state, not an
	// error: there's nothing to fetch yet.
	if r := executil.RunQuiet(ctx, dir, "git", "fetch", "origin", BranchName); !r.Ok() {
		return Snapshot{}, nil
	}
	coverage, err := readState[Baseline](ctx, dir, StatePath, keys...)
	if err != nil {
		return Snapshot{}, err
	}
	scores, err := readState[CRAPBaseline](ctx, dir, CRAPStatePath, keys...)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Coverage: coverage, CRAP: scores}, nil
}

// readState reads one metric's document off the branch, which the caller has
// already fetched. One implementation for every metric, because everything that
// makes a read correct — an unreadable entry is a miss rather than a permanent
// red line, an empty object is a miss — is a property of caching a measurement
// rather than of which measurement it is.
//
// A miss is a nil map and never an error, so a caller asks `len` rather than
// carrying a second value per metric.
func readState[M ~map[string]E, E any](ctx context.Context, dir string, path func(string) string, keys ...string) (M, error) {
	// Keys are tried in order: the tree first, then the commit SHA. The SHA is
	// only a fallback for baselines written before keying moved to trees, so
	// existing state keeps resolving instead of every repository recomputing on
	// the first run after the change.
	var r executil.Result
	found := ""
	for _, key := range keys {
		if key == "" {
			continue
		}
		r = executil.RunQuiet(ctx, dir, "git", "show", "origin/"+BranchName+":"+path(key))
		if r.Ok() {
			found = key
			break
		}
	}
	// A caller that passed no usable key found nothing, which is a miss. The
	// zero Result reports Ok, so without this the unmarshal below runs on an
	// empty string and answers with a parse error — a hard failure where the
	// question was only whether an entry exists.
	if found == "" || !r.Ok() {
		return nil, nil
	}
	var report M
	if err := json.Unmarshal([]byte(r.Output), &report); err != nil {
		// A miss, and never an error. Nothing rewrites the base tree's entry —
		// a run records the tree it measured — so returning an error here
		// red-lines the gate for every change whose merge-base is that tree,
		// permanently and with no way back. A hand-edit, a truncated object or
		// an entry from a format this version does not know would all do it.
		// A miss recomputes and overwrites, which is the same self-healing the
		// empty-entry rule below exists for.
		fmt.Fprintf(os.Stderr, "lydite: the cached baseline for %s is not readable (%v) — measuring it again\n", found, err)
		return nil, nil
	}
	// An empty baseline ("{}") is a cache miss, not a baseline of nothing. A
	// run whose measurement failed for every component records nothing worth
	// keeping, and once such an entry is written it is indistinguishable from
	// a valid one: every later pull request gets a cache *hit* on it, reports
	// every component as new, and the gate enforces nothing, permanently and
	// silently. wardnet's lydite branch accumulated nine of these. The writer
	// refuses to cache an empty baseline in the first place; treating one as a
	// miss here heals the entries that were already written, without a manual
	// purge.
	if len(report) == 0 {
		return nil, nil
	}
	return report, nil
}

// WriteBaseline caches report for sha on the lydite branch, via a
// throwaway worktree so the caller's own working tree/branch is untouched.
//
// The branch is shared and busy — every CI run on the repo may push to it —
// so a non-fast-forward rejection is a routine event, not an edge case: the
// local origin/lydite tracking ref is as stale as the job's checkout
// (wardnet's scan runs for minutes between the two), and any concurrent run
// that lands a baseline in that window advances the remote. Each attempt
// therefore fetches the fresh remote ref immediately before staging, and a
// rejected push is retried from that fresh ref rather than swallowed. A push
// that never lands is returned as an error — the caller decides whether
// that's fatal, but it must never be reported as recorded (wardnet's main
// run printed "recorded coverage baseline" while the push had been rejected,
// and its PRs gated against nothing).
func WriteBaseline(ctx context.Context, dir, sha string, snap Snapshot) error {
	// One commit carrying every metric's document, never one push per metric.
	// The two describe the same tree and are landed by the same job, so
	// separate pushes would double the retry loop and leave a window where the
	// branch holds one half of a measurement — and a second writer is a second
	// place a baseline can reach the branch, which is the invariant
	// `lydite test record` rests on.
	//
	// A metric with nothing to record writes no document at all. An empty
	// object is a miss to every reader anyway, so writing one would add a file
	// per tree saying nothing — and for a repository whose components lydite
	// computes no CRAP for, one on every recording forever.
	files := map[string][]byte{}
	if err := stage(files, StatePath(sha), snap.Coverage); err != nil {
		return err
	}
	if err := stage(files, CRAPStatePath(sha), snap.CRAP); err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	const attempts = 3
	var lastErr error
	for range attempts {
		if lastErr = pushBaseline(ctx, dir, sha, files); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("pushing baseline for %s to %s (%d attempts): %w", sha, BranchName, attempts, lastErr)
}

// stage adds one metric's document to what the push will write, and adds
// nothing for a metric with no entry.
func stage[M ~map[string]E, E any](files map[string][]byte, path string, report M) error {
	if len(report) == 0 {
		return nil
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	files[path] = data
	return nil
}

// pushBaseline is one fetch → stage → commit → push attempt, over every
// document the snapshot holds.
func pushBaseline(ctx context.Context, dir, sha string, files map[string][]byte) error {
	tmp, err := os.MkdirTemp("", "lydite-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	defer func() { _ = executil.RunQuiet(ctx, dir, "git", "worktree", "remove", "--force", tmp) }()

	// A local branch unique to this invocation (derived from the unique temp
	// dir), never the shared BranchName itself: git refuses to have the same
	// branch checked out in two worktrees at once, and this repo may well
	// have several worktrees already (lydite's own gt bare-repo layout, or
	// concurrent CI jobs sharing a checkout) — two concurrent WriteBaseline
	// calls must not race on a shared local branch name. The remote branch
	// is still named BranchName; only the local staging name differs, pushed
	// via refspec below.
	staging := "lydite-staging-" + filepath.Base(tmp)

	branchExists := executil.RunQuiet(ctx, dir, "git", "ls-remote", "--exit-code", "--heads", "origin", BranchName).Ok()
	if branchExists {
		// Refresh origin/<BranchName> right before staging on it: the tracking
		// ref left behind by the job's checkout (or a prior ReadBaseline) can
		// be minutes stale, and a staging branch built on a stale ref pushes
		// non-fast-forward and is rejected.
		if r := executil.RunQuiet(ctx, dir, "git", "fetch", "origin", BranchName); !r.Ok() {
			return fmt.Errorf("fetch %s: %w", BranchName, r.Err)
		}
		if r := executil.RunQuiet(ctx, dir, "git", "worktree", "add", "-b", staging, tmp, "origin/"+BranchName); !r.Ok() {
			return fmt.Errorf("worktree add %s: %w", BranchName, r.Err)
		}
	} else {
		if r := executil.RunQuiet(ctx, dir, "git", "worktree", "add", "--detach", tmp); !r.Ok() {
			return fmt.Errorf("worktree add (detached): %w", r.Err)
		}
		if r := executil.RunQuiet(ctx, tmp, "git", "checkout", "--orphan", staging); !r.Ok() {
			return fmt.Errorf("checkout --orphan %s: %w", staging, r.Err)
		}
		if r := executil.RunQuiet(ctx, tmp, "git", "rm", "-rf", "--ignore-unmatch", "."); !r.Ok() {
			return fmt.Errorf("clear orphan worktree: %w", r.Err)
		}
	}

	// Sorted, so the `git add` arguments and therefore the commit are the
	// same whatever order the map iterated in.
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		path := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[rel], 0o600); err != nil {
			return err
		}
	}
	if r := executil.RunQuiet(ctx, tmp, "git", append([]string{"add"}, rels...)...); !r.Ok() {
		return fmt.Errorf("git add: %w", r.Err)
	}
	// Nothing staged means the fetched branch already carries this exact
	// content — a concurrent run recorded the same SHA's baseline first. The
	// desired state is on the remote; committing here would fail ("nothing to
	// commit"), so this is success, not an error. Different content (notably a
	// poisoned `{}` entry being healed by a real report) stages a change and
	// proceeds to overwrite as usual.
	if executil.RunQuiet(ctx, tmp, "git", "diff", "--cached", "--quiet").Ok() {
		return nil
	}
	commitR := executil.RunQuietEnv(ctx, tmp, []string{
		"GIT_AUTHOR_NAME=lydite", "GIT_AUTHOR_EMAIL=lydite@users.noreply.github.com",
		"GIT_COMMITTER_NAME=lydite", "GIT_COMMITTER_EMAIL=lydite@users.noreply.github.com",
	}, "git", "commit", "-m", "baseline for "+sha)
	if !commitR.Ok() {
		return fmt.Errorf("git commit: %w", commitR.Err)
	}
	if r := executil.RunQuiet(ctx, tmp, "git", "push", "origin", staging+":refs/heads/"+BranchName); !r.Ok() {
		return fmt.Errorf("git push: %w", r.Err)
	}
	return nil
}
