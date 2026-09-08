package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// newRecordCmd lands the baseline one or more runs measured.
//
// It exists because measuring and recording want different things of the job
// they run in, and those things are incompatible. Measuring runs each
// component's suite and any setup/teardown shell the declaration carries, so
// on a pull request it executes the pull request's own code. Recording pushes
// to the lydite branch, so it needs a token that can write. A single command
// doing both puts that token in the job running that code.
//
// This command runs nothing from the repository: no suite, no setup, no
// teardown, no compose. It reads documents, checks them against the
// declaration, and writes.
//
// What it does not do is verify the numbers, and that is the honest limit. A
// branch that can edit its own workflow and its own component declaration can
// make the measuring job emit whatever it likes, and a command that executes
// nothing cannot tell. So this narrows the exposure — a job holding the token
// no longer runs arbitrary code — without closing it, and the answer to the
// rest is unchanged: record on a tree that has already merged, where the code
// has passed review and every gate. See docs/adr/0025.
//
// It is a subcommand of `test` because the document it consumes is one
// `lydite test` wrote, beside `lydite test plan` and `lydite test merge`.
//
// It folds the --reports directories itself rather than reading what `merge`
// produced, because it runs in a job `merge` does not precede: recording is one
// write after every shard has measured, in a workflow whose other jobs hold no
// token that can push.
func newRecordCmd() *cobra.Command {
	var dir, branch string
	var reports []string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "record",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Record the coverage baseline a run measured, running none of the repository",
		Long: `Write the baseline one or more ` + measurementsName + ` documents hold to the ` +
			gitstate.BranchName + ` branch.

Each --reports directory is a ` + runner.ReportDir + ` directory a lydite test run wrote —
one per shard, or one for the whole run. Nothing from the repository is
executed: no suite, no setup or teardown command, and no compose service.

The documents must all describe the tree that is checked out, so a measurement
cannot be recorded anywhere but where it was taken.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			rep := ui.NewReport("record")
			if err := recordBaseline(cmd.Context(), cmd, rep, dir, branch, reports); err != nil {
				return err
			}
			saveDocument(dir, rep)
			out := cmd.OutOrStdout()
			if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
				return err
			}
			return rep.Err()
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory holding a "+measurementsName+"; repeatable")
	cmd.Flags().StringVar(&branch, "branch", "",
		"the branch this recording's quality history is filed under; defaults to the checked-out branch, which a detached checkout does not have")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// recordBaseline folds the named documents and lands the result.
//
// Every refusal is a failing row rather than an error, because this command
// reached an answer: the documents were read and something about them says
// they must not be recorded. An error is reserved for not reaching one at all
// — an unreadable directory, a checkout with no tree.
func recordBaseline(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir, branch string, reports []string) error {
	docs, err := measurementsIn(rep, reports)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return errors.New("none of the named report directories holds a " + measurementsName +
			"\n       a `lydite test` run writes one; a run with --no-coverage does not")
	}
	folded, err := foldMeasurements(docs)
	if err != nil {
		return err
	}
	// The binding. A document names the tree it measured, and this refuses
	// to record it anywhere else: without it a mis-wired workflow lands one
	// tree's numbers under another tree's key, silently, and that entry then
	// gates every later change whose merge-base is that tree.
	//
	// It is asked before anything is written and before the fold is asked
	// whether it holds a baseline, because it binds the history as well: a
	// document describing another tree describes another commit, and a record
	// filed against this one would be this command inventing a data point.
	head, err := gitstate.TreeSHA(ctx, dir, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving the tree that is checked out: %w", err)
	}
	if head != folded.Tree {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — the measurement was taken on %s, but %s is checked out", shortSHA(folded.Tree), shortSHA(head)),
			Detail: []string{
				"record where the measurement was taken, or check that tree out first",
			}})
		return nil
	}

	// The quality history is decided independently of the baseline. The two
	// are different policies over one branch: a baseline is a cache, refused
	// whenever it would be partial, because a partial one reads as a cache hit
	// and gates every later change on nothing. A record is not recomputable at
	// all, so what a run measured is appended whether or not it adds up to a
	// baseline — and the run whose suite went red, which establishes no
	// baseline by construction, is exactly the one whose test counts a history
	// most wants.
	history, historyWhy := historyRecords(ctx, dir, branch, folded)

	// What may be recorded as a baseline, and the row that says why when
	// nothing may. An empty snapshot is a legitimate answer here, and is what
	// lets a refusal and an append land through the one write below.
	record, verdict, err := baselineToRecord(ctx, dir, folded, head)
	if err != nil {
		return err
	}

	// The one place a recording reaches the state branch, over both policies
	// and whichever of them has something to say. A second call site is a
	// second place state can reach the branch, which is the invariant this
	// command rests on and which a grep for `gitstate.Write` answers.
	landed, err := gitstate.Write(ctx, dir, head, record, history)
	if err != nil {
		// A failing row only when a baseline was actually being landed. That
		// write is what this command exists to do, so one that never landed is
		// this command failing. A recording carrying no baseline — a run whose
		// every suite went red, which still has its test counts — was writing
		// only the history, and a failed append is never a failing row: the
		// branch is shared and busy, a push race is routine, and failing a
		// consumer's build over one would erode trust in a gate that is
		// otherwise about their code. The next successful append records the
		// gap.
		//
		// A snapshot that is empty for any other reason than a stated one
		// still fails: `verdict` carries the refusal that emptied it, and an
		// empty snapshot with nothing to say about why is this command
		// failing to do its job.
		if record.Recorded() || verdict.Value == "" {
			rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
				Value:  "not recorded — the write to the " + gitstate.BranchName + " branch did not land",
				Detail: []string{err.Error()}})
		} else {
			rep.Add(verdict)
		}
		// The write carried both, so a push that never landed took the record
		// with it — unless there was nothing to append, in which case the
		// reason is still its own and not this one.
		failure := "the write to the " + gitstate.BranchName + " branch did not land"
		if history == nil {
			failure = ""
		}
		rep.Add(historyRow(nil, historyWhy, failure))
		return nil
	}
	if verdict.Value == "" {
		verdict = ui.Row{Status: ui.StatusPass, Label: "record",
			Value: fmt.Sprintf("%d component(s) recorded for %s", len(record.Coverage), shortSHA(head))}
	}
	rep.Add(verdict)
	rep.Add(historyRow(landed, historyWhy, ""))
	return nil
}

// baselineToRecord is what this fold may be recorded as, merged onto whatever
// the tree already holds — and, when it may not be recorded at all, the row
// that says why beside an empty snapshot.
//
// A refusal is a row rather than an error because this command reached an
// answer: the documents were read and something about them says they must not
// become a baseline. An error is reserved for not reaching one — an unreadable
// declaration, a configuration that will not parse.
//
// It writes nothing. Whether there is a baseline to land and whether there is
// history to append are separate questions with separate answers, and the one
// write below is what makes them one commit.
func baselineToRecord(ctx context.Context, dir string, folded measurementsDoc, head string) (gitstate.Snapshot, ui.Row, error) {
	if len(folded.Components) == 0 {
		// A fold with no component may still hold test counts: a run whose
		// every suite failed establishes no baseline by construction and knows
		// exactly how many tests went red, which is the data point a history
		// most wants and the one nothing can recover later.
		//
		// The reason travels as a Detail line rather than inside the value.
		// It is the one field here written by a run this command did not
		// perform, so it may carry anything that run's own inputs carried —
		// and a Detail is indented, which is what stops a line of it being
		// read as a status row of its own.
		return gitstate.Snapshot{}, ui.Row{Status: ui.StatusUnmeasured, Label: "record",
			Value: "nothing to record", Detail: []string{folded.Reason}}, nil
	}

	// Read from the tree being recorded, never from the fold: the
	// declaration says which components a complete baseline must cover, and
	// taking that from the same document whose completeness is in question
	// would make the check answer itself.
	decl, err := component.Load(dir)
	if err != nil {
		return gitstate.Snapshot{}, ui.Row{}, fmt.Errorf("reading %s: %w", component.FileName, err)
	}
	if gap, blocked := missingFromRecord(decl, folded); blocked {
		return gitstate.Snapshot{}, ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — %s has no entry, and a baseline missing a component gates on nothing", gap),
			Detail: []string{
				"the next change against this tree measures it instead of gating against a partial baseline",
			}}, nil
	}

	// The declaration bounds what may be recorded, on both sides of the merge
	// below. The fold is written by runs this command did not perform, so
	// a name in it that the tree does not declare is a component nothing can
	// ever measure again — the same thing an entry left behind by a deleted
	// component is, and it is dropped for the same reason.
	record := declaredOnly(decl, folded.snapshot())

	// The tolerance is read from the tree being recorded, because it is that
	// tree's own statement of how much measurement noise it accepts.
	cfg, err := config.Load(dir)
	if err != nil {
		return gitstate.Snapshot{}, ui.Row{}, fmt.Errorf("reading %s: %w", config.FileName, err)
	}

	// Merged onto whatever this tree already holds, never skipped because it
	// holds something: a re-run that measured more than the last one must not
	// be refused for finding an entry there.
	existing := existingSnapshot(ctx, dir, head)
	if !existing.Recorded() {
		return record, ui.Row{}, nil
	}
	// Anchored against what this tree already holds, not only against what
	// the measuring run compared with. The same tree is the same content, so
	// a difference between two measurements of it is the measurement noise
	// the tolerance exists for — and without this a recording that
	// re-measures a tree an earlier one already recorded replaces the
	// anchored high-water entry with a raw dipped one, handing the next
	// change a lowered number to gate against. That is the per-merge ratchet
	// withToleratedDipsRestored exists to prevent.
	//
	// No CRAP equivalent, and none is wanted: the score is a count of
	// functions, so two measurements of one tree differ only if what is
	// measured changed. There is no sub-tenth noise for a tolerance to
	// absorb, and a tolerance over an integer count would be a free function
	// above the threshold per merge.
	record.Coverage = withToleratedDipsRestored(record.Coverage, existing.Coverage, cfg.Coverage.Tolerance)
	merged := declaredOnly(decl, existing)
	for name, e := range record.Coverage {
		merged.Coverage[name] = e
	}
	for name, e := range record.CRAP {
		merged.CRAP[name] = e
	}
	if sameSnapshot(merged, existing) {
		// The same bytes as the branch already holds. It is still handed to
		// the write, which stages it, finds nothing changed and pushes only if
		// the history beside it did — so a recording that adds a record to an
		// already-recorded tree is one commit carrying only the record.
		return merged, ui.Row{Status: ui.StatusPass, Label: "record",
			Value: shortSHA(head) + " already holds this measurement"}, nil
	}
	return merged, ui.Row{}, nil
}

// historyRecords is the quality history this recording appends, and why there
// is none when there is none.
//
// It answers with a function rather than the records themselves because
// whether this commit follows the last one recorded is a question about the
// branch as fetched by the attempt that is about to write — a retry fetches a
// branch a concurrent run may have advanced, and an answer computed once would
// have that retry declare a gap the intervening run had just filled.
func historyRecords(ctx context.Context, dir, override string, folded measurementsDoc) (gitstate.Records, string) {
	// A branch and never a guess. History is per branch, so a record filed
	// under a branch this checkout is not on puts one line's points on
	// another line, and nothing downstream can tell. The caller's own
	// statement comes first, because a detached HEAD is the normal shape of a
	// CI checkout and the job that chose the ref is the one that knows.
	branch := gitstate.Branch(ctx, dir, override)
	if branch == "" {
		return nil, "this checkout names no branch, so pass " + gitstate.BranchFlag +
			" — history is per branch, and one filed under the wrong branch is worse than none"
	}
	// What there is to say, before asking git anything: a fold carrying no
	// scalar is a record naming a commit and holding no number, which is a
	// point on no line — and there is no reason to describe a commit nothing
	// is going to be filed against.
	components := historyComponents(folded)
	if len(components) == 0 {
		return nil, "no component produced a scalar"
	}
	head, err := gitstate.DescribeCommit(ctx, dir, "HEAD")
	if err != nil {
		return nil, "this commit could not be described: " + err.Error()
	}
	entry := ledger.Record{
		Kind:       ledger.KindEntry,
		At:         head.At,
		Commit:     head.SHA,
		Parent:     head.Parent,
		Tree:       head.Tree,
		Branch:     branch,
		Components: components,
	}
	return func(worktree string) ([]ledger.Record, error) {
		if gap, ok := gapBefore(ctx, dir, worktree, branch, head); ok {
			return []ledger.Record{gap, entry}, nil
		}
		return []ledger.Record{entry}, nil
	}, ""
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
func gapBefore(ctx context.Context, dir, worktree, branch string, head gitstate.Commit) (ledger.Record, bool) {
	previous, ok := ledger.Latest(worktree, branch, head.At)
	// Nothing recorded on this branch yet is not a gap: a repository adopting
	// the ledger has no history to be missing, and claiming one would put a
	// break at the start of every line ever drawn.
	if !ok {
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

// historyComponents is each component's scalars as the ledger records them.
//
// The counts are what was MEASURED and never the anchored ones a baseline
// records: anchoring exists so a within-tolerance dip does not ratchet the
// gate downwards, which is a property of the gate rather than of the commit,
// and a history that recorded it would draw a line nothing ever measured.
//
// A component this run carried rather than measured keeps its number, because
// affected selection established that the change could not have touched it —
// so a flat line is the true one, and dropping it would make the series vanish
// and reappear with whatever a change happened to touch.
func historyComponents(doc measurementsDoc) map[string]ledger.Component {
	out := map[string]ledger.Component{}
	for name, m := range doc.Components {
		c := ledger.Component{Producer: m.Producer}
		lines := m.LineCount
		if m.Unanchored != nil {
			lines = *m.Unanchored
		}
		if lines.Measured() {
			c.Coverage = &ledger.Lines{Covered: lines.Covered, Total: lines.Total}
		}
		if m.CRAP != nil {
			c.CRAP = &ledger.CRAP{Above: m.CRAP.Above, Worst: m.CRAP.Worst}
		}
		out[name] = c
	}
	for name, counts := range doc.Tests {
		c := out[name]
		c.Tests = &counts
		out[name] = c
	}
	// A component holding no scalar at all contributes nothing but its name,
	// which is a point on no line.
	for name, c := range out {
		if c.Coverage == nil && c.CRAP == nil && c.Tests == nil {
			delete(out, name)
		}
	}
	return out
}

// historyRow says what reached the quality history.
//
// A failed append is never a failing row. Failing a consumer's build over a
// push race would erode trust in a gate that is otherwise about their code,
// which is the whole reason the ledger tolerates gaps and records them instead
// — so the row is amber, and the next successful append is what says how wide
// the hole was.
func historyRow(landed []ledger.Record, why, failure string) ui.Row {
	const label = "history"
	switch {
	case failure != "":
		return ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value:  "not appended — the next recording on this branch records the gap",
			Detail: []string{failure}}
	case why != "":
		return ui.Row{Status: ui.StatusUnmeasured, Label: label, Value: "not appended — " + why}
	case len(landed) == 0:
		// What was offered and what landed are different things: a commit
		// already on the branch is not appended again, and saying otherwise
		// would claim a data point this run did not add.
		return ui.Row{Status: ui.StatusContext, Label: label, Value: "already recorded"}
	}
	var gap *ledger.Gap
	for _, rec := range landed {
		if rec.Kind == ledger.KindGap {
			gap = rec.Gap
		}
	}
	if gap == nil {
		return ui.Row{Status: ui.StatusContext, Label: label,
			Value: fmt.Sprintf("%d record(s) appended", len(landed))}
	}
	// A gap is reported where it is discovered, because the recording that
	// should have written it wrote nothing at all — this is the only run that
	// can say the hole is there.
	return ui.Row{Status: ui.StatusContext, Label: label,
		Value:  fmt.Sprintf("%d record(s) appended, one of them a gap", len(landed)),
		Detail: []string{gap.Reason}}
}

// existingSnapshot is what the branch already holds for this tree, across every
// metric, so a recording merges onto it rather than replacing it.
//
// A read that fails is an empty snapshot rather than an error: the question is
// only whether there is something to merge onto, and a run that cannot answer
// it records what it measured — which is the state the tree would have been
// left in had nothing been there.
func existingSnapshot(ctx context.Context, dir, tree string) gitstate.Snapshot {
	snap, err := gitstate.ReadSnapshot(ctx, dir, tree)
	if err != nil {
		return gitstate.Snapshot{}
	}
	return snap
}

// sameSnapshot reports whether two snapshots hold the same entries under every
// metric, so a run that would rewrite a tree's state byte for byte does not
// push to do it.
func sameSnapshot(a, b gitstate.Snapshot) bool {
	return sameEntries(a.Coverage, b.Coverage) && sameEntries(a.CRAP, b.CRAP)
}

// measurementsIn reads one document per named directory, adding a row for each
// so a folded recording says what it was folded from.
//
// A directory holding no measurements is named and skipped rather than failing
// the command: a run with --no-coverage writes none, and a caller passing the
// same directory list to `record` as to `publish` is doing something
// reasonable.
func measurementsIn(rep *ui.Report, reports []string) ([]measurementsDoc, error) {
	var docs []measurementsDoc
	for _, dir := range reports {
		doc, err := readMeasurements(dir)
		if err != nil {
			rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "read(" + dir + ")",
				Value: "no measurements", Detail: []string{err.Error()}})
			continue
		}
		rep.Add(ui.Row{Status: ui.StatusContext, Label: "read(" + dir + ")",
			Value: fmt.Sprintf("%d component(s) for %s", len(doc.Components), shortSHA(doc.Tree))})
		docs = append(docs, doc)
	}
	return docs, nil
}

// missingFromRecord names a declared component the fold has no entry for.
//
// The same rule a single run applies to what it would record, asked again here
// because a sharded run's completeness is a property of the fold and of no
// document in it: each shard is missing most components until they are put
// together.
//
// A component nothing could ever measure is not a gap — a raw `command:`, or a
// runner whose instrumented variant names no report — and it is recognised the
// same way `lydite test` recognises it, from the declaration alone.
func missingFromRecord(decl component.File, doc measurementsDoc) (string, bool) {
	var gaps []string
	for _, c := range decl.Components {
		if _, ok := doc.Components[c.Name]; ok {
			continue
		}
		if unmeasurableByDeclaration(c) {
			continue
		}
		gaps = append(gaps, c.Name)
	}
	if len(gaps) == 0 {
		return "", false
	}
	sort.Strings(gaps)
	return strings.Join(gaps, ", "), true
}

// unmeasurableByDeclaration reports whether no run could ever measure this
// component, from what it declares and nothing else — which is all this
// command has, since it runs none of them.
func unmeasurableByDeclaration(c component.Component) bool {
	if len(c.Command) > 0 {
		return true
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return true
	}
	inv, ok := r.Build(runner.Instrumented, c.Args)
	return !ok || inv.CoverageReport == ""
}

// declaredOnly is a baseline narrowed to the components the tree declares.
//
// It is applied to everything that reaches the branch, so the declaration read
// from the recorded tree is the whole of what may appear under that tree's
// key. A component the declaration no longer holds dies with it rather than
// leaving a tail of entries nobody can measure, and a name a document
// invented never arrives.
func declaredOnly(decl component.File, snap gitstate.Snapshot) gitstate.Snapshot {
	out := gitstate.Snapshot{
		Coverage: make(gitstate.Baseline, len(snap.Coverage)),
		CRAP:     make(gitstate.CRAPBaseline, len(snap.CRAP)),
	}
	for name, e := range snap.Coverage {
		if declares(decl, name) {
			out.Coverage[name] = e
		}
	}
	for name, e := range snap.CRAP {
		if declares(decl, name) {
			out.CRAP[name] = e
		}
	}
	return out
}

// declares reports whether the declaration still holds a component by name.
func declares(decl component.File, name string) bool {
	for _, c := range decl.Components {
		if c.Name == name {
			return true
		}
	}
	return false
}
