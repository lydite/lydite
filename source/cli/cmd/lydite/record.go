package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flow"
	recordflow "lydite/lydite/internal/flows/record"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/ledger"
	"lydite/lydite/internal/runner"
	recordstages "lydite/lydite/internal/stages/record"
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
			if err := recordBaseline(cmd.Context(), rep, dir, branch, reports); err != nil {
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
// — an unreadable directory, a checkout with no tree — and adds no row, so a
// run that reached no answer renders nothing.
func recordBaseline(ctx context.Context, rep *ui.Report, dir, branch string, reports []string) error {
	record, err := recordflow.New()
	if err != nil {
		return err
	}
	res, err := record.Run(ctx, recordflow.Params{
		Dir:                  dir,
		Branch:               branch,
		Reports:              reports,
		Reader:               newRecordReports(),
		LangEnabled:          langEnabled,
		ScannerGates:         scannerGates,
		RestoreToleratedDips: withToleratedDipsRestored,
	}.Inputs())
	if err != nil {
		return recordError(err)
	}
	rows, err := recordRows(res)
	if err != nil {
		return err
	}
	for _, row := range rows {
		rep.Add(row)
	}
	return nil
}

// recordError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it, and a set of directories holding no
// measurements named by the document a run has to write.
func recordError(err error) error {
	if errors.Is(err, recordstages.ErrNoMeasurements) {
		return errors.New("none of the named report directories holds a " + measurementsName +
			"\n       a `lydite test` run writes one; a run with --no-coverage does not")
	}
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// recordRows is every row a completed run renders, in the order a reader
// follows the recording: what each directory held, whether the measurements
// bind to this checkout, how much of a scan was counted, what became of the
// baseline, and what reached the quality history.
func recordRows(res *flow.Result) ([]ui.Row, error) {
	read, err := flow.Output[recordstages.ReadReportsOut](res, recordflow.StageReadReports)
	if err != nil {
		return nil, err
	}
	rows := readRows(read)

	// A document describing another tree is refused before anything is
	// counted or written: it describes another commit, so neither a baseline
	// nor a record may be filed against this one.
	bound, err := flow.Output[recordstages.BindTreeOut](res, recordflow.StageBindTree)
	if err != nil {
		return nil, err
	}
	if !bound.Bound {
		return append(rows, ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — the measurement was taken on %s, but %s is checked out", shortSHA(bound.Measured), shortSHA(bound.Head)),
			Detail: []string{
				"record where the measurement was taken, or check that tree out first",
			}}), nil
	}

	counted, err := flow.Output[recordstages.CountFindingsOut](res, recordflow.StageCountFindings)
	if err != nil {
		return nil, err
	}
	rows = append(rows, findingsRow(counted.PerComponent, counted.Root, read.Scanned))

	composed, err := flow.Output[recordstages.ComposeHistoryOut](res, recordflow.StageComposeHistory)
	if err != nil {
		return nil, err
	}
	why, err := historyWhy(composed)
	if err != nil {
		return nil, err
	}
	decided, err := flow.Output[recordstages.DecideBaselineOut](res, recordflow.StageDecideBaseline)
	if err != nil {
		return nil, err
	}

	if res.Status(recordflow.StageWriteState) == flow.StatusFailed {
		written := writeError(res)
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
		// still fails: the verdict carries the refusal that emptied it, and an
		// empty snapshot with nothing to say about why is this command failing
		// to do its job.
		if decided.Snapshot.Recorded() || decided.Verdict == recordstages.VerdictToRecord {
			rows = append(rows, ui.Row{Status: ui.StatusFail, Label: "record",
				Value:  "not recorded — the write to the " + gitstate.BranchName + " branch did not land",
				Detail: []string{written.Error()}})
		} else {
			row, err := baselineRow(decided, bound.Head)
			if err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		// The write carried both, so a push that never landed took the record
		// with it — unless there was nothing to append, in which case the
		// reason is still its own and not this one.
		failure := ""
		if composed.Reason == recordstages.HistoryToAppend {
			failure = "the write to the " + gitstate.BranchName + " branch did not land"
		}
		return append(rows, historyRow(nil, why, failure)), nil
	}

	landed, err := flow.Output[recordstages.WriteStateOut](res, recordflow.StageWriteState)
	if err != nil {
		return nil, err
	}
	row, err := baselineRow(decided, bound.Head)
	if err != nil {
		return nil, err
	}
	return append(rows, row, historyRow(landed.Landed, why, "")), nil
}

// writeError is the error the write to the state branch failed with, which
// the flow records rather than failing the run on.
func writeError(res *flow.Result) error {
	for _, failed := range res.Errors() {
		if failed.Stage == recordflow.StageWriteState {
			return failed.Err
		}
	}
	return errors.New("the write failed and recorded no error")
}

// readRows is one row per report directory, saying what came out of it.
//
// A directory holding none of the documents is named and skipped rather than
// failing the command: a run with --no-coverage writes no measurements, and a
// caller passing the same directory list to `record` as to `publish` is doing
// something reasonable.
//
// One row per directory whatever it held, because the row answers "what came
// out of here" and a directory that answers twice is one a reader has to add
// up themselves.
func readRows(read recordstages.ReadReportsOut) []ui.Row {
	rows := make([]ui.Row, 0, len(read.Directories))
	for _, d := range read.Directories {
		rows = append(rows, readRow(d))
	}
	return rows
}

// readRow is what one report directory held.
//
// A document that is simply not there says nothing a reader must act on: each
// job writes some of the three, so another's absence is that job's shape rather
// than news. A document that is there and will not parse is the opposite, and
// is the only thing the detail reports — a scan whose claims stay absent, or
// measurements that would have been folded, each with the reason nothing else
// would say.
//
// The counts are the document absent from almost every directory: a `lydite
// test` shard writes none, and a mutation run writes one whether or not it
// mutated anything. Its absence is therefore never reported — only a document
// that is there and will not parse is.
func readRow(d recordstages.Directory) ui.Row {
	var held, detail []string
	switch {
	case d.MeasurementsErr == nil:
		held = append(held, fmt.Sprintf("%d component(s) for %s", len(d.Measurements.Components), shortSHA(d.Measurements.Tree)))
	case !errors.Is(d.MeasurementsErr, os.ErrNotExist):
		detail = append(detail, d.MeasurementsErr.Error())
	}
	switch {
	case d.ScanErr == nil:
		held = append(held, fmt.Sprintf("%d finding(s) from scan", len(d.Scan.Findings)))
	case !errors.Is(d.ScanErr, os.ErrNotExist):
		detail = append(detail, d.ScanErr.Error())
	}
	switch {
	case d.MutantsErr == nil:
		held = append(held, fmt.Sprintf("%d component(s) mutated for %s", len(d.Mutants.Components), shortSHA(d.Mutants.Tree)))
	case !errors.Is(d.MutantsErr, os.ErrNotExist):
		detail = append(detail, d.MutantsErr.Error())
	}

	if len(held) == 0 {
		// Nothing came out of this directory at all, so why is the whole of
		// what the row has to say — a plain absence included. That is the one
		// place it is worth reporting: a directory that held another document
		// is not missing anything, and this one is missing every one.
		if len(detail) == 0 {
			detail = []string{d.MeasurementsErr.Error()}
		}
		return ui.Row{Status: ui.StatusUnmeasured, Label: "read(" + d.Dir + ")",
			Value: "no measurements", Detail: detail}
	}
	// Context and not amber. A directory holding a scan and no measurements is
	// what the scan job uploads, and rendering the expected shape of a job as
	// an amber row trains a reader to skip the tag that exists to be noticed.
	return ui.Row{Status: ui.StatusContext, Label: "read(" + d.Dir + ")",
		Value: strings.Join(held, ", "), Detail: detail}
}

// baselineRow says what became of the baseline, for a write that landed and
// for a refusal whose own row stands whether or not it did.
//
// The reason a fold holds nothing travels as a Detail line rather than inside
// the value. It is the one field here written by a run this command did not
// perform, so it may carry anything that run's own inputs carried — and a
// Detail is indented, which is what stops a line of it being read as a status
// row of its own.
func baselineRow(decided recordstages.DecideBaselineOut, head string) (ui.Row, error) {
	switch decided.Verdict {
	case recordstages.VerdictToRecord:
		return ui.Row{Status: ui.StatusPass, Label: "record",
			Value: fmt.Sprintf("%d component(s) recorded for %s", len(decided.Snapshot.Coverage), shortSHA(head))}, nil
	case recordstages.VerdictUnchanged:
		return ui.Row{Status: ui.StatusPass, Label: "record",
			Value: shortSHA(head) + " already holds this measurement"}, nil
	case recordstages.VerdictRefused:
		return ui.Row{Status: ui.StatusFail, Label: "record",
			Value: fmt.Sprintf("not recorded — %s has no entry, and a baseline missing a component gates on nothing", decided.Missing),
			Detail: []string{
				"the next change against this tree measures it instead of gating against a partial baseline",
			}}, nil
	case recordstages.VerdictNothingToRecord:
		return ui.Row{Status: ui.StatusUnmeasured, Label: "record",
			Value: "nothing to record", Detail: []string{decided.Reason}}, nil
	default:
		return ui.Row{}, fmt.Errorf("the baseline was given no verdict (%d)", decided.Verdict)
	}
}

// historyWhy is why a recording appends no history, and empty when it
// appends some.
func historyWhy(composed recordstages.ComposeHistoryOut) (string, error) {
	switch composed.Reason {
	case recordstages.HistoryToAppend:
		return "", nil
	case recordstages.HistoryNoBranch:
		return "this checkout names no branch, so pass " + gitstate.BranchFlag +
			" — history is per branch, and one filed under the wrong branch is worse than none", nil
	case recordstages.HistoryNoScalar:
		return "no component produced a scalar", nil
	case recordstages.HistoryUndescribed:
		return "this commit could not be described: " + composed.Err.Error(), nil
	default:
		return "", fmt.Errorf("the history was given no reason (%d)", composed.Reason)
	}
}

// findingsRow says how much of a scan reached the record.
//
// It exists because the first production run of this path recorded nothing and
// said nothing: the scan job was green, the recording was green, and a ledger
// holding no finding count is exactly what a repository with no findings looks
// like. A count that silently did not happen has to be distinguishable from a
// count of nothing, and the row is the only thing that can say which.
//
// Context rather than amber. The count is not a gate, and a consumer whose
// workflow runs no scan is not one lydite should colour a recording over — the
// row is there to be read, not to vote.
func findingsRow(perComponent map[string]map[string]int, root map[string]int, scanned bool) ui.Row {
	const label = "findings"
	if !scanned {
		return ui.Row{Status: ui.StatusContext, Label: label,
			Value: "not counted — no report directory holds a " + documentName("scan")}
	}
	gates := 0
	for _, counts := range perComponent {
		gates += len(counts)
	}
	if gates == 0 && len(root) == 0 {
		return ui.Row{Status: ui.StatusContext, Label: label,
			Value: "not counted — no declared component has a gate that reports findings"}
	}
	return ui.Row{Status: ui.StatusContext, Label: label,
		Value: fmt.Sprintf("%d gate(s) counted for %d component(s), %d root-scoped",
			gates, len(perComponent), len(root))}
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

// recordReports is recordstages.ReportReader over the documents this package's
// other commands write: measurements from `lydite test`, a report document
// from `lydite scan`, and mutant counts from `lydite mutation`.
//
// Each document is read and folded by the code that owns it, in its own types
// and by its own rules, and converted only on the way out; every error is the
// owning code's own, unchanged, so the text a reader of the recording is shown
// is the text that code wrote.
//
// It keeps every document it read, keyed by the directory it was read from,
// because a fold folds what the reads returned rather than reading a directory
// a second time.
type recordReports struct {
	measurements map[string]measurementsDoc
	mutants      map[string]mutantsDoc
}

func newRecordReports() *recordReports {
	return &recordReports{measurements: map[string]measurementsDoc{}, mutants: map[string]mutantsDoc{}}
}

func (r *recordReports) ReadMeasurements(dir string) (recordstages.Measurements, error) {
	doc, err := readMeasurements(dir)
	if err != nil {
		return recordstages.Measurements{}, err
	}
	r.measurements[dir] = doc
	return recordedMeasurements(doc), nil
}

func (r *recordReports) ReadScan(dir string) (recordstages.Scan, error) {
	doc, err := readDocument(documentPath(dir, "scan"))
	if err != nil {
		return recordstages.Scan{}, err
	}
	return recordstages.Scan{Findings: doc.Findings, Crashed: doc.Crashed}, nil
}

func (r *recordReports) ReadMutants(dir string) (recordstages.Mutants, error) {
	doc, err := readMutants(dir)
	if err != nil {
		return recordstages.Mutants{}, err
	}
	r.mutants[dir] = doc
	return recordedMutants(doc), nil
}

func (r *recordReports) FoldMeasurements(dirs []string) (recordstages.Measurements, error) {
	docs := make([]measurementsDoc, 0, len(dirs))
	for _, dir := range dirs {
		doc, ok := r.measurements[dir]
		if !ok {
			return recordstages.Measurements{}, fmt.Errorf("%s: no %s was read from it to fold", dir, measurementsName)
		}
		docs = append(docs, doc)
	}
	folded, err := foldMeasurements(docs)
	if err != nil {
		return recordstages.Measurements{}, err
	}
	return recordedMeasurements(folded), nil
}

func (r *recordReports) FoldMutants(dirs []string) (recordstages.Mutants, error) {
	docs := make([]mutantsDoc, 0, len(dirs))
	for _, dir := range dirs {
		doc, ok := r.mutants[dir]
		if !ok {
			return recordstages.Mutants{}, fmt.Errorf("%s: no %s was read from it to fold", dir, mutantsName)
		}
		docs = append(docs, doc)
	}
	folded, err := foldMutants(docs)
	if err != nil {
		return recordstages.Mutants{}, err
	}
	return recordedMutants(folded), nil
}

// recordedMeasurements is doc as a recording reads it, its snapshot taken by
// the document's own rule for what the lydite branch stores.
func recordedMeasurements(doc measurementsDoc) recordstages.Measurements {
	components := make(map[string]recordstages.Measurement, len(doc.Components))
	for name, m := range doc.Components {
		components[name] = recordstages.Measurement{Entry: m.Entry, Unanchored: m.Unanchored, CRAP: m.CRAP}
	}
	return recordstages.Measurements{
		Tree:       doc.Tree,
		Components: components,
		Tests:      doc.Tests,
		Reason:     doc.Reason,
		Snapshot:   doc.snapshot(),
	}
}

// recordedMutants is doc as a recording reads it.
func recordedMutants(doc mutantsDoc) recordstages.Mutants {
	components := make(map[string]recordstages.MutantCounts, len(doc.Components))
	for name, c := range doc.Components {
		components[name] = recordstages.MutantCounts{
			Killed:         c.Killed,
			TimedOut:       c.TimedOut,
			OutOfMemory:    c.OutOfMemory,
			Survived:       c.Survived,
			Unviable:       c.Unviable,
			Acknowledged:   c.Acknowledged,
			ElapsedSeconds: c.ElapsedSeconds,
		}
	}
	return recordstages.Mutants{Tree: doc.Tree, Components: components}
}

// missingFromRecord is recordstages.MissingFromRecord over this package's own
// measurements document, so `lydite test merge` refuses a partial fold by the
// same rule a recording does.
func missingFromRecord(decl component.File, doc measurementsDoc) (string, bool) {
	return recordstages.MissingFromRecord(decl, recordedMeasurements(doc))
}

// unmeasurableByDeclaration is recordstages.UnmeasurableByDeclaration, so a
// run, a fold and a recording recognise a component nothing could ever measure
// by one rule.
func unmeasurableByDeclaration(c component.Component) bool {
	return recordstages.UnmeasurableByDeclaration(c)
}

// findingCounts is recordstages.FindingCounts under `lydite scan`'s own
// answers to which languages are checked and which gates each reports under.
func findingCounts(dir string, decl component.File, cfg config.Config, found []finding.Finding, scanned bool) (map[string]map[string]int, map[string]int) {
	return recordstages.FindingCounts(dir, decl, cfg, found, scanned, langEnabled, scannerGates)
}

// declares is recordstages.Declares.
func declares(decl component.File, name string) bool {
	return recordstages.Declares(decl, name)
}
