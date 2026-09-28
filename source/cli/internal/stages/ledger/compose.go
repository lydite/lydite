package ledgerstages

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
)

// ComposeRecordsIn is every scalar a recording carries, the findings it
// measured, and the checkout whose commit it is filed against.
type ComposeRecordsIn struct {
	// Dir is the checkout being recorded.
	Dir string
	// BranchOverride is the branch the caller states the recording is filed
	// under, empty to take the one that is checked out.
	BranchOverride string
	// Components is each component's scalars as the ledger records them. A
	// component holding none is absent.
	Components map[string]ledger.Component
	// RootFindings is the per-gate finding counts taken over the repository
	// rather than any one component.
	RootFindings map[string]int
	// Scope is every finding bucket this recording measured. A bucket outside
	// it keeps what the branch holds open.
	Scope map[ledger.FindingBucket]bool
	// Found is every located claim the recording's scan made.
	Found []finding.Finding
}

// ComposeRecordsOut is the quality history the recording appends, and why
// there is none when there is none.
type ComposeRecordsOut struct {
	// Records is what the write asks, once per push attempt, for the records
	// to append. Nil whenever Reason is set, which is what tells the write
	// there is no history to append at all.
	Records gitstate.Records
	// Reason is why there is no history to append, nil when Records is set.
	Reason Reason
}

// ComposeRecords is the quality history this recording appends, and why there
// is none when there is none.
//
// It answers with a function rather than the records themselves because
// whether this commit follows the last one recorded is a question about the
// branch as fetched by the attempt that is about to write — a retry fetches a
// branch a concurrent run may have advanced, and an answer computed once would
// have that retry declare a gap the intervening run had just filled. Which
// findings appeared and resolved is the same kind of question, asked of the
// same fetched branch for the same reason. Nothing but the write ever calls
// it.
//
// No reason is an error. A recording that appends no history can still land
// its baseline, so each one is an answer about the history alone.
func ComposeRecords(ctx context.Context, in ComposeRecordsIn) (ComposeRecordsOut, error) {
	// A branch and never a guess. History is per branch, so a record filed
	// under a branch this checkout is not on puts one line's points on another
	// line, and nothing downstream can tell. The caller's own statement comes
	// first, because a detached HEAD is the normal shape of a CI checkout and
	// the job that chose the ref is the one that knows.
	branch := gitstate.Branch(ctx, in.Dir, in.BranchOverride)
	if branch == "" {
		return ComposeRecordsOut{Reason: NoBranch{}}, nil
	}
	// What there is to say, before asking git anything: a recording carrying
	// no scalar is a record naming a commit and holding no number, which is a
	// point on no line — and there is no reason to describe a commit nothing
	// is going to be filed against. The root-scoped counts are a scalar of
	// their own and keep a recording worth making on their own: a repository
	// whose every suite was carried and whose scan found something still has a
	// point to put on that line.
	if len(in.Components) == 0 && len(in.RootFindings) == 0 {
		return ComposeRecordsOut{Reason: NoScalars{}}, nil
	}
	head, err := gitstate.DescribeCommit(ctx, in.Dir, "HEAD")
	if err != nil {
		return ComposeRecordsOut{Reason: Undescribable{Err: err}}, nil
	}
	entry := ledger.Record{
		Kind:         ledger.KindEntry,
		At:           head.At,
		Commit:       head.SHA,
		Parent:       head.Parent,
		Tree:         head.Tree,
		Branch:       branch,
		Components:   in.Components,
		RootFindings: in.RootFindings,
	}
	records := func(worktree string) ([]ledger.Record, error) {
		// One walk of the branch's own history serves both gap detection and
		// the finding diff, rather than each reading the same partitions on
		// its own — see ledger.BranchState.
		open, previous, hasPrevious := ledger.BranchState(worktree, branch, head.At)
		// A copy per attempt, so a retry's events are diffed against the
		// branch it fetched and never carry an earlier attempt's.
		rec := entry
		// No scope is no scan, and a recording that measured no bucket has
		// nothing to diff against what the branch holds open: findingEvents
		// diffs no bucket outside scope, so it answers nil for an empty one.
		rec.FindingEvents = findingEvents(in.Scope, in.Found, open)
		if gap, ok := gapBefore(ctx, in.Dir, branch, head, previous, hasPrevious); ok {
			return []ledger.Record{gap, rec}, nil
		}
		return []ledger.Record{rec}, nil
	}
	return ComposeRecordsOut{Records: records}, nil
}

