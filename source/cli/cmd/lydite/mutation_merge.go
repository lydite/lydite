package main

import (
	"errors"
	"fmt"
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
	inputs := readShards(rep, reports, "mutation", nil)

	problems := wholeTreeRows(rep, inputs, mutationWholeTreeRows)
	if row, ok := foldedScheduleRow(inputs); ok {
		rep.Add(row)
	}
	problems = append(problems, componentRows(rep, decl, inputs, mutationLabel)...)
	rep.Add(foldedMutationRow(inputs, decl))
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

// killedOf reads a component row's score back out of its value.
//
// A report's rows carry rendered prose rather than numbers, so folding reports
// cannot recover a count any other way — the alternative is a second document
// per shard carrying two integers, which is what `lydite test` needs because
// its figures are re-weighted and this one's are not. A row whose value does
// not match is not counted, and the summary says how many components it covers,
// so a value this stops recognising shows up as a summary over fewer components
// rather than as a wrong number.
var killedOf = regexp.MustCompile(`^(\d+) of (\d+) mutant\(s\) (killed|survived) in (.+)$`)

// foldedMutationRow sums the shards' scores.
//
// It is `context` and gates nothing: survived == 0 for every component is
// survived == 0 for the repository, so a gating row here could only restate the
// conjunction of the rows above it — and each of those has already failed the
// run if it had a survivor.
func foldedMutationRow(inputs []shardInput, decl component.File) ui.Row {
	killed, total, covered := 0, 0, 0
	var elapsed time.Duration
	for _, c := range decl.Components {
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
			if d, err := time.ParseDuration(m[4]); err == nil {
				elapsed += d
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
