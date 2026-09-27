package teststages

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	testmeasure "lydite/lydite/internal/test/measure"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// previousTreeBaseline is the entry recorded for the commit immediately before
// this one, used only to anchor a tolerated dip when recording on a branch that
// is its own base.
//
// That path reads no baseline — there is nothing to gate against — so without
// this the anchoring is a no-op and a raw within-tolerance dip is recorded
// against nothing. Each merge then dips up to one tolerance from the last
// recorded value and passes, which is the per-merge downward ratchet the
// anchoring exists to prevent, on the one path with no prior number in hand.
// A squash merge hides it, because the tree the pull request already anchored
// is the tree that lands; a rebase merge or a direct push does not.
//
// The commit immediately before, and never the nearest ancestor that happens
// to have an entry. A fixed one step back is reproducible from the change
// itself; a walk makes the number depend on how far back history had one,
// which is what ADR 0019 rejected for gating. This anchors a recording and
// gates nothing — a miss simply means no anchor.
func previousTreeBaseline(ctx context.Context, dir string) gitstate.Snapshot {
	tree, err := gitstate.TreeSHA(ctx, dir, "HEAD~1")
	if err != nil {
		return gitstate.Snapshot{}
	}
	// Every metric, because a component affected selection did not run carries
	// each of them forward from here, and an anchor holding half a tree's
	// state silently drops the other half.
	snap, err := gitstate.ReadSnapshot(ctx, dir, tree)
	if err != nil {
		return gitstate.Snapshot{}
	}
	return snap
}

// baselineFor reads the base tree's baseline, and measures it when there is
// none, with the row saying so when it does.
//
// A miss is resolved by measuring, never by substituting the nearest ancestor
// that happens to have an entry: which number a change is judged against would
// then depend on how far back history had one, which is not reproducible from
// the change itself.
func baselineFor(ctx context.Context, w io.Writer, dir, base string, concurrency int, logs Logs) (gitstate.Snapshot, []ui.Row, error) {
	tree, err := gitstate.TreeSHA(ctx, dir, base)
	if err != nil {
		tree = ""
	}
	cached, err := gitstate.ReadSnapshot(ctx, dir, tree, base)
	if err != nil {
		return gitstate.Snapshot{}, nil, err
	}
	// The coverage baseline alone decides whether the base tree is measured
	// again. CRAP is its own document with its own miss, and a miss there does
	// not measure: every repository has a coverage baseline and no CRAP one
	// the first time it runs a lydite that computes CRAP, and re-measuring the
	// base tree for a metric that gates nothing yet would charge every one of
	// them a full suite run for it. Those components read `new` and gate
	// nothing for one change, which is the shape a changed producer already
	// has.
	if len(cached.Coverage) > 0 {
		return cached, nil, nil
	}
	rows := []ui.Row{{Status: ui.StatusContext, Label: "baseline",
		Value:  fmt.Sprintf("not cached for %s — measuring it now", shortSHA(base)),
		Detail: []string{"the first change against this base tree pays this cost"}}}
	// A cache miss measures, and the measurement answers both metrics at
	// once: CRAP is computed from the coverage report, so a base tree
	// measured for one is measured for the other with nothing further to run.
	snap, err := measureBaseTree(ctx, w, dir, base, concurrency, logs)
	if err != nil {
		return gitstate.Snapshot{}, nil, err
	}
	// A base tree nothing could be measured at gates against nothing, and the
	// run says so rather than comparing against an empty baseline — which
	// renders every component new and enforces nothing.
	if len(snap.Coverage) == 0 {
		_, _ = fmt.Fprintf(w,
			"warning: measured no coverage at all at %s. The gate cannot compare against a baseline of nothing; fix what failed above and the next run measures it again.\n", base)
	}
	// The measurement is not cached, because `lydite test` writes nothing to
	// the lydite branch. It runs the repository's own suites and any
	// setup/teardown shell the declaration carries, and a job doing that must
	// not also hold a token that can push — which is the whole of what makes
	// the recording a separate command.
	//
	// What fills this cache is `lydite test record` on a tree that has
	// already merged, so the cost here is bounded by how often a base tree is
	// one nothing recorded: the first change after adopting the gate, and any
	// change based on a commit whose own run never landed its entry.
	return snap, rows, nil
}

