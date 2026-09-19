// Package junit reads the test counts a runner's JUnit XML report holds.
//
// It exists for the quality-history ledger, which records how many tests a
// component ran and how many of them did not pass — numbers no coverage report
// carries, and numbers nothing can recompute once the commit they describe has
// been squashed away.
//
// One reader for every runner, because JUnit is the one report format all of
// them can be made to write: `go test` through the pinned gotestsum, cargo
// nextest through a tool config layered under the repository's own, and vitest
// through its junit reporter. A count parsed from each runner's own human
// output instead would be four parsers of four formats that are each the
// runner's to change without notice.
//
// The same elements answer a second question, for a second reader: what became
// of one named test. The flaky gate asks whether a test the change introduced
// agrees with itself across two runs, which is a comparison of two reports test
// by test rather than of two totals — so ReadOutcomes keeps the names Read has
// no use for, off the same document and by the same rule.
//
// Counts come from the <testcase> elements and never from the summary
// attributes on <testsuites>. Producers disagree about those: nextest omits
// ignored tests from its `tests` attribute while gotestsum includes them, and
// neither records a top-level skipped count at all. An element is a test the
// producer decided to report, and its children say what became of it, which is
// the one part of the format every producer spells the same way.
package junit

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
)

// Counts is what became of one report's tests.
//
// Three numbers rather than a pass count, because passing is the one of the
// four that is derivable: Total − Failed − Skipped. Storing it as well would
// give a reader two quantities free to disagree.
type Counts struct {
	// Total is how many tests the report holds.
	Total int `json:"total"`
	// Failed is how many of them the producer recorded a failure or an error
	// against. The two are one number here: JUnit's distinction is between a
	// test that asserted and lost and one that died before it could, and both
	// are a test that did not pass.
	Failed int `json:"failed"`
	// Skipped is how many were reported without being run.
	Skipped int `json:"skipped"`
}

// Empty reports whether the report held no test at all.
//
// A report with no tests is not a component that passed everything; it is far
// more likely a runner that collected nothing, so a caller records it as
// unmeasured rather than as a run of nought tests.
func (c Counts) Empty() bool { return c.Total == 0 }

