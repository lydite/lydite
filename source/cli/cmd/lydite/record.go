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
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// newRecordCmd lands the candidate baselines one or more runs produced.
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
// `lydite test` wrote, and because `lydite test plan` and `lydite test merge`
// join it there.
func newRecordCmd() *cobra.Command {
	var dir string
	var reports []string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "record",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Record the coverage baseline a run measured, running none of the repository",
		Long: `Write the candidate baseline one or more ` + candidateName + ` documents hold to the ` +
			gitstate.BranchName + ` branch.

Each --reports directory is a ` + runner.ReportDir + ` directory a lydite test run wrote —
one per shard, or one for the whole run. Nothing from the repository is
executed: no suite, no setup or teardown command, and no compose service.

The candidates must all describe the tree that is checked out, so a document
cannot be recorded anywhere but where it was measured.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			rep := ui.NewReport("record")
			if err := recordBaseline(cmd.Context(), cmd, rep, dir, reports); err != nil {
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
		"a "+runner.ReportDir+" directory holding a "+candidateName+"; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// recordBaseline folds the named candidates and lands the result.
//
// Every refusal is a failing row rather than an error, because this command
// reached an answer: the candidates were read and something about them says
// they must not be recorded. An error is reserved for not reaching one at all
// — an unreadable directory, a checkout with no tree.
func recordBaseline(ctx context.Context, cmd *cobra.Command, rep *ui.Report, dir string, reports []string) error {
	docs, err := candidatesIn(rep, reports)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return errors.New("none of the named report directories holds a " + candidateName +
			"\n       a `lydite test` run writes one; a run with --no-coverage does not")
	}
	folded, err := foldCandidates(docs)
	if err != nil {
		return err
	}
	if len(folded.Components) == 0 {
		// The reason travels as a Detail line rather than inside the value.
		// It is the one field here written by a run this command did not
		// perform, so it may carry anything that run's own inputs carried —
		// and a Detail is indented, which is what stops a line of it being
		// read as a status row of its own.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "record",
			Value: "nothing to record", Detail: []string{folded.Reason}})
		return nil
	}

	// The binding. A candidate names the tree it measured, and this refuses
	// to record it anywhere else: without it a mis-wired workflow lands one
	// tree's numbers under another tree's key, silently, and that entry then
	// gates every later change whose merge-base is that tree.
	head, err := gitstate.TreeSHA(ctx, dir, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving the tree that is checked out: %w", err)
	}
	if head != folded.Tree {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — the candidate measured %s, but %s is checked out", shortSHA(folded.Tree), shortSHA(head)),
			Detail: []string{
				"record where the measurement was taken, or check that tree out first",
			}})
		return nil
	}

	// Read from the tree being recorded, never from the candidate: the
	// declaration says which components a complete baseline must cover, and
	// taking that from the same document whose completeness is in question
	// would make the check answer itself.
	decl, err := component.Load(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", component.FileName, err)
	}
	if gap, blocked := missingFromRecord(decl, folded); blocked {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — %s has no entry, and a baseline missing a component gates on nothing", gap),
			Detail: []string{
				"the next change against this tree measures it instead of gating against a partial baseline",
			}})
		return nil
	}

	// The declaration bounds what may be recorded, on both sides of the merge
	// below. A candidate is written by a run this command did not perform, so
	// a name in it that the tree does not declare is a component nothing can
	// ever measure again — the same thing an entry left behind by a deleted
	// component is, and it is dropped for the same reason.
	record := declaredOnly(decl, folded.baseline())

	// The tolerance is read from the tree being recorded, because it is that
	// tree's own statement of how much measurement noise it accepts.
	cfg, err := config.Load(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", config.FileName, err)
	}

	// Merged onto whatever this tree already holds, never skipped because it
	// holds something: a re-run that measured more than the last one must not
	// be refused for finding an entry there.
	if existing, hit, _ := gitstate.ReadBaseline(ctx, dir, head); hit {
		// Anchored against what this tree already holds, not only against
		// what the measuring run compared with. The same tree is the same
		// content, so a difference between two measurements of it is the
		// measurement noise the tolerance exists for — and without this a
		// recording that re-measures a tree an earlier one already recorded
		// replaces the anchored high-water entry with a raw dipped one,
		// handing the next change a lowered number to gate against. That is
		// the per-merge ratchet withToleratedDipsRestored exists to prevent.
		record = withToleratedDipsRestored(record, existing, cfg.Coverage.Tolerance)
		merged := declaredOnly(decl, existing)
		for name, e := range record {
			merged[name] = e
		}
		if len(merged) == len(existing) && sameCounts(merged, existing) {
			rep.Add(ui.Row{Status: ui.StatusPass, Label: "record",
				Value: shortSHA(head) + " already holds this measurement"})
			return nil
		}
		record = merged
	}
	if err := gitstate.WriteBaseline(ctx, dir, head, record); err != nil {
		// A failing row and not a warning. This command exists to do exactly
		// one thing, so a write that never landed is this command failing —
		// unlike the same write attempted from inside a gate, where the
		// verdict was already reached and the cost was the next run's.
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
			Value:  "not recorded — the write to the " + gitstate.BranchName + " branch did not land",
			Detail: []string{err.Error()}})
		return nil
	}
	rep.Add(ui.Row{Status: ui.StatusPass, Label: "record",
		Value: fmt.Sprintf("%d component(s) recorded for %s", len(record), shortSHA(head))})
	return nil
}

// candidatesIn reads one candidate per named directory, adding a row for each
// so a folded recording says what it was folded from.
//
// A directory holding no candidate is named and skipped rather than failing
// the command: a run with --no-coverage writes none, and a caller passing the
// same directory list to `record` as to `publish` is doing something
// reasonable.
func candidatesIn(rep *ui.Report, reports []string) ([]candidateDoc, error) {
	var docs []candidateDoc
	for _, dir := range reports {
		doc, err := readCandidate(dir)
		if err != nil {
			rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: "read(" + dir + ")",
				Value: "no candidate baseline", Detail: []string{err.Error()}})
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
func missingFromRecord(decl component.File, doc candidateDoc) (string, bool) {
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
// leaving a tail of entries nobody can measure, and a name a candidate
// invented never arrives.
func declaredOnly(decl component.File, b gitstate.Baseline) gitstate.Baseline {
	out := make(gitstate.Baseline, len(b))
	for name, e := range b {
		if declares(decl, name) {
			out[name] = e
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
