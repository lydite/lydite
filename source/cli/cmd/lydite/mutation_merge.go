package main

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/runner"
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
			decl, err := component.Load(dir)
			if err != nil {
				return err
			}
			// Completeness is a question about the declaration, and an empty
			// one answers every question with yes. `plan` and `lydite test
			// merge` refuse the same state for the same reason.
			if len(decl.Components) == 0 {
				return errors.New("no components declared in " + component.FileName +
					"\n       there is nothing to fold, and a fold over no component cannot report a shard that died")
			}
			rep := ui.NewReport("mutation")
			mergeMutationShards(rep, decl, reports)
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

// mergeMutationShards builds the folded report.
func mergeMutationShards(rep *ui.Report, decl component.File, reports []string) {
	var docs []mutantsDoc
	inputs := readShards(rep, reports, "mutation", func(dir string, _ *shardInput, row *ui.Row) {
		if doc, ok := readShardMutants(dir, row); ok {
			docs = append(docs, doc)
		}
	})

	problems := wholeTreeRows(rep, inputs, mutationWholeTreeRows)
	if row, ok := foldedScheduleRow(inputs); ok {
		rep.Add(row)
	}
	problems = append(problems, componentRows(rep, decl, inputs, mutationLabel)...)
	// A tree the shards disagree about is reported under `shards`, which is the
	// row that says these documents are not one run. The counts still fold from
	// the rows, because a fold that dropped them would answer a narrower
	// question than the one that failed.
	var counts mutantsDoc
	if len(docs) > 0 {
		folded, err := foldMutants(docs)
		if err != nil {
			problems = append(problems, err.Error())
		} else {
			counts = folded
		}
	}
	rep.Add(foldedMutationRow(inputs, counts, decl))
	carryUnhandled(rep, inputs, func(label string) bool { return foldedMutationLabel(label, decl) })
	shardsRow(rep, decl, inputs, problems)
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

// readShardMutants reads the counts a shard wrote beside its report.
//
// A shard that wrote none is a shard whose lydite is older than the document,
// which is a run whose counts have to come back out of its prose rather than a
// run that went missing. A file that is there and will not parse is neither,
// and is named on that shard's row: treated as absent it would have the fold
// quietly answer from the prose while the row still read `pass`.
func readShardMutants(dir string, row *ui.Row) (mutantsDoc, bool) {
	switch doc, err := readMutants(dir); {
	case err == nil:
		return doc, true
	case !errors.Is(err, fs.ErrNotExist):
		row.Status = ui.StatusFail
		row.Value += ", mutant counts not readable"
		row.Detail = []string{err.Error()}
	}
	return mutantsDoc{}, false
}

// killedOf reads a component row's score back out of its value.
//
// It is how the fold reads a shard that wrote no mutants.json — an older
// lydite, or one whose document did not parse — and it is how every shard's
// elapsed time is read, because the counts document carries what became of each
// mutant and not how long the component took to say so. A row whose value does
// not match contributes nothing, and the summary says how many components it
// covers, so a value this stops recognising shows up as a summary over fewer
// components rather than as a wrong number.
var killedOf = regexp.MustCompile(`^(\d+) of (\d+) mutant\(s\) (killed|survived) in (.+)$`)

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
func foldedMutationRow(inputs []shardInput, counts mutantsDoc, decl component.File) ui.Row {
	killed, total, covered := 0, 0, 0
	var elapsed time.Duration
	for _, c := range decl.Components {
		rows := rowsFor(inputs, mutationLabel(c.Name))
		for _, row := range rows {
			if m := killedOf.FindStringSubmatch(row.Value); m != nil {
				if d, err := time.ParseDuration(m[4]); err == nil {
					elapsed += d
				}
			}
		}
		if s, ok := counts.Components[c.Name]; ok {
			// Every component the document holds ran, including one whose
			// mutants said nothing about the suite — its row is `unmeasured`
			// and its denominator nought, and counting it is what makes a
			// folded run report the component count an unsharded one does.
			n, of := s.summary().Score()
			killed, total, covered = killed+n, total+of, covered+1
			continue
		}
		for _, row := range rows {
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
	return ui.Row{Status: ui.StatusContext, Label: "mutation",
		Value: fmt.Sprintf("%d of %d mutant(s) killed across %d component(s) in %s", killed, total, covered, elapsed)}
}