// findingEvents is what became of each fingerprint in scope since the branch's
// last recording: appeared when this scan holds it and the open set did not,
// resolved when the open set held it and this scan does not.
//
// A bucket outside scope is never diffed. Its gate did not run this time, so
// the absence of its findings from this scan says nothing about them, and a
// resolution recorded there would close a finding nothing looked for.
//
// A resolution carries no path or rule. The claim it would read them from is
// the one this scan no longer makes, and the open set records only the
// fingerprint; the appearance that opened it carries both.
//
// Sorted, so the same scan over the same history writes the same line.
func findingEvents(scope map[ledger.FindingBucket]bool, found []finding.Finding,
	open map[ledger.FindingBucket]map[string]bool) []ledger.FindingEvent {
	current := map[ledger.FindingBucket]map[string]finding.Finding{}
	for _, f := range found {
		bucket := ledger.FindingBucket{Gate: f.Gate, Component: f.Component}
		if !scope[bucket] {
			continue
		}
		if current[bucket] == nil {
			current[bucket] = map[string]finding.Finding{}
		}
		fp := f.Fingerprint()
		if _, seen := current[bucket][fp]; !seen {
			current[bucket][fp] = f
		}
	}
	var events []ledger.FindingEvent
	for bucket := range scope {
		for fp, f := range current[bucket] {
			if !open[bucket][fp] {
				events = append(events, ledger.FindingEvent{
					Transition: ledger.FindingAppeared, Fingerprint: fp,
					Gate: bucket.Gate, Component: bucket.Component, Path: f.Path, Rule: f.Rule,
				})
			}
		}
		for fp := range open[bucket] {
			if _, still := current[bucket][fp]; !still {
				events = append(events, ledger.FindingEvent{
					Transition: ledger.FindingResolved, Fingerprint: fp,
					Gate: bucket.Gate, Component: bucket.Component,
				})
			}
		}
	}
	// No two events share all four keys — a bucket's appearances come from one
	// map keyed by fingerprint and its resolutions from another — so the order
	// is total.
	slices.SortFunc(events, func(a, b ledger.FindingEvent) int {
		return cmp.Or(
			cmp.Compare(a.Gate, b.Gate),
			cmp.Compare(a.Component, b.Component),
			cmp.Compare(a.Transition, b.Transition),
			cmp.Compare(a.Fingerprint, b.Fingerprint),
		)
	})
	return events
}

// gapBefore is the explicit break to append ahead of this commit's record,
// when the newest record on this branch is not this commit's parent.
//
// The break is already visible without it — every record names its parent, so
// a reader that finds one whose parent is not the previous record's commit has
// found the hole in the file it is already reading. What this adds is the one
// thing that reader cannot work out: how wide the hole is, which needs the
// repository. That division is what makes a gap recordable at all: the append
// that would have consumed a sequence number is the append that failed, so
// nothing about the failure can be written BY the failing run. It is written
// by the next successful one, out of git history — the one input a failed
// write cannot have damaged.
//
// previous and hasPrevious are the caller's own ledger.BranchState answer, not
// a fresh ledger.Latest lookup here: the caller already paid for one walk of
// the branch's partitions and this does not pay for a second.
func gapBefore(ctx context.Context, dir, branch string, head gitstate.Commit, previous ledger.Record, hasPrevious bool) (ledger.Record, bool) {
	// Nothing recorded on this branch yet is not a gap: a repository adopting
	// the ledger has no history to be missing, and claiming one would put a
	// break at the start of every line ever drawn.
	if !hasPrevious {
		return ledger.Record{}, false
	}
	if previous.Commit == head.Parent || previous.Commit == head.SHA {
		return ledger.Record{}, false
	}
	gap := ledger.Gap{From: previous.Commit}
	// At least one commit lies between, and never nought: CommitsBetween
	// answers nought only when the last recorded commit is this one's first
	// parent, and that is the contiguous case the return above already took.
	missing, known := gitstate.CommitsBetween(ctx, dir, previous.Commit, head.SHA)
	if !known {
		// A force-push, an unrelated history, or a checkout too shallow to
		// see back that far. The break is real and its width is not
		// establishable, and saying so is the whole of what this record is
		// for — a width invented here would be worse than the honest absence.
		gap.Reason = "the last recorded commit is not an ancestor of this one, so how many recordings are missing cannot be established"
	} else {
		gap.Reason = fmt.Sprintf("%d commit(s) between the last recorded one and this one were never recorded", missing)
		gap.Missing = missing
	}
	return ledger.Record{
		Kind: ledger.KindGap, At: head.At, Commit: head.SHA,
		Parent: head.Parent, Branch: branch, Gap: &gap,
	}, true
}
