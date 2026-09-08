package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/junit"
)

// measurementsName is the file a run writes what it measured to, inside the
// reports directory.
//
// A flat file and not a directory: readDocuments skips directories, so a
// component named `measurements` writing `.lydite-reports/measurements/test.log`
// cannot collide with this. readDocuments does read every *.json it finds, so
// this one name is skipped there — it is data a later command consumes, not a
// report anything renders.
const measurementsName = "measurements.json"

// measurementsDoc is what a run measured: the baseline candidate `lydite test
// record` lands, and the numbers `lydite test merge` composes the
// repository-wide figures from.
//
// It exists because the run that measures and the writes that follow are
// different jobs. Measuring executes the repository's suites and its
// setup/teardown shell; recording needs a token that can push, and composing
// needs every shard's answer at once. A document between them is what lets
// each of those hold what the measuring job must not.
//
// It is deliberately not a ui.Document. A report is rendered and read by a
// person; this is data with a schema its consumers depend on, and the two have
// different readers and different lifetimes. Keeping it out of ui.Document
// also keeps a coverage-shaped field off the type every command renders
// through.
type measurementsDoc struct {
	// Tree is the tree these measurements describe, and is what binds the
	// document to the checkout it may be recorded from. Without it a
	// mis-wired workflow records one tree's numbers under another tree's key,
	// silently, and that entry then gates every later change.
	Tree string `json:"tree"`
	// Components is each component's entry. Absent entirely when the run
	// measured nothing at all, which is not the same as an empty object and
	// is why Reason is beside it.
	Components map[string]componentMeasurement `json:"components,omitempty"`
	// Gated says this run compared what it measured against a baseline. It is
	// stated rather than inferred from an entry carrying one: a first
	// adoption gates every component against nothing and would otherwise be
	// indistinguishable from a run on the default branch, which compares
	// nothing on purpose. The fold reads it to decide whether its
	// repository-wide figures are a comparison or a measurement.
	Gated bool `json:"gated,omitempty"`
	// Reason says why there is nothing to record, and is empty exactly when
	// there is something. A document that simply omitted its components would
	// be indistinguishable from one that measured a repository with none.
	Reason string `json:"reason,omitempty"`
	// Tests is what became of each component's suite, for the components this
	// run ran.
	//
	// Beside Components rather than inside it, and the separation is
	// load-bearing. A component whose suite FAILED has test counts and
	// deliberately has no baseline entry: a report written by a run that
	// stopped early is not a measurement of the tree. Carrying the counts
	// inside Components would put that component in the map every
	// completeness check reads, so a failing suite would satisfy
	// missingFromRecord and land a baseline entry of nought covered lines —
	// which every later change then gates against.
	//
	// It is also why these are not a baseline at all. They are recorded in
	// the quality history, which asks what happened rather than what the tree
	// measures, and nothing compares them against a previous value.
	Tests map[string]junit.Counts `json:"tests,omitempty"`
}

// componentMeasurement is one component's contribution: what would be
// recorded, whether this run actually measured it, and what the fold needs to
// compose a figure over every component without touching the network.
type componentMeasurement struct {
	gitstate.Entry
	// Unanchored is the counts this run took, before a within-tolerance dip
	// was anchored back to the baseline's percentage. Absent when the two
	// agree, which is every entry but a tolerated dip.
	//
	// The two are different quantities and the fold needs both: what
	// `lydite test record` lands is the anchored entry, because recording a
	// dipped number verbatim turns the tolerance into an unbounded downward
	// ratchet — and what `coverage(repo)` sums is what was measured, because
	// an unsharded run composes from exactly that. Composing from the anchored
	// entry instead gives a sharded run headroom it did not earn, and prints a
	// total a reader cannot reach by adding up the rows above it.
	Unanchored *coverage.LineCount `json:"unanchored,omitempty"`
	// Carried marks an entry inherited from the base tree rather than
	// measured here, because affected selection did not run this component.
	//
	// A fold needs it, because the same component can appear in more than one
	// document and only one copy came from a suite that ran: taking the last
	// would record the base tree's number for a component the change rewrote.
	// It is also what a composed figure counts, so a row can say how much of
	// itself this run measured.
	Carried bool `json:"carried,omitempty"`
	// Patch is this component's changed lines and how many of them its report
	// covers, absent when the change touched none of them. `patch(repo)` is
	// summed over these — a report's rows carry rendered prose rather than
	// numbers, so folding reports could not recover them.
	Patch *patchCount `json:"patch,omitempty"`
	// Base is the baseline entry this component was gated against, absent for
	// a run that gated nothing and for one a different instrument measured. It
	// travels here so `lydite test merge` composes the baseline side of every
	// figure from what the shards already read, rather than reading the lydite
	// branch a second time.
	Base *gitstate.Entry `json:"base,omitempty"`
	// CRAP is this component's complexity scalars, absent for a component
	// lydite scores none of and for one whose score could not be taken.
	//
	// It rides on the entry rather than in a document of its own, because a
	// score is derived from the coverage measurement beside it and the two are
	// recorded by the same command from the same fold. They are separate
	// documents only where they are *stored*, which is where the cost of
	// coupling them falls: a change to what one records must not be a cache
	// miss in the other.
	//
	// No baseline entry beside it, unlike Base. The fold composes no CRAP
	// comparison — the per-component gate is strictly the stricter one, since
	// a change adding a function above the threshold to one component and
	// removing one from another fails there and nets to zero over the
	// repository — so nothing downstream has a comparison to make.
	CRAP *gitstate.CRAPEntry `json:"crap,omitempty"`
}

