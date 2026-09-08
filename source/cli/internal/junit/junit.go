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
