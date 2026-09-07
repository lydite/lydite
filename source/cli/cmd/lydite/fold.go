package main

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/ui"
)

// A fold answers one question, and every command that shards asks it: did each
// declared component report exactly once?
//
// Every shard reports exactly the components it was responsible for, so a
// component with no row is a shard whose job died and one with two rows is two
// jobs that ran the same work. That is what makes completeness a question about
// the declaration and the documents, needing no third input — and it is why a
// gap is a failure rather than an `unmeasured` row, which does not vote and
// would let a half-run repository publish a passing verdict.
//
// The rule is here rather than in each command's merge because two copies of it
// would agree until one learned something the other had not, and the copy that
// drifts is the one nobody is looking at. What each command keeps for itself is
// the part that is genuinely its own: `lydite test merge` composes the figures
// over the repository from the shards' measurements, and `lydite mutation
// merge` sums a score.

// shardInput is one shard's directory, and what was read out of it.
type shardInput struct {
	dir  string
	doc  ui.Document
	read bool
	// measured is the shard's measurements, absent for a shard that ran with
	// --no-coverage or wrote none.
	measured measurementsDoc
}

// readShards reads each named directory's report for one command, adding a row
// per directory so a folded report says what it was folded from.
//
// alongside reads whatever else that command's shards wrote beside the report
// and says so on the same row — `lydite test` writes measurements, and nothing
// else does. On the same row rather than in a second pass, because a second row
// per directory under the same label is what a consumer keying rows by label
// cannot survive.
func readShards(rep *ui.Report, reports []string, command string, alongside func(dir string, in *shardInput, row *ui.Row)) []shardInput {
	inputs := make([]shardInput, 0, len(reports))
	for _, dir := range reports {
		in := shardInput{dir: dir}
		doc, err := readDocument(documentPath(dir, command))
		if err != nil {
			rep.Add(ui.Row{Status: ui.StatusFail, Label: "read(" + dir + ")",
				Value: "no " + command + " report", Detail: []string{err.Error()}})
			inputs = append(inputs, in)
			continue
		}
		in.doc, in.read = doc, true
		row := ui.Row{Status: ui.StatusContext, Label: "read(" + dir + ")",
			Value: fmt.Sprintf("%d row(s), %s", len(doc.Rows), doc.Verdict)}
		if alongside != nil {
			alongside(dir, &in, &row)
		}
		rep.Add(row)
		inputs = append(inputs, in)
	}
	return inputs
}

// readTestMeasurements is that hook for `lydite test merge`.
//
// A shard run with --no-coverage writes no measurements at all, which is a run
// that gated nothing rather than a run that went missing. A file that is there
// and will not parse is neither, and is named: treated as absent it would leave
// that shard's components composing nothing while the row still read `pass`.
func readTestMeasurements(dir string, in *shardInput, row *ui.Row) {
	switch m, err := readMeasurements(dir); {
	case err == nil:
		in.measured = m
	case !errors.Is(err, fs.ErrNotExist):
		row.Status = ui.StatusFail
		row.Value += ", measurements not readable"
		row.Detail = []string{err.Error()}
	}
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

// collapse returns the rows under one label and whether every shard that wrote
// one wrote the same row.
//
// The rows rather than the single agreed one, because a caller that has to
// show a disagreement needs all of them and would otherwise ask for them
// again — and a second value saying "there is a row" would only restate the
// length of the first.
func collapse(inputs []shardInput, label string) (found []ui.Row, agreed bool) {
	found = rowsFor(inputs, label)
	if len(found) == 0 {
		return nil, true
	}
	for _, r := range found[1:] {
		if !sameRow(r, found[0]) {
			return found, false
		}
	}
	return found, true
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
				// The largest any shard reached, written as a clamp: a
				// conditional whose boundary assigns the value already held
				// is a branch nothing can be asked about.
				if n, err := strconv.Atoi(m[1]); err == nil {
					best = max(best, n)
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

// wholeTreeRow folds the gates that ask about the declaration and the tree
// rather than about any component, so every shard computes the same answer.
// They collapse to one, and a disagreement means the shards did not see the
// same tree — which makes every other row in the fold suspect.
func wholeTreeRows(rep *ui.Report, inputs []shardInput, labels []string) []string {
	var problems []string
	for _, label := range labels {
		found, agreed := collapse(inputs, label)
		if !agreed {
			problems = append(problems, fmt.Sprintf(
				"the shards disagree about %s, so they did not all see the same tree", label))
			continue
		}
		if len(found) > 0 {
			rep.Add(found[0])
		}
	}
	return problems
}

// componentRows adds the one row each declared component takes across the
// shards, in declaration order, and names every component that has none or has
// more than one.
//
// A component no shard reported is a shard that died; one two shards reported
// is two jobs running the same work, and a consumer keying rows by label picks
// one of two answers.
func componentRows(rep *ui.Report, decl component.File, inputs []shardInput, label func(string) string) []string {
	var problems []string
	for _, c := range decl.Components {
		found := rowsFor(inputs, label(c.Name))
		switch len(found) {
		case 0:
			problems = append(problems, c.Name+" has no row in any shard's report")
		case 1:
			rep.Add(found[0])
		default:
			problems = append(problems, fmt.Sprintf("%s has a row in %d shards' reports", c.Name, len(found)))
		}
	}
	return problems
}

// unhandledLabels are the labels the shards wrote that the fold has no rule
// for, in the order they first appear.
func unhandledLabels(inputs []shardInput, folded func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, in := range inputs {
		for _, row := range in.doc.Rows {
			if seen[row.Label] || folded(row.Label) {
				continue
			}
			seen[row.Label] = true
			out = append(out, row.Label)
		}
	}
	return out
}

// carryUnhandled adds every row the fold has no rule for, once when the shards
// agree about it and once per shard when they do not.
//
// A row merge cannot arbitrate must not be silently reduced to one shard's copy.
func carryUnhandled(rep *ui.Report, inputs []shardInput, folded func(string) bool) {
	for _, label := range unhandledLabels(inputs, folded) {
		found, agreed := collapse(inputs, label)
		if agreed {
			// One row carries them all. Sliced rather than indexed: every
			// label here came from a row some shard wrote, so there is always
			// one — but that invariant lives in unhandledLabels, and a fold
			// that panicked when it stopped holding would be worse than one
			// that carried nothing.
			found = found[:min(len(found), 1)]
		}
		for _, r := range found {
			rep.Add(r)
		}
	}
}

// shardsRow is the one row saying whether the shards fold into a complete run.
//
// It is added last because the verdict check inside it reads the rows above,
// and because two rows under one label is what a consumer keying rows by label
// cannot survive — which is the very failure this row exists to report.
func shardsRow(rep *ui.Report, decl component.File, inputs []shardInput, problems []string) {
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