// measureBaseTree checks the base commit out into a throwaway worktree and
// measures it through the same path this run took.
//
// The same path, deliberately: it starts the compose services each component
// declares, installs what each runner needs, and reads the report the
// instrumented variant wrote. Invoking coverage tooling directly instead is
// what produced a failed measurement for every suite that needs a database,
// which is one of the roads to an empty baseline cached as real.
//
// Nothing here may return a baseline missing a component it was supposed to
// measure. A bare worktree is where a measurement most often fails — no
// container runtime for a component's services, an install that fails there, a
// cold instrumentation build that times out — and a partial entry, once
// cached, reads as a hit for every later change: the missing component is new
// forever, and its language's row and the global row refuse to compare and
// stop gating, silently and with a green verdict.
func measureBaseTree(ctx context.Context, w io.Writer, dir, base string, concurrency int, logs Logs) (gitstate.Snapshot, error) {
	tmp, err := os.MkdirTemp("", "lydite-baseline-*")
	if err != nil {
		return gitstate.Snapshot{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// A context of its own, for the reason every compose teardown has one: the
	// run's may already be cancelled, and an interrupt would kill the removal
	// and then delete the directory anyway — leaving a registered worktree
	// pointing at nothing, which every later `git worktree add` and `git
	// worktree list` in that repository trips over until someone prunes.
	defer func() {
		_ = executil.RunQuiet(context.WithoutCancel(ctx), dir, "git", "worktree", "remove", "--force", tmp)
	}()

	if r := executil.RunQuiet(ctx, dir, "git", "worktree", "add", "--detach", tmp, base); !r.Ok() {
		return gitstate.Snapshot{}, fmt.Errorf("checking out %s to measure its baseline: %w", shortSHA(base), r.Err)
	}
	// A worktree holds the whole repository, and the scan root may sit below
	// it. Measuring at the worktree root instead would look for
	// `.lydite/components.yml` where there is none, find no components, and
	// hand back an empty baseline — so every component reads as new, every
	// composed row refuses to compare, and the gate passes having compared
	// nothing. Worse where the repository also has a declaration at its own
	// root: the base tree would then be measured through a different
	// repository's components.
	prefix, err := gitdiff.Prefix(ctx, dir)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("locating the scan root inside the repository: %w", err)
	}
	root := filepath.Join(tmp, filepath.FromSlash(prefix))
	// The base tree's own declaration and configuration, never this branch's.
	// A component this change adds did not exist there, and one it renames is
	// a different component; measuring the base tree through the branch's
	// declaration would attribute one component's coverage to another. Read
	// leniently, because this tree is being measured rather than configuring
	// lydite: it is the tree written for the version before whatever this one
	// rejects, and the first pull request that removes a retired key has
	// exactly that tree as its base.
	baseCfg, err := config.LoadHistorical(root)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("reading %s at %s: %w", config.FileName, shortSHA(base), err)
	}
	decl, err := component.LoadHistorical(root)
	if err != nil {
		return gitstate.Snapshot{}, fmt.Errorf("reading %s at %s: %w", component.FileName, shortSHA(base), err)
	}
	if len(decl.Components) == 0 {
		return gitstate.Snapshot{}, nil
	}
	// The base tree's own toolchains, from its own declaration and its own
	// config, for the whole of that declaration: a baseline missing a
	// component reads as a cache hit on every later change, so a shard
	// measures the base tree completely or not at all.
	//
	// A failure here warns and measures with what is on PATH rather than
	// ending the run. The only thing that can fail is a toolchain override the
	// base tree wrote and lydite cannot parse, and the author of a historical
	// tree is not being addressed and cannot act. The branch's own override is
	// still checked, by the resolution this run did for itself, so a
	// repository carrying a bad value is told about it exactly once and on
	// the tree whose author can fix it.
	envs, err := testrun.EnsureToolchains(ctx, w, root, baseCfg, testrun.ComponentUnits(decl.Components))
	if err != nil {
		_, _ = fmt.Fprintf(w,
			"warning: could not resolve the base tree's toolchains (%v) — measuring it with what is on PATH\n", err)
		envs = nil
	}
	// The base tree's rows describe a run nobody asked for, and reporting them
	// would put a second set of component rows beside the ones the reader is
	// looking at. Only a failing row's tail is kept.
	rows, ms := runComponents(ctx, root, decl.Components, nil, nil, baseCfg, envs, concurrency, false, true, logs, nil)

	// The worktree — and every log written into it — is removed on the way
	// out, so the tail under a failing row is the only account of what went
	// wrong that can outlive this function.
	tails := map[string][]string{}
	for _, row := range rows {
		if name, ok := strings.CutPrefix(row.Label, "test("); ok && row.Status == ui.StatusFail {
			tails[strings.TrimSuffix(name, ")")] = row.Detail
		}
	}

	out := baseTreeBaseline(w, base, ms, tails)
	// The same predicate a run applies to what it records, over the same two
	// things: the measurements, and what came of them. Returned empty rather
	// than partial, so the caller's refusal to gate against an empty baseline
	// covers this too — the next change measures the tree again, which is
	// slower and correct.
	//
	// Asked of the coverage half alone, because that is the half a partial
	// entry poisons: a composed figure refuses to compare unless the baseline
	// covers every component in it. CRAP has no composed gate, so a component
	// missing from it reports `new` on the next change, gates nothing for that
	// one change, and is recorded — which heals rather than persisting.
	if _, blocked := testmeasure.RecordingBlockedBy(ms, out); blocked {
		return gitstate.Snapshot{}, nil
	}
	return gitstate.Snapshot{Coverage: out, CRAP: baseTreeCRAP(ms)}, nil
}