// ReadFile reads the counts one JUnit report holds.
func ReadFile(path string) (Counts, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is one a runner declared it writes to
	if err != nil {
		return Counts{}, err
	}
	defer func() { _ = f.Close() }()
	c, err := Read(f)
	if err != nil {
		return Counts{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Outcome is what a report records became of one test.
//
// Three values and not four: a pass is the absence of a child element saying
// otherwise, which is the one part of the format every producer spells the
// same way, and a test the report does not hold at all is absence from the map
// rather than a value in it. A gate that reads "did this test agree with
// itself" has to tell "it passed" from "nothing ran it", and an Outcome that
// could mean either is what would let it report the second as the first.
type Outcome int

const (
	// Pass is a testcase the producer recorded nothing against.
	Pass Outcome = iota
	// Fail is a failure or an error, which are one outcome here for the
	// reason Counts.Failed folds them: both are a test that did not pass.
	Fail
	// Skip is a test reported without being run.
	Skip
)

// String names the outcome as a report's reader would say it.
func (o Outcome) String() string {
	switch o {
	case Fail:
		return "failed"
	case Skip:
		return "skipped"
	default:
		return "passed"
	}
}

// ReadOutcomesFile reads one JUnit report's per-test outcomes.
func ReadOutcomesFile(path string) (map[string]Outcome, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is one a runner declared it writes to
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out, err := ReadOutcomes(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// ReadOutcomes maps each test the report holds to what became of it, by the
// name the producer recorded.
//
// The name alone and not the class beside it, because the caller asking this
// question knows a test by the name `go test -run` matches — the classname is
// the producer's own idea of a suite, an import path for gotestsum and a binary
// for nextest, and a caller holding a package directory cannot derive either.
// A report recording one name twice keeps the worse outcome, Fail over Skip
// over Pass: the two records are a real ambiguity, and resolving it towards the
// outcome that reports rather than the one that stays quiet leaves a caller
// with noise instead of a silence it cannot see.
//
// Subtests are in the map under their own full names — `TestParent/child`
// beside `TestParent` — because they are testcases of their own and dropping
// them would make this reader disagree with Read about what the report holds.
// A parent's own record already aggregates its children, so a caller keyed on
// top-level names reads the aggregate and never has to roll one up.
func ReadOutcomes(r io.Reader) (map[string]Outcome, error) {
	return readOutcomes(r, func(_, name string) string { return name })
}

// ClassKey is the key ReadOutcomesByClass records a test under: the classname
// and the name with a NUL between them, which no producer can write into
// either half, so no pair of parts can spell another pair's key.
func ClassKey(classname, name string) string { return classname + "\x00" + name }

// ReadOutcomesByClassFile reads one JUnit report's per-test outcomes, keyed by
// classname and name together.
func ReadOutcomesByClassFile(path string) (map[string]Outcome, error) {
	f, err := os.Open(path) // #nosec G304 -- the path is one a runner declared it writes to
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out, err := ReadOutcomesByClass(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// ReadOutcomesByClass maps each test the report holds to what became of it,
// under ClassKey of the classname and name the producer recorded.
//
// A caller reaches for this rather than ReadOutcomes when a name alone is not
// an identity in the language the report came from. One `go test` process is
// scoped to one package where the compiler forbids two functions with a name,
// so ReadOutcomes cannot collide there; cargo nextest runs every binary in a
// crate in one invocation and vitest every file in a component, and both write
// one report over the lot. nextest-suite.xml records `shared_name` under both
// `nextestprobe::a` and `nextestprobe::b` — two tests in two binaries, which a
// name-keyed map merges into one entry holding the worse of the two. For the
// ledger's totals that merge is harmless, but a gate comparing two runs of a
// named test reads it as one test that disagreed with itself, or worse, lets a
// pass in one binary mask a flake in another.
//
// A classname and name recorded twice resolve the way ReadOutcomes resolves a
// repeated name, and for the same reason: the worse outcome, Fail over Skip
// over Pass. A testcase missing either half is not addressable by a caller
// holding both, and is recorded under neither.
func ReadOutcomesByClass(r io.Reader) (map[string]Outcome, error) {
	return readOutcomes(r, func(classname, name string) string {
		if classname == "" || name == "" {
			return ""
		}
		return ClassKey(classname, name)
	})
}

// readOutcomes walks the <testcase> elements and records each under the key
// keyOf gives it, empty meaning a case no caller can address. The two readers
// differ only in that key: one document, one bound on what counts as a test's
// outcome, so a fix to either is a fix to both.
func readOutcomes(r io.Reader, keyOf func(classname, name string) string) (map[string]Outcome, error) {
	dec := xml.NewDecoder(r)
	out := map[string]Outcome{}
	// The key of the <testcase> currently open, empty outside one, so that an
	// element named `failure` elsewhere in the document cannot be read as some
	// test's outcome — the bound Read already keeps.
	key := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testcase":
				key = keyOf(attr(t, "classname"), attr(t, "name"))
				if key != "" {
					record(out, key, Pass)
				}
			case "failure", "error":
				if key != "" {
					record(out, key, Fail)
				}
			case "skipped":
				if key != "" {
					record(out, key, Skip)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "testcase" {
				key = ""
			}
		}
	}
	return out, nil
}

// record keeps the worse of what is already known about a key and what has
// just been read. A testcase carrying both a failure and a skipped child, and
// two packages recording one name, resolve the same way.
func record(out map[string]Outcome, key string, o Outcome) {
	if prev, ok := out[key]; ok && worse(prev, o) {
		return
	}
	out[key] = o
}

// worse orders the outcomes for record: Fail over Skip over Pass.
func worse(a, b Outcome) bool {
	return rank(a) >= rank(b) // [lydite:exclude_from_mutation][rank is injective over the three outcomes, so a tie only occurs when a == b — record then overwrites out[name] with the value it already holds, which no observation can tell from skipping the write]
}

func rank(o Outcome) int {
	switch o {
	case Fail:
		return 2
	case Skip:
		return 1
	default:
		return 0
	}
}

// attr reads one attribute off an element, answering "" for an absent one.
func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// Read counts the tests in a JUnit report.
//
// The document is walked as a token stream rather than unmarshalled into a
// tree, because a report is one line per test and a large suite's is
// megabytes: the counts are three integers whatever the size, and holding the
// whole document to arrive at them would make a repository's own test volume
// the thing that decides whether its ledger entry can be written.
func Read(r io.Reader) (Counts, error) {
	dec := xml.NewDecoder(r)
	var c Counts
	// depth of nesting inside the <testcase> currently open, so that an
	// element named `failure` somewhere else in the document — a producer's
	// own <properties>, or a nested suite's summary — cannot be read as this
	// test's outcome.
	inCase := false
	// One outcome per test. A producer may write several <failure> elements
	// for one testcase (a test that failed and whose teardown then errored),
	// and counting each would report more failures than there were tests.
	counted := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Counts{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testcase":
				inCase, counted = true, false
				c.Total++
			case "failure", "error":
				if inCase && !counted {
					c.Failed++
					counted = true
				}
			case "skipped":
				if inCase && !counted {
					c.Skipped++
					counted = true
				}
			}
		case xml.EndElement:
			if t.Name.Local == "testcase" {
				inCase = false
			}
		}
	}
	return c, nil
}
