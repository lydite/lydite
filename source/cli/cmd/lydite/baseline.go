package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/gitstate"
)

// candidateName is the file a run writes its candidate baseline to, inside the
// reports directory.
//
// A flat file and not a directory: readDocuments skips directories, so a
// component named `baseline` writing `.lydite-reports/baseline/test.log`
// cannot collide with this. readDocuments does read every *.json it finds, so
// this one name is skipped there — the candidate is data a later command
// consumes, not a report anything renders.
const candidateName = "baseline.json"

// candidateDoc is what a run would record, handed to `lydite test record` to
// land.
//
// It exists because the run that measures and the write to the lydite branch
// are different jobs: measuring executes the repository's suites and its
// setup/teardown shell, and recording needs a token that can push. A document
// between them is what lets the second hold the token while the first holds
// the code.
//
// It is deliberately not a ui.Document. A report is rendered and read by a
// person; this is data with a schema its consumer depends on, and the two have
// different readers and different lifetimes. Keeping it out of ui.Document
// also keeps a coverage-shaped field off the type every command renders
// through.
type candidateDoc struct {
	// Tree is the tree these measurements describe, and is what binds the
	// document to the checkout it may be recorded from. Without it a
	// mis-wired workflow records one tree's numbers under another tree's key,
	// silently, and that entry then gates every later change.
	Tree string `json:"tree"`
	// Components is each component's entry. Absent entirely when the run
	// established no candidate at all, which is not the same as an empty
	// object and is why Reason is beside it.
	Components map[string]candidateEntry `json:"components,omitempty"`
	// Reason says why there is nothing to record, and is empty exactly when
	// there is something. A document that simply omitted its components would
	// be indistinguishable from one that measured a repository with none.
	Reason string `json:"reason,omitempty"`
}

// candidateEntry is one component's contribution: what would be recorded, and
// whether this run actually measured it.
type candidateEntry struct {
	gitstate.Entry
	// Carried marks an entry inherited from the base tree rather than
	// measured here, because affected selection did not run this component.
	//
	// It is what lets shards be folded. A shard that ran `cli` carries `web`
	// and the shard that ran `web` carries `cli`, so both documents hold both
	// components and only this flag says which copy came from a suite that
	// actually ran. Folding without it would let a carried entry overwrite a
	// measured one, recording the base tree's number for a component this
	// change rewrote.
	Carried bool `json:"carried,omitempty"`
}

// candidatePath is where a run writes its candidate, under root.
func candidatePath(root string) string {
	return filepath.Join(reportsDir(root), candidateName)
}

// writeCandidate saves the candidate beside the run's report.
//
// Unconditionally, exactly as saveDocument writes the report: a measurement
// that reaches the recording step only when somebody remembered a flag records
// nothing when they forget.
func writeCandidate(root string, doc candidateDoc) error {
	if err := os.MkdirAll(reportsDir(root), 0o750); err != nil {
		return err
	}
	ignoreReports(reportsDir(root))
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(candidatePath(root), append(data, '\n'), 0o600)
}

// readCandidate loads one run's candidate from a reports directory.
func readCandidate(dir string) (candidateDoc, error) {
	path := filepath.Join(dir, candidateName)
	data, err := os.ReadFile(path) // #nosec G304 -- the path is a reports directory the caller named
	if err != nil {
		return candidateDoc{}, err
	}
	var doc candidateDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return candidateDoc{}, fmt.Errorf("%s: %w", path, err)
	}
	// A candidate naming no tree cannot be bound to a checkout, so it cannot
	// be recorded at all. Refused here rather than defaulted, for the reason
	// ReadDocument refuses a document with no command: this is not a newer
	// shape, it is not a candidate.
	if doc.Tree == "" {
		return candidateDoc{}, fmt.Errorf("%s: names no tree, so there is nothing it can be recorded against", path)
	}
	return doc, nil
}

// foldCandidates merges the candidates of a sharded run into one.
//
// Every document must describe the same tree. Shards that measured different
// trees are not parts of one run, and folding them would record a baseline no
// tree ever had — the numbers would each be right and the entry wrong.
//
// A measured entry beats a carried one, whichever order the documents arrive
// in. Each shard carries forward every component it did not run, so most
// components appear in every document and only one copy came from a suite
// that executed; taking the last would record the base tree's number for a
// component this change rewrote. Two measured entries for one component mean
// two shards ran it, which the planner does not emit — the first is kept and
// the fold does not pretend to arbitrate.
func foldCandidates(docs []candidateDoc) (candidateDoc, error) {
	if len(docs) == 0 {
		return candidateDoc{}, fmt.Errorf("no candidate baseline was found in any of the named report directories")
	}
	out := candidateDoc{Tree: docs[0].Tree, Components: map[string]candidateEntry{}}
	var reasons []string
	for _, doc := range docs {
		if doc.Tree != out.Tree {
			return candidateDoc{}, fmt.Errorf(
				"the candidates describe different trees (%s and %s), so they are not shards of one run",
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

// baseline is the fold as the map the lydite branch stores, dropping the
// carried flag — which says how an entry was arrived at in one run and is not
// a property of the tree.
func (d candidateDoc) baseline() gitstate.Baseline {
	out := gitstate.Baseline{}
	for name, e := range d.Components {
		out[name] = e.Entry
	}
	return out
}

// candidateFrom builds the document a run hands on, from what it would have
// recorded and which of those entries it measured.
func candidateFrom(tree string, record gitstate.Baseline, carried map[string]bool) candidateDoc {
	doc := candidateDoc{Tree: tree, Components: make(map[string]candidateEntry, len(record))}
	for name, e := range record {
		doc.Components[name] = candidateEntry{Entry: e, Carried: carried[name]}
	}
	return doc
}
