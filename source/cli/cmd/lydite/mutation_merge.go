package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/flow"
	mutationflow "lydite/lydite/internal/flows/mutation"
	"lydite/lydite/internal/runner"
	mutationstages "lydite/lydite/internal/stages/mutation"
	shardstages "lydite/lydite/internal/stages/shards"
	"lydite/lydite/internal/ui"
)

// newMutationMergeCmd folds the documents a matrix of mutation shards wrote
// into one.
//
// It asks the same question `lydite test merge` asks, through the same
// implementation: every shard reports exactly the components it was
// responsible for, so a declared component with no row is a shard whose job
// died and one with two rows is two jobs that ran the same work. A second copy
// of that rule would agree with the first until one of them learned something,
// and the copy that drifts is the one nobody is looking at.
//
// What it adds is the summary. There is no repository-wide figure only a fold
// can compute — survived == 0 for every component is survived == 0 for the
// repository — so the row gates nothing and carries the counts and the elapsed
// time that make a runtime budget a measured decision later.
func newMutationMergeCmd() *cobra.Command {
	var dir string
	var reports []string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use:           "merge",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Fold a matrix of shards' mutation reports into one",
		Long: `Fold the ` + documentName("mutation") + ` documents one or more shards wrote into a
single report.

Each --reports directory is a ` + runner.ReportDir + ` directory a lydite mutation run wrote.
Nothing from the repository is executed and nothing is fetched: the declaration
says which components a complete run covers, and each shard's own rows say what
became of the mutants it ran.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			streamDiagnostics(asJSON)
			rep, err := mergeMutationShards(cmd.Context(), dir, reports)
			if err != nil {
				return err
			}
			return renderReport(cmd, rep, dir, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+component.FileName+" applies")
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory one shard wrote; repeatable")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// mutationWholeTreeRows are the gates a mutation run computes over the whole
// declaration, which every shard therefore answers identically.
var mutationWholeTreeRows = []string{"select"}

// mergeMutationShards runs the fold's flow over the shards' report directories
// and builds the folded report from what it read.
func mergeMutationShards(ctx context.Context, dir string, reports []string) (*ui.Report, error) {
	merge, err := mutationflow.NewMerge()
	if err != nil {
		return nil, err
	}
	r, err := merge.Run(ctx, mutationflow.MergeParams{
		Dir:     dir,
		Reports: reports,
		LogName: mutationLogName,
	}.Inputs())
	if err != nil {
		return nil, mutationError(err)
	}
	loaded, err := flow.Output[mutationstages.LoadComponentsOut](r, mutationflow.StageLoadComponents)
	if err != nil {
		return nil, err
	}
	// Completeness is a question about the declaration, and an empty one
	// answers every question with yes. `plan` and `lydite test merge` refuse
	// the same state for the same reason.
	if !loaded.Declared {
		return nil, errors.New("no components declared in " + component.FileName +
			"\n       there is nothing to fold, and a fold over no component cannot report a shard that died")
	}
	var read mutationMergeRead
	if read.shards, err = flow.Output[shardstages.ReadShardsOut](r, mutationflow.StageReadShards); err != nil {
		return nil, err
	}
	if read.counts, err = flow.Output[mutationstages.ReadShardCountsOut](r, mutationflow.StageReadShardCounts); err != nil {
		return nil, err
	}
	if read.folded, err = flow.Output[mutationstages.FoldShardCountsOut](r, mutationflow.StageFoldShardCounts); err != nil {
		return nil, err
	}
	if read.projections, err = flow.Output[mutationstages.ReadProjectionsOut](r, mutationflow.StageReadProjections); err != nil {
		return nil, err
	}
	// ReadProgress never fails as a whole: a log it could not read through is
	// a component it says nothing about.
	read.progress, _ = mutationstages.ReadProgress(ctx, mutationstages.ReadProgressIn{
		Shards: read.shards.Shards, File: loaded.File, LogName: mutationLogName})
	rep := ui.NewReport("mutation")
	addMergedMutationRows(rep, loaded.File, read)
	return rep, nil
}

// mutationMergeRead is what the fold's flow read out of the shards.
type mutationMergeRead struct {
	shards      shardstages.ReadShardsOut
	counts      mutationstages.ReadShardCountsOut
	folded      mutationstages.FoldShardCountsOut
	projections mutationstages.ReadProjectionsOut
	progress    mutationstages.ReadProgressOut
}

// addMergedMutationRows builds the folded report.
func addMergedMutationRows(rep *ui.Report, decl component.File, read mutationMergeRead) {
	// Keyed by directory, which is what shardInputs hands the hook: a shard's
	// counts are read from the directory its report was, so a directory named
	// twice reads the same both times.
	counted := map[string]mutationstages.ShardCounts{}
	for _, c := range read.counts.Shards {
		counted[c.Dir] = c
	}
	progressed := map[string][]mutationstages.Progress{}
	for _, p := range read.progress.Shards {
		progressed[p.Dir] = p.Components
	}
	inputs := shardInputsNoting(rep, "mutation", read.shards.Shards, func(dir string, _ *shardInput, row *ui.Row) {
		shardCountsRow(counted[dir], row)
	}, func(dir string) []string {
		return progressDetail(progressed[dir])
	})

	problems := wholeTreeRows(rep, inputs, mutationWholeTreeRows)
	if row, ok := foldedScheduleRow(inputs); ok {
		rep.Add(row)
	}
	problems = append(problems, mutationRows(rep, decl, inputs, read.projections.Projections)...)
	// A tree the shards disagree about is reported under `shards`, which is the
	// row that says these documents are not one run. The counts still fold from
	// the rows, because a fold that dropped them would answer a narrower
	// question than the one that failed.
	if read.folded.Err != nil {
		problems = append(problems, read.folded.Err.Error())
	}
	rep.Add(foldedMutationRow(inputs, read.folded.Counts, decl))
	carryUnhandled(rep, inputs, func(label string) bool { return foldedMutationLabel(label, decl) })
	shardsRow(rep, decl, inputs, problems)
}

// mutationRows is componentRowsNoting over the declaration, with every
// component that declares no suite taking the row its declaration gives it
// instead — the rule suiteRows applies to the test fold, for the same reason.
//
// No shard mutates such a component — `test plan` places it in no shard — so
// its absence from every shard's report is the plan working, not a job that
// died. Its row is the one an unsharded run gives it, and a shard that reported
// it anyway is not read: foldedMutationLabel claims its label, so its copy is
// neither carried nor counted twice.
func mutationRows(rep *ui.Report, decl component.File, inputs []shardInput, projections map[string]mutationstages.Projection) []string {
	var problems []string
	for _, c := range decl.Components {
		if declaresNoSuite(c) {
			rep.Add(noSuiteTestRow("mutation", c.Name))
			continue
		}
		one := component.File{Components: []component.Component{c}}
		problems = append(problems, componentRowsNoting(rep, one, inputs, mutationLabel,
			func(name string) string { return projectionNote(projections, name) })...)
	}
	return problems
}

// projectionNote is what a shard's uploaded directory still says about a
// component that took no row.
//
// Only the projection, quoted as the run's own words, and only when the log
// survived: the run said what it was about to cost before it spent it, and a
// reader deciding what to do about a missing row wants that figure. It is not a
// cause and is not offered as one — a killed job, a runner that ran out of
// memory and an upload that never arrived all leave exactly this behind, and a
// projection is a statement made before any of them happened.
//
// The first shard that has the log answers, which is the one projections
// holds. A component is one shard's responsibility, so there is normally one;
// two would mean two jobs ran the same work, which is the failure the second
// arm of componentRows reports and not this one.
func projectionNote(projections map[string]mutationstages.Projection, name string) string {
	p, ok := projections[name]
	if !ok {
		return ""
	}
	return fmt.Sprintf("and the log it left in %s says the run projected: %q — what the run said it was about to cost, not what became of it",
		p.Dir, p.Line)
}

// progressDetail is what the logs an unread shard left say about how far each
// of its components got, one sentence per fact, to sit beneath the reason its
// report was not read.
//
// A cancelled job writes no report, so this is the only place a reader learns
// what it was doing when it stopped: the mutants it started and never
// finished, against how many finished at all. A log that names no mutant is a
// run that stopped before the first one — a build, an install or a baseline
// that never returned. Like projectionNote it says what the log states and
// never why the run stopped, and it only ever adds detail: the row it lands on
// fails regardless, because a shard with no report is a run the fold cannot
// count.
func progressDetail(progress []mutationstages.Progress) []string {
	var out []string
	for _, p := range progress {
		says := "the log it left for " + p.Component + " "
		if !p.Started && p.Finished == 0 {
			out = append(out, says+"names no mutant, so the run stopped before any mutant ran")
			continue
		}
		finished := fmt.Sprintf("%d mutant(s) finished", p.Finished)
		if p.Projected {
			finished = fmt.Sprintf("%d of %d mutant(s) finished", p.Finished, p.Planned)
		}
		out = append(out, says+"says "+finished)
		for _, m := range p.InFlight {
			out = append(out, says+"names a mutant that started and never finished: "+m)
		}
	}
	return out
}

// foldedMutationLabel names every label this fold produces itself, so
// carryUnhandled can tell a row it replaced from one it has never seen.
func foldedMutationLabel(label string, decl component.File) bool {
	switch label {
	case "schedule", "shards", "mutation":
		return true
	}
	for _, l := range mutationWholeTreeRows {
		if label == l {
			return true
		}
	}
	if strings.HasPrefix(label, "read(") {
		return true
	}
	for _, c := range decl.Components {
		if label == mutationLabel(c.Name) {
			return true
		}
	}
	return false
}

// shardCountsRow says on a shard's row that the counts it wrote beside its
// report would not read.
//
// A shard that wrote none is a shard whose lydite is older than the document,
// and its row is left as it is: its counts come back out of its prose. A file
// that is there and will not parse is named: treated as absent it would have
// the fold quietly answer from the prose while the row still read `pass`.
func shardCountsRow(counts mutationstages.ShardCounts, row *ui.Row) {
	if counts.Err == nil {
		return
	}
	row.Status = ui.StatusFail
	row.Value += ", mutant counts not readable"
	row.Detail = []string{counts.Err.Error()}
}

// killedOf reads a component row's score back out of its value.
//
// It is how the fold reads a shard that wrote no mutants.json — an older
// lydite, or one whose document did not parse — and nothing else: the elapsed
// time the row states is not captured, because the counts document carries that
// span as a number and a fold reading it out of a sentence is one wording change
// away from a wrong total. A row whose value does not match contributes nothing,
// and the summary says how many components it covers, so a value this stops
// recognising shows up as a summary over fewer components rather than as a wrong
// number.
var killedOf = regexp.MustCompile(`^(\d+) of (\d+) mutant\(s\) (killed|survived) in .+$`)

// foldedMutationRow sums the shards' scores.
//
// The counts come from the shards' own mutants.json wherever one was read, so
// the figure is arithmetic over data rather than over sentences the fold
// happens to control the wording of. A component no shard's document holds
// falls back to its rendered row: a shard that could not contribute data still
// contributed a verdict, and degrading to the prose is what keeps its mutants
// in the total rather than silently out of it.
//
// It is `context` and gates nothing: survived == 0 for every component is
// survived == 0 for the repository, so a gating row here could only restate the
// conjunction of the rows above it — and each of those has already failed the
// run if it had a survivor.
//
// The elapsed time is the sum over the components that recorded one, which is
// machine time and not wall-clock: shards run beside each other, so no clock
// ever read this, and it is the same quantity an unsharded run's own summary
// reports for the same declaration. A component whose counts carried no time —
// a shard with no document, or one an older lydite wrote — is left out of it,
// and the row says how many contributed rather than counting them at nought.
func foldedMutationRow(inputs []shardInput, counts mutantsDoc, decl component.File) ui.Row {
	killed, total, covered, timed := 0, 0, 0, 0
	var elapsed time.Duration
	for _, c := range decl.Components {
		if s, ok := counts.Components[c.Name]; ok {
			// Every component the document holds ran, including one whose
			// mutants said nothing about the suite — its row is `unmeasured`
			// and its denominator nought, and counting it is what makes a
			// folded run report the component count an unsharded one does.
			n, of := s.Summary().Score()
			killed, total, covered = killed+n, total+of, covered+1
			if d, ok := s.Elapsed(); ok {
				elapsed += d
				timed++
			}
			continue
		}
		for _, row := range rowsFor(inputs, mutationLabel(c.Name)) {
			m := killedOf.FindStringSubmatch(row.Value)
			if m == nil {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			of, _ := strconv.Atoi(m[2])
			if m[3] == "survived" {
				n = of - n
			}
			killed, total, covered = killed+n, total+of, covered+1
		}
	}
	if covered == 0 {
		return ui.Row{Status: ui.StatusContext, Label: "mutation", Value: "no component was mutated"}
	}
	row := ui.Row{Status: ui.StatusContext, Label: "mutation",
		Value: fmt.Sprintf("%d of %d mutant(s) killed across %d component(s)", killed, total, covered)}
	switch {
	case timed == covered:
		row.Value += " in " + elapsed.Round(time.Second).String()
	case timed > 0:
		row.Value += fmt.Sprintf(" in %s, from the %d that recorded a time",
			elapsed.Round(time.Second), timed)
	default:
		// Said rather than rendered as `in 0s`: a total nothing measured is
		// indistinguishable from a run that took no time, and the shard that
		// recorded none is the one a reader has to go and look at.
		row.Detail = []string{"no shard recorded how long its components took"}
	}
	return row
}
