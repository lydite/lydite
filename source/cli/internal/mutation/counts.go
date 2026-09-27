package mutation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CountsFileName is the file a mutation run writes its counts to, inside the
// reports directory.
//
// It is `mutants.json` and not anything with "mutation" in it. The mutation is
// the act and the mutant is the artefact, and this document counts artefacts —
// but the harder constraint is that the name has to survive being read beside
// `mutation.json` in a directory listing and matched beside it in a workflow's
// `find`, which a near-homograph would not.
//
// A flat file and not a directory: a component named `mutants` writes its logs
// under `mutants/`, and a file cannot collide with a directory. It carries no
// command and no verdict, because it is data a later command consumes rather
// than a report anything renders.
const CountsFileName = "mutants.json"

// CountsDocument is what a mutation run made of the mutants it generated: the
// scalars `lydite mutation merge` folds across shards and `lydite test record`
// lands in the quality history.
//
// It exists because the counts live nowhere else. A rendered report turns "3
// ran out of memory" into a sentence, and a fold could recover the number only
// by parsing prose the report happens to control today.
type CountsDocument struct {
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
	Components map[string]ComponentCounts `json:"components,omitempty"`
}

// ComponentCounts is what became of one component's mutants, and how long that
// took.
//
// It mirrors Summary rather than serialising it. Summary is the in-process
// tally and carries no JSON tags at all; this is a stored shape its readers
// depend on, and the two have different lifetimes — a rename inside the
// package must not rewrite the keys of every document already on disk.
type ComponentCounts struct {
	Killed       int `json:"killed"`
	TimedOut     int `json:"timed_out"`
	OutOfMemory  int `json:"out_of_memory"`
	Survived     int `json:"survived"`
	Unviable     int `json:"unviable"`
	Acknowledged int `json:"acknowledged"`
	// ElapsedSeconds is how long the component took to say all that: its
	// baseline suite and every mutant after it, the same span its row renders.
	//
	// It is data here because the fold and the ledger both need the number, and
	// the only other place it exists is a sentence the row happens to word this
	// way today. Seconds as a float rather than a count of some unit a reader
	// has to guess at, and never a duration string: a stored shape is parsed by
	// readers this document outlives.
	//
	// Nought means no elapsed time was recorded, which is a document an older
	// lydite wrote. A component that ran cannot produce it — a baseline suite
	// and at least one mutant take a measurable time — so a reader may treat
	// the two as the same answer, and must not report nought as a run that
	// took no time.
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
}

// Elapsed is the stored seconds back as a duration, and false where the
// document recorded none.
func (c ComponentCounts) Elapsed() (time.Duration, bool) {
	if c.ElapsedSeconds == 0 {
		return 0, false
	}
	return time.Duration(c.ElapsedSeconds * float64(time.Second)), true
}

// Summary is the stored counts back as the tally every score is taken from, so
// a folded document and a single run compute a figure through one
// implementation.
func (c ComponentCounts) Summary() Summary {
	return Summary{
		Killed:       c.Killed,
		TimedOut:     c.TimedOut,
		OutOfMemory:  c.OutOfMemory,
		Survived:     c.Survived,
		Unviable:     c.Unviable,
		Acknowledged: c.Acknowledged,
	}
}

// CountsOf is one component's summary and the time it took, as the document
// stores them.
func CountsOf(s Summary, elapsed time.Duration) ComponentCounts {
	return ComponentCounts{
		Killed:         s.Killed,
		TimedOut:       s.TimedOut,
		OutOfMemory:    s.OutOfMemory,
		Survived:       s.Survived,
		Unviable:       s.Unviable,
		Acknowledged:   s.Acknowledged,
		ElapsedSeconds: elapsed.Seconds(),
	}
}

// ReadCounts loads one run's counts from a reports directory.
func ReadCounts(reportsDir string) (CountsDocument, error) {
	path := filepath.Join(reportsDir, CountsFileName)
	data, err := os.ReadFile(path) // #nosec G304 -- the path is a reports directory the caller named
	if err != nil {
		return CountsDocument{}, err
	}
	var doc CountsDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return CountsDocument{}, fmt.Errorf("%s: %w", path, err)
	}
	// A document naming no tree cannot be bound to a checkout, so nothing can
	// be recorded against it. Refused here rather than defaulted: this is not a
	// newer document, it is not a measurement.
	if doc.Tree == "" {
		return CountsDocument{}, fmt.Errorf("%s: names no tree, so there is nothing it can be recorded against", path)
	}
	return doc, nil
}

// WriteCounts saves the document into a reports directory.
//
// Unconditionally: a measurement that reaches the write only when somebody
// remembered a flag records nothing when they forget.
func WriteCounts(reportsDir string, doc CountsDocument) error {
	if err := os.MkdirAll(reportsDir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(reportsDir, CountsFileName), append(data, '\n'), 0o600)
}

// FoldCounts merges the documents of a sharded run into one.
//
// Every document must describe the same tree. Shards that mutated different
// trees are not parts of one run, and folding them would report a score no
// tree ever had — the numbers each right and the total wrong.
//
// A component belongs to exactly one shard, so a second entry for one
// component means two jobs ran the same work: the first is kept and the fold
// does not pretend to arbitrate between them. A shard that ran nothing
// contributes no entry rather than a zeroed one, so it cannot win that
// arbitration for a component another shard actually mutated.
func FoldCounts(docs []CountsDocument) (CountsDocument, error) {
	if len(docs) == 0 {
		return CountsDocument{}, fmt.Errorf("no mutant counts were found in any of the named report directories")
	}
	out := CountsDocument{Tree: docs[0].Tree, Components: map[string]ComponentCounts{}}
	for _, doc := range docs {
		if doc.Tree != out.Tree {
			return CountsDocument{}, fmt.Errorf(
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

// shortSHA truncates a tree hash to what an error needs to name a disagreement
// without printing the whole thing.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