// patchCount is a component's changed lines, and how many of them its coverage
// report covers.
type patchCount struct {
	Hit   int `json:"hit"`
	Total int `json:"total"`
}

// measurementsPath is where a run writes what it measured, under root.
func measurementsPath(root string) string {
	return filepath.Join(reportsDir(root), measurementsName)
}

// writeMeasurements saves the document beside the run's report.
//
// Unconditionally, exactly as saveDocument writes the report: a measurement
// that reaches the recording step only when somebody remembered a flag records
// nothing when they forget.
func writeMeasurements(root string, doc measurementsDoc) error {
	if err := os.MkdirAll(reportsDir(root), 0o750); err != nil {
		return err
	}
	ignoreReports(reportsDir(root))
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(measurementsPath(root), append(data, '\n'), 0o600)
}

// readMeasurements loads one run's measurements from a reports directory.
func readMeasurements(dir string) (measurementsDoc, error) {
	path := filepath.Join(dir, measurementsName)
	data, err := os.ReadFile(path) // #nosec G304 -- the path is a reports directory the caller named
	if err != nil {
		return measurementsDoc{}, err
	}
	var doc measurementsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return measurementsDoc{}, fmt.Errorf("%s: %w", path, err)
	}
	// A document naming no tree cannot be bound to a checkout, so it cannot
	// be recorded at all. Refused here rather than defaulted, for the reason
	// ReadDocument refuses a document with no command: this is not a newer
	// shape, it is not a measurement.
	if doc.Tree == "" {
		return measurementsDoc{}, fmt.Errorf("%s: names no tree, so there is nothing it can be recorded against", path)
	}
	return doc, nil
}

// foldMeasurements merges the documents of a sharded run into one.
//
// Every document must describe the same tree. Shards that measured different
// trees are not parts of one run, and folding them would record a baseline no
// tree ever had — the numbers would each be right and the entry wrong.
//
// A measured entry beats a carried one, whichever order the documents arrive
// in, so a component appearing in several documents is recorded from the run
// that actually measured it. Two measured entries for one component mean two
// runs measured it; the first is kept and the fold does not pretend to
// arbitrate.
func foldMeasurements(docs []measurementsDoc) (measurementsDoc, error) {
	if len(docs) == 0 {
		return measurementsDoc{}, fmt.Errorf("no measurements were found in any of the named report directories")
	}
	out := measurementsDoc{Tree: docs[0].Tree, Components: map[string]componentMeasurement{}}
	var reasons []string
	for _, doc := range docs {
		if doc.Tree != out.Tree {
			return measurementsDoc{}, fmt.Errorf(
				"the measurements describe different trees (%s and %s), so they are not shards of one run",
				shortSHA(out.Tree), shortSHA(doc.Tree))
		}
		// Gated if any shard was. Every shard of one run passes the same
		// flags, so a mix means one of them could not reach a baseline at all
		// — and a figure composed over the rest is still a comparison for the
		// components that have one, which composedRow reports per component.
		if doc.Gated {
			out.Gated = true
		}
		if doc.Reason != "" {
			reasons = append(reasons, doc.Reason)
		}
		for name, counts := range doc.Tests {
			if out.Tests == nil {
				out.Tests = map[string]junit.Counts{}
			}
			// First wins, for the reason a measured entry beats a carried
			// one: every declared component belongs to exactly one shard, so
			// a second answer means two jobs ran the same work and the fold
			// does not pretend to arbitrate between them.
			if _, seen := out.Tests[name]; !seen {
				out.Tests[name] = counts
			}
		}
		for name, e := range doc.Components {
			if have, ok := out.Components[name]; ok && (!have.Carried || e.Carried) {
				continue
			}
			out.Components[name] = e
		}
	}
	if len(out.Components) == 0 {
		out.Reason = firstNonEmpty(reasons...)
		if out.Reason == "" {
			out.Reason = "no component produced a measurement"
		}
	}
	return out, nil
}