// baseTreeCRAP is what the base-tree run scored, for the components it scored.
//
// No warning of its own: a component that could not be scored either could not
// be measured — baseTreeBaseline has already named it — or is in a language
// lydite scores none of, which is permanent and expected.
func baseTreeCRAP(ms []testmeasure.Measurement) gitstate.CRAPBaseline {
	out := gitstate.CRAPBaseline{}
	for _, m := range ms {
		if m.Scored() {
			out[m.Name] = m.CRAPEntry()
		}
	}
	return out
}

// baseTreeBaseline is what a base-tree run measured, and the account of what it
// did not.
//
// A component nothing could ever measure is neither: its absence is permanent
// and expected rather than a gap this run created, so warning about it would
// print what reads as a measurement failure on every baseline computation,
// forever, about a state the repository stated on purpose.
//
// Everything else that produced no measurement is named on w rather than
// dropped in silence, and the tail with it: the worktree holding the log is
// removed on the way out, so this is the only account of the failure that
// outlives the measurement.
func baseTreeBaseline(w io.Writer, base string, ms []testmeasure.Measurement, tails map[string][]string) gitstate.Baseline {
	out := gitstate.Baseline{}
	for _, m := range ms {
		switch {
		case m.Measured():
			out[m.Name] = m.Entry()
		case m.Unmeasurable:
		default:
			_, _ = fmt.Fprintf(w, "warning: the base tree at %s could not be measured for component %q: %s\n", shortSHA(base), m.Name, m.Why)
			for _, line := range tails[m.Name] {
				_, _ = fmt.Fprintf(w, "  %s\n", line)
			}
		}
	}
	return out
}
