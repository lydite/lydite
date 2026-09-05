package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/gitstate"
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
	// Reason says why there is nothing to record, and is empty exactly when
	// there is something. A document that simply omitted its components would
	// be indistinguishable from one that measured a repository with none.
	Reason string `json:"reason,omitempty"`
}

// componentMeasurement is one component's contribution: what would be
// recorded, whether this run actually measured it, and what the fold needs to
// compose a figure over every component without touching the network.
type componentMeasurement struct {
	gitstate.Entry
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
	// a run that gated nothing and for one a different instrument measured. It travels here so `lydite test merge`
	// composes the baseline side of every figure from what the shards already
	// read, rather than reading the lydite branch a second time.
	Base *gitstate.Entry `json:"base,omitempty"`
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
		if doc.Reason != "" {
			reasons = append(reasons, doc.Reason)
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

// baseline is the fold as the map the lydite branch stores, dropping
// everything that says how an entry was arrived at in one run rather than what
// is true of the tree.
func (d measurementsDoc) baseline() gitstate.Baseline {
	out := gitstate.Baseline{}
	for name, e := range d.Components {
		out[name] = e.Entry
	}
	return out
}

// measurementsFrom builds the document a run hands on, from what it would have
// recorded, which of those entries it measured, and what each was gated
// against.
func measurementsFrom(tree string, record gitstate.Baseline, carried map[string]bool, baseline gitstate.Baseline, parts []patchPart) measurementsDoc {
	doc := measurementsDoc{Tree: tree, Components: make(map[string]componentMeasurement, len(record))}
	patch := make(map[string]patchCount, len(parts))
	for _, p := range parts {
		patch[p.Name] = patchCount{Hit: p.Hit, Total: p.Total}
	}
	for name, e := range record {
		m := componentMeasurement{Entry: e, Carried: carried[name]}
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
		doc.Components[name] = m
	}
	return doc
}

// asMeasurement is one folded entry as the value the composition reads.
func (e componentMeasurement) asMeasurement(c component.Component) measurement {
	return measurement{Name: c.Name, Dir: c.Dir, Lang: langOf(c), Lines: e.LineCount, Producer: e.Producer}
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
