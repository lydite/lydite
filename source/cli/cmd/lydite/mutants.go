package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/mutation"
)

// mutantsName is the file a mutation run writes its counts to, inside the
// reports directory.
//
// It is `mutants.json` and not anything with "mutation" in it. The mutation is
// the act and the mutant is the artefact, and this document counts artefacts —
// but the harder constraint is that the name has to survive being read beside
// `mutation.json` in a directory listing and matched beside it in a workflow's
// `find`, which a near-homograph would not.
//
// A flat file and not a directory, for the reason measurementsName is: a
// component named `mutants` writes its logs under `mutants/`, and a file cannot
// collide with a directory. readDocuments does read every *.json it finds, so
// this one is skipped by name there — it carries no command and no verdict,
// because it is data a later command consumes rather than a report anything
// renders.
const mutantsName = "mutants.json"

// mutantsDoc is what a mutation run made of the mutants it generated: the
// scalars `lydite mutation merge` folds across shards and `lydite test record`
// lands in the quality history.
//
// It exists because the counts live nowhere else. `mutation.json` is a rendered
// ui.Document, so "3 ran out of memory" survives as a sentence and not as a
// number, and a fold could recover it only by parsing prose it happens to
// control today. This is the same answer measurements.json gave for coverage,
// for the same reason.
type mutantsDoc struct {
	// Tree is the tree these mutants came from, and is what binds the document
	// to the checkout it may be recorded from. Without it a mis-wired workflow
	// records one tree's counts under another tree's key, silently, and the
	// ledger is append-only — there is no later measurement of that commit's
	// diff to correct it with, because after the merge the diff is gone.
	Tree string `json:"tree"`
	// Components is the counts for each component that ran, and presence in it
	// is the whole "did this run at all" signal.
	//
	// A component that did not run is absent, never present with zeros.
	// Untouched by the diff, declared `mutation: false`, or in a shard that was
	// interrupted before reaching it — all three are the same answer, which is
	// that nothing measured this component. A component that ran and killed
	// every mutant is present with a survived count of nought: a measured zero,
	// and a different fact from absence. Zeros standing in for an absence would
	// record a perfect suite into a permanent history, and win any fold they
	// were part of.
	Components map[string]mutantCounts `json:"components,omitempty"`
}

// mutantCounts is what became of one component's mutants.
//
// It mirrors mutation.Summary rather than serialising it. Summary is the
// in-process tally and carries no JSON tags at all; this is a stored shape its
// readers depend on, and the two have different lifetimes — a rename inside the
// package must not rewrite the keys of every document already on disk.
type mutantCounts struct {
	Killed       int `json:"killed"`
	TimedOut     int `json:"timed_out"`
	OutOfMemory  int `json:"out_of_memory"`
	Survived     int `json:"survived"`
	Unviable     int `json:"unviable"`
	Acknowledged int `json:"acknowledged"`
}

// countsOf is one component's summary as the document stores it.
func countsOf(s mutation.Summary) mutantCounts {
	return mutantCounts{
		Killed:       s.Killed,
		TimedOut:     s.TimedOut,
		OutOfMemory:  s.OutOfMemory,
		Survived:     s.Survived,
		Unviable:     s.Unviable,
		Acknowledged: s.Acknowledged,
	}
}

// summary is the stored counts back as the tally every score is taken from, so
// a folded document and a single run compute a figure through one
// implementation.
func (c mutantCounts) summary() mutation.Summary {
	return mutation.Summary{
		Killed:       c.Killed,
		TimedOut:     c.TimedOut,
		OutOfMemory:  c.OutOfMemory,
		Survived:     c.Survived,
		Unviable:     c.Unviable,
		Acknowledged: c.Acknowledged,
	}
}

// mutantsPath is where a run writes its counts, under root.
func mutantsPath(root string) string {
	return filepath.Join(reportsDir(root), mutantsName)
}

// mutantsFrom builds the document one run hands on: the tree it mutated, and
// the counts for exactly the components that ran.
//
// A run that ran nothing still names its tree. The document says what was
// measured, and "this tree, and nothing on it" is an answer; a document naming
// no tree is not one at all.
func mutantsFrom(tree string, ran map[string]mutation.Summary) mutantsDoc {
	doc := mutantsDoc{Tree: tree}
	if len(ran) == 0 {
		return doc
	}
	doc.Components = make(map[string]mutantCounts, len(ran))
	for name, s := range ran {
		doc.Components[name] = countsOf(s)
	}
	return doc
}

// writeMutants saves the document beside the run's report.
//
// Unconditionally, exactly as saveDocument writes the report: a measurement
// that reaches the recording step only when somebody remembered a flag records
// nothing when they forget.
func writeMutants(root string, doc mutantsDoc) error {
	if err := os.MkdirAll(reportsDir(root), 0o750); err != nil {
		return err
	}
	ignoreReports(reportsDir(root))
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mutantsPath(root), append(data, '\n'), 0o600)
}

// readMutants loads one run's counts from a reports directory.
func readMutants(dir string) (mutantsDoc, error) {
	path := filepath.Join(dir, mutantsName)
	data, err := os.ReadFile(path) // #nosec G304 -- the path is a reports directory the caller named
	if err != nil {
		return mutantsDoc{}, err
	}
	var doc mutantsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return mutantsDoc{}, fmt.Errorf("%s: %w", path, err)
	}
	// A document naming no tree cannot be bound to a checkout, so nothing can
	// be recorded against it. Refused here rather than defaulted, for the
	// reason readMeasurements refuses the same shape: this is not a newer
	// document, it is not a measurement.
	if doc.Tree == "" {
		return mutantsDoc{}, fmt.Errorf("%s: names no tree, so there is nothing it can be recorded against", path)
	}
	return doc, nil
}

// foldMutants merges the documents of a sharded run into one.
//
// Every document must describe the same tree. Shards that mutated different
// trees are not parts of one run, and folding them would report a score no tree
// ever had — the numbers each right and the total wrong.
//
// A component belongs to exactly one shard, so a second entry for one component
// means two jobs ran the same work: the first is kept and the fold does not
// pretend to arbitrate between them. A shard that ran nothing contributes no
// entry rather than a zeroed one, so it cannot win that arbitration for a
// component another shard actually mutated.
func foldMutants(docs []mutantsDoc) (mutantsDoc, error) {
	if len(docs) == 0 {
		return mutantsDoc{}, fmt.Errorf("no mutant counts were found in any of the named report directories")
	}
	out := mutantsDoc{Tree: docs[0].Tree, Components: map[string]mutantCounts{}}
	for _, doc := range docs {
		if doc.Tree != out.Tree {
			return mutantsDoc{}, fmt.Errorf(
				"the mutant counts describe different trees (%s and %s), so they are not shards of one run",
				shortSHA(out.Tree), shortSHA(doc.Tree))
		}
		for name, counts := range doc.Components {
			if _, seen := out.Components[name]; seen {
				continue
			}
			out.Components[name] = counts
		}
	}
	return out, nil
}
