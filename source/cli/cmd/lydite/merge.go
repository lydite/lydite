package main

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// newMergeCmd folds the documents a matrix of shards wrote into one.
//
// Every shard reports exactly the components it was responsible for, so every
// declared component appears exactly once across them. That is what makes "did
// a shard die" a question about the declaration and the documents, needing no
// third input to answer — and it is why a gap is a failure here rather than an
// `unmeasured` row: an unmeasured row does not vote, so a run whose runner died
// would publish `"verdict": "pass"` over a repository it half tested.
//
// It is also the only thing that computes coverage(repo) and patch(repo). Both
// sum every component's counts, and both refuse to compare unless the baseline
// covers every component in the figure, so a shard holding two of four
// components would answer about its own two under a label about the repository.
//
// It touches no network. The shards' measurements carry each component's
// counts, its patch part and the baseline entry it was gated against, so the
// composition needs neither the lydite branch nor a merge-base.
func newMergeCmd() *cobra.Command {
	var dir string
	var reports []string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "merge",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Fold a matrix of shards' test reports into one",
		Long: `Fold the ` + documentName("test") + ` documents one or more shards wrote into a single
report, and compute the figures no shard can: coverage over the repository, and
patch coverage over the repository.

Each --reports directory is a ` + runner.ReportDir + ` directory a lydite test run wrote.
Nothing from the repository is executed and nothing is fetched: the declaration
says which components a complete run covers, and the shards' own measurements
carry everything the composition needs.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			streamDiagnostics(asJSON)
			decl, err := component.Load(dir)
			if err != nil {
				return err
			}
			// A fold over no component cannot say a shard died: completeness
			// is a question about the declaration, and an empty one answers
			// every question with yes. `plan` refuses the same state for the
			// same reason, and `scan` refuses it rather than emitting a row.
			if len(decl.Components) == 0 {
				return errors.New("no components declared in " + component.FileName +
					"\n       there is nothing to fold, and a fold over no component cannot report a shard that died")
			}
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			rep := ui.NewReport("test")
			mergeShards(rep, decl, cfg, reports)
			return renderTestReport(cmd, rep, dir, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory one shard wrote; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// shardInput is one shard's directory, and what was read out of it.
type shardInput struct {
	dir  string
	doc  ui.Document
	read bool
	// measured is the shard's measurements, absent for a shard that ran with
	// --no-coverage or wrote none.
	measured measurementsDoc
}

// wholeTreeRows are the gates that ask about the declaration and the tree
// rather than about any component, so every shard computes the same answer.
// They collapse to one, and a disagreement means the shards did not see the
// same tree — which makes every other row in the fold suspect.
var wholeTreeRows = []string{"orphans", "watch", "select"}

// mergeShards builds the folded report.
func mergeShards(rep *ui.Report, decl component.File, cfg config.Config, reports []string) {
	inputs := readShards(rep, reports)

	var problems []string
	for _, label := range wholeTreeRows {
		row, ok, disagreed := collapse(inputs, label)
		if disagreed {
			problems = append(problems, fmt.Sprintf(
				"the shards disagree about %s, so they did not all see the same tree", label))
			continue
		}
		if ok {
			rep.Add(row)
		}
	}
	if row, ok := foldedScheduleRow(inputs); ok {
		rep.Add(row)
	}

	// One row per declared component, in declaration order. A component no
	// shard reported is a shard that died; one two shards reported is two
	// jobs running the same suite, and a consumer keying rows by label picks
	// one of two answers.
	for _, c := range decl.Components {
		label := "test(" + c.Name + ")"
		found := rowsFor(inputs, label)
		switch len(found) {
		case 0:
			problems = append(problems, c.Name+" has no row in any shard's report")
		case 1:
			rep.Add(found[0])
		default:
			problems = append(problems, fmt.Sprintf("%s has a row in %d shards' reports", c.Name, len(found)))
		}
	}
	for _, c := range decl.Components {
		for _, label := range []string{"coverage(" + c.Name + ")", "patch(" + c.Name + ")", "floor(" + c.Name + ")"} {
			for _, row := range rowsFor(inputs, label) {
				rep.Add(row)
			}
		}
	}

	folded := foldMeasured(rep, decl, inputs)
	switch {
	case folded.measured && folded.gated:
		composedRows(rep, folded.ms, folded.carried, folded.baseline, folded.parts, cfg)
	case folded.measured:
		// The shards measured and compared nothing — the shape a run takes
		// where HEAD is its own merge-base. A gated row here would publish a
		// verdict the run it folds refused to reach, against a baseline no
		// shard was held to.
		rep.Add(ungatedComposedRow(repoLabel("coverage"), folded.ms, folded.carried, everything))
	case anyCoverageRow(inputs, decl):
		// The shards measured and wrote no measurements, which is what a run
		// that was never asked to gate does. A figure over the repository is
		// composed from counts rather than from rendered rows, so it cannot be
		// recovered here — and a fold that silently lost the row a single
		// process publishes is indistinguishable from a repository nobody
		// measured.
		rep.Add(unmeasuredRow(repoLabel("coverage"),
			"the shards wrote no "+measurementsName+", so there are no counts to compose — each shard writes one when --gate-coverage reaches a baseline"))
	}
	switch row, ok := floorSummaryRow(folded.floorMs, cfg.Coverage.Floor); {
	case ok:
		rep.Add(row)
	case cfg.Coverage.Floor > 0 && !folded.measured:
		// A floor is configured and the shards wrote no counts to hold
		// anything to. A shard emits no summary — the count is over the whole
		// declaration — so without this the merged report is byte-identical to
		// one where the floor is off, which is a gate that did not run reading
		// as one that did.
		rep.Add(unmeasuredRow("floor", fmt.Sprintf(
			"the shards wrote no %s, so the %.1f%% floor was applied to no component",
			measurementsName, cfg.Coverage.Floor)))
	}
	if folded.measured {
		rep.Add(recordRow(decl, folded.doc))
	}

	// Anything the fold has no rule for is carried through when the shards
	// agree about it, and named when they do not. A row merge cannot arbitrate
	// must not be silently reduced to one shard's copy.
	for _, label := range unhandledLabels(inputs, decl) {
		row, ok, disagreed := collapse(inputs, label)
		if disagreed {
			for _, r := range rowsFor(inputs, label) {
				rep.Add(r)
			}
			continue
		}
		if ok {
			rep.Add(row)
		}
	}

	// One `shards` row, whatever went wrong. It is added last because the
	// verdict check below reads the rows above it, and two rows under one
	// label is what a consumer keying rows by label cannot survive — which is
	// the very failure this row exists to report, reproduced by the row that
	// reports it.
	problems = append(problems, uncarriedVerdicts(rep, inputs)...)
	if len(problems) > 0 {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "shards",
			Value: fmt.Sprintf("%d shard(s) do not fold into one run of %d component(s)", len(inputs), len(decl.Components)),
			Detail: append(problems,
				"every declared component takes exactly one row across the shards, and every whole-tree gate the same answer; a shard whose job died satisfies neither, and an unmeasured row would let that publish a passing verdict")})
		return
	}
	rep.Add(ui.Row{Status: ui.StatusPass, Label: "shards",
		Value: fmt.Sprintf("%d shard(s) cover %d of %d component(s)", len(inputs), len(decl.Components), len(decl.Components))})
}

// readShards reads each named directory's test report and measurements, adding
// a row per directory so a folded report says what it was folded from.
func readShards(rep *ui.Report, reports []string) []shardInput {
	inputs := make([]shardInput, 0, len(reports))
	for _, dir := range reports {
		in := shardInput{dir: dir}
		doc, err := readDocument(documentPath(dir, "test"))
		if err != nil {
			rep.Add(ui.Row{Status: ui.StatusFail, Label: "read(" + dir + ")",
				Value: "no test report", Detail: []string{err.Error()}})
			inputs = append(inputs, in)
			continue
		}
		in.doc, in.read = doc, true
		// A shard run with --no-coverage writes no measurements at all, which
		// is a run that gated nothing rather than a run that went missing. A
		// file that is there and will not parse is neither, and is named:
		// treated as absent it would leave that shard's components composing
		// nothing while the row above still read `pass`.
		row := ui.Row{Status: ui.StatusContext, Label: "read(" + dir + ")",
			Value: fmt.Sprintf("%d row(s), %s", len(doc.Rows), doc.Verdict)}
		switch m, err := readMeasurements(dir); {
		case err == nil:
			in.measured = m
		case !errors.Is(err, fs.ErrNotExist):
			row.Status = ui.StatusFail
			row.Value += ", measurements not readable"
			row.Detail = []string{err.Error()}
		}
		rep.Add(row)
		inputs = append(inputs, in)
	}
	return inputs
}

// rowsFor returns every row carrying a label, in shard order.
func rowsFor(inputs []shardInput, label string) []ui.Row {
	var out []ui.Row
	for _, in := range inputs {
		for _, row := range in.doc.Rows {
			if row.Label == label {
				out = append(out, row)
			}
		}
	}
	return out
}

// collapse folds the rows under one label into the single row every shard
// agreed on. disagreed says two shards wrote different ones.
func collapse(inputs []shardInput, label string) (row ui.Row, ok, disagreed bool) {
	found := rowsFor(inputs, label)
	if len(found) == 0 {
		return ui.Row{}, false, false
	}
	for _, r := range found[1:] {
		if !sameRow(r, found[0]) {
			return ui.Row{}, false, true
		}
	}
	return found[0], true, false
}

// sameRow compares two rows by everything a reader sees, so "the shards agree"
// means they wrote the same row rather than the same status.
func sameRow(a, b ui.Row) bool {
	if a.Status != b.Status || a.Value != b.Value || a.Log != b.Log || len(a.Detail) != len(b.Detail) {
		return false
	}
	for i := range a.Detail {
		if a.Detail[i] != b.Detail[i] {
			return false
		}
	}
	return true
}

// foldedRow names every label the fold produces itself, so unhandledLabels can
// tell a row it replaced from one it has never seen.
func foldedRow(label string, decl component.File) bool {
	switch label {
	case "schedule", "shards", "record", repoLabel("coverage"), repoLabel("patch"), "floor":
		return true
	}
	for _, l := range wholeTreeRows {
		if label == l {
			return true
		}
	}
	if strings.HasPrefix(label, "read(") {
		return true
	}
	for _, c := range decl.Components {
		if label == "test("+c.Name+")" || label == "coverage("+c.Name+")" ||
			label == "patch("+c.Name+")" || label == "floor("+c.Name+")" {
			return true
		}
	}
	return false
}

// unhandledLabels are the labels the shards wrote that the fold has no rule
// for, in the order they first appear.
func unhandledLabels(inputs []shardInput, decl component.File) []string {
	seen := map[string]bool{}
	var out []string
	for _, in := range inputs {
		for _, row := range in.doc.Rows {
			if seen[row.Label] || foldedRow(row.Label, decl) {
				continue
			}
			seen[row.Label] = true
			out = append(out, row.Label)
		}
	}
	return out
}

// maxConcurrent reads the components-run-at-once count out of a schedule row's
// value.
//
// The number is observed at run time, so the fold cannot recompute it — and it
// is the one number that separates a scheduler that ran from one that only
// claims to, since every assertion about port locks is satisfied by a run that
// never had two components going at once. Reading it back out of lydite's own
// row is the alternative to a second channel carrying one integer.
var maxConcurrent = regexp.MustCompile(`max (\d+) concurrent`)

// foldedScheduleRow folds the shards' schedule rows into one.
//
// The value carries the largest number of components any shard ran at once,
// because that is what the port locks were actually exercised by. Each shard's
// serialised pairs are named beneath, verbatim, so a reader — and the proving
// ground's assertion — sees the constraint was reached rather than declared.
//
// A shard that failed its schedule row fails this one: an interrupted shard is
// a run that tested part of the repository, and the fold must not launder that
// into a pass.
func foldedScheduleRow(inputs []shardInput) (ui.Row, bool) {
	row := ui.Row{Status: ui.StatusPass, Label: "schedule"}
	best, shards, scheduled := 0, 0, 0
	for _, in := range inputs {
		if in.read {
			// Every shard that was read, not every shard that scheduled
			// something: one whose components were all deselected ran no
			// scheduler at all and still folded into this run.
			shards++
		}
		for _, r := range in.doc.Rows {
			if r.Label != "schedule" {
				continue
			}
			scheduled++
			if r.Status == ui.StatusFail {
				row.Status = ui.StatusFail
			}
			if m := maxConcurrent.FindStringSubmatch(r.Value); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n > best {
					best = n
				}
			}
			row.Detail = append(row.Detail, in.dir+": "+r.Value)
			row.Detail = append(row.Detail, r.Detail...)
		}
	}
	// No shard scheduled anything — every component was deselected, or none was
	// reached. A single run emits no schedule row at all in that state, and a
	// fold inventing a green one would be more assertive about the scheduler
	// than the runs it folds.
	if scheduled == 0 {
		return ui.Row{}, false
	}
	row.Value = fmt.Sprintf("%d shard(s), max %d concurrent", shards, best)
	return row, true
}

// composition is every shard's measurements as the values the repository-wide
// figures are computed from: one measurement per declared component, which of
// them were carried forward rather than measured, the baseline each was gated
// against, and each one's patch part.
type composition struct {
	doc      measurementsDoc
	ms       []measurement
	carried  map[string]bool
	baseline gitstate.Baseline
	parts    []patchPart
	// floorMs is ms with every carried entry back to unmeasured, which is
	// what the shards themselves held the floor against: a carried number
	// describes the base tree, and the component whose baseline it came from
	// ran nowhere in this matrix. Counted as measured it would fail a floor
	// no shard could have emitted a row for, and floorSummaryRow suppresses
	// the summary when anything is below — so the merged report would carry
	// no floor row at all with a floor configured and a component under it.
	floorMs []measurement
	// measured is false when no shard wrote any measurement at all — a matrix
	// run with --no-coverage — in which case there is no figure to compose and
	// a row saying 0.0% would be a measurement of nothing.
	measured bool
	// gated says the shards compared what they measured against a baseline.
	// Without that the figure is composed and shown, never gated: a run where
	// HEAD is its own merge-base measures every component and holds none of
	// them to anything. It is the shards' own statement rather than an
	// inference from an entry carrying a baseline, because a first adoption
	// gates every component against nothing and is still a gated run.
	gated bool
}

// foldMeasured builds that composition, padding every declared component the
// shards said nothing about so a figure counts the declaration rather than
// whatever happened to be measured.
func foldMeasured(rep *ui.Report, decl component.File, inputs []shardInput) composition {
	out := composition{carried: map[string]bool{}, baseline: gitstate.Baseline{}}
	var docs []measurementsDoc
	for _, in := range inputs {
		if in.measured.Tree != "" {
			docs = append(docs, in.measured)
		}
	}
	if len(docs) == 0 {
		return out
	}
	folded, err := foldMeasurements(docs)
	if err != nil {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "record",
			Value: "not folded", Detail: []string{err.Error()}})
		return out
	}
	for _, c := range decl.Components {
		e, ok := folded.Components[c.Name]
		if !ok {
			m := unmeasuredComponent(c, "no shard's measurements hold this component")
			m.Unmeasurable = unmeasurableByDeclaration(c)
			out.ms = append(out.ms, m)
			out.floorMs = append(out.floorMs, m)
			continue
		}
		m := e.asMeasurement(c)
		out.ms = append(out.ms, m)
		if e.Carried {
			out.carried[c.Name] = true
			m = unmeasuredComponent(c, "the component was not selected for this run")
		}
		out.floorMs = append(out.floorMs, m)
		if e.Base != nil {
			out.baseline[c.Name] = *e.Base
		}
		if p, ok := e.patchPartOf(c.Name); ok {
			out.parts = append(out.parts, p)
		}
	}
	out.doc, out.measured, out.gated = folded, true, folded.Gated
	return out
}

// recordRow says what `lydite test record` has to land, recomputed from the
// fold rather than collapsed from the shards' own rows: each of those counts
// only its own components, so three shards would publish three different
// numbers under one label.
//
// It holds the fold against the declaration, which is the one thing a fold can
// answer definitively and a single shard cannot. Announcing a recording that
// `record` then refuses is the untruth every row in this file is arranged to
// avoid, and the predicate is `missingFromRecord` — the same one `record`
// itself applies, so the two cannot give different answers.
//
// It records nothing. `record` reads the same --reports directories and folds
// them again, because it runs in a job with a token that can push and this one
// does not.
func recordRow(decl component.File, folded measurementsDoc) ui.Row {
	if len(folded.Components) == 0 {
		return ui.Row{Status: ui.StatusUnmeasured, Label: "record",
			Value: "nothing to record", Detail: []string{folded.Reason}}
	}
	if gap, blocked := missingFromRecord(decl, folded); blocked {
		return ui.Row{Status: ui.StatusUnmeasured, Label: "record",
			Value: fmt.Sprintf("not recordable — %s has no entry, and a baseline missing a component gates on nothing", gap),
			Detail: []string{
				"`lydite test record` refuses this fold; the next change against this tree measures it instead",
			}}
	}
	return ui.Row{Status: ui.StatusContext, Label: "record",
		Value: fmt.Sprintf("%d of %d component(s) ready for %s — `lydite test record` lands it",
			len(folded.Components), recordable(decl), shortSHA(folded.Tree))}
}

// uncarriedVerdicts names every shard that failed over something no row in the
// fold carries.
//
// It is the safety net for the reasoning above it: every row a shard wrote is
// reproduced or replaced by something the fold computed from the same facts, so
// it should find nothing. If it does, a shard failed over a row the fold
// dropped, and a fold that quietly passed would be the exact failure the
// completeness check exists to prevent, one layer out.
func uncarriedVerdicts(rep *ui.Report, inputs []shardInput) []string {
	if rep.Verdict() == ui.VerdictFail {
		return nil
	}
	var failed []string
	for _, in := range inputs {
		if in.read && in.doc.Verdict == ui.VerdictFail {
			failed = append(failed,
				in.dir+" failed, and the fold reproduced no row explaining it — read that shard's own report")
		}
	}
	sort.Strings(failed)
	return failed
}

// anyCoverageRow reports whether any shard measured a component's coverage.
//
// It separates a matrix run with --no-coverage, which measured nothing and
// wants no row saying so, from one that measured without gating, which lost the
// figure over the repository and has to say that it did.
func anyCoverageRow(inputs []shardInput, decl component.File) bool {
	for _, c := range decl.Components {
		if len(rowsFor(inputs, "coverage("+c.Name+")")) > 0 {
			return true
		}
	}
	return false
}