// snapshot is the fold as the documents the lydite branch stores, dropping
// everything that says how an entry was arrived at in one run rather than what
// is true of the tree.
func (d measurementsDoc) snapshot() gitstate.Snapshot {
	snap := gitstate.Snapshot{Coverage: gitstate.Baseline{}, CRAP: gitstate.CRAPBaseline{}}
	for name, e := range d.Components {
		snap.Coverage[name] = e.Entry
		if e.CRAP != nil {
			snap.CRAP[name] = *e.CRAP
		}
	}
	return snap
}

// measurementsFrom builds the document a run hands on: what it would record,
// what it actually measured, which entries it carried forward rather than
// measured, and what each was gated against.
func measurementsFrom(tree string, record, measured gitstate.Baseline, carried map[string]bool, scores gitstate.CRAPBaseline, baseline gitstate.Baseline, gated bool, parts []patchPart, tests map[string]junit.Counts) measurementsDoc {
	doc := measurementsDoc{Tree: tree, Gated: gated, Components: make(map[string]componentMeasurement, len(record)), Tests: tests}
	patch := make(map[string]patchCount, len(parts))
	for _, p := range parts {
		patch[p.Name] = patchCount{Hit: p.Hit, Total: p.Total}
	}
	for name, e := range record {
		m := componentMeasurement{Entry: e, Carried: carried[name]}
		if raw, ok := measured[name]; ok && raw.LineCount != e.LineCount {
			lines := raw.LineCount
			m.Unanchored = &lines
		}
		if p, ok := patch[name]; ok {
			m.Patch = &p
		}
		// Only a baseline the same instrument produced. `patch(repo)` is
		// composed straight from these counts, so an entry stored without
		// that check would have the fold compare across a change of
		// instrument — the comparison ADR 0025 exists to prevent, and one the
		// shard's own `patch(<name>)` row already refused.
		if b, ok := baseline[name]; ok && b.Measured() && b.Producer == e.Producer {
			m.Base = &b
		}
		if c, ok := scores[name]; ok {
			m.CRAP = &c
		}
		doc.Components[name] = m
	}
	return doc
}

// asMeasurement is one folded entry as the value the composition reads: the
// counts the run measured, never the anchored ones it would record.
//
// A carried entry has no measurement of its own and contributes the baseline's
// counts, which is exactly what an unsharded run composes for it.
func (e componentMeasurement) asMeasurement(c component.Component) measurement {
	lines := e.LineCount
	if e.Unanchored != nil {
		lines = *e.Unanchored
	}
	return measurement{Name: c.Name, Dir: c.Dir, Lang: langOf(c), Lines: lines, Producer: e.Producer}
}

// patchPartOf is this entry's contribution to `patch(repo)`, and false when
// the change touched none of the component's measurable lines.
func (e componentMeasurement) patchPartOf(name string) (patchPart, bool) {
	if e.Patch == nil {
		return patchPart{}, false
	}
	var base coverage.LineCount
	if e.Base != nil {
		base = e.Base.LineCount
	}
	return patchPart{Name: name, Hit: e.Patch.Hit, Total: e.Patch.Total, Base: base}, true
}

// testCounts is what each component's suite reported, for the components a run
// actually ran one for.
//
// Every component in ms, whether or not it produced a measurement: a suite
// that failed has counts and no measurement, which is exactly the pair this
// map exists to carry past a document keyed on what would be recorded.
func testCounts(w io.Writer, ms []measurement) map[string]junit.Counts {
	var out map[string]junit.Counts
	for _, m := range ms {
		if m.Tests == nil {
			// A report that was asked for and did not arrive is named, never
			// skipped in silence: a component contributing no counts is
			// indistinguishable in a history from one that ran no tests, and
			// the commonest cause is a repository whose own runner
			// configuration sent the report somewhere lydite does not look.
			// Stderr, because stdout carries the report and, under --json, a
			// document a warning would make unparseable.
			if m.TestsWhy != "" {
				_, _ = fmt.Fprintf(w, "warning: %s contributed no test counts: %s\n", m.Name, m.TestsWhy)
			}
			continue
		}
		if out == nil {
			out = map[string]junit.Counts{}
		}
		out[m.Name] = *m.Tests
	}
	return out
}
