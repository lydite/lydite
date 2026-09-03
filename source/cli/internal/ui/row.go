// Package ui renders lydite's terminal surface and its machine-readable
// twin, so every command speaks one grammar rather than each growing its own.
//
// The grammar is specified in docs/design/tokens.md: a row is glyph, space,
// label, leader dots, value, with the value column aligned at 34 characters;
// the last line of a run is always the verdict plus its duration.
//
// Glyph and exit code are deliberately not the same axis. A run has exactly
// one verdict, and that verdict owns the exit code; a row's glyph only says
// how much attention the row wants. StatusUnmeasured and StatusDropped are
// the reason the two must come apart — a gate that did not run has to be
// visibly distinct from one that passed, which makes it amber, but it has
// never failed a build and must not start.
package ui

import (
	"strings"
	"unicode/utf8"
)

// Status is what one row reports. The set is closed: rendering a status the
// grammar has no glyph for would silently drop it to a context line, and a
// verdict nobody can see is the failure mode this package exists to prevent.
type Status string

const (
	// StatusPass is a check that ran and was satisfied.
	StatusPass Status = "pass"
	// StatusFail is a gate the author clears by doing more work.
	StatusFail Status = "fail"
	// StatusRefer is lydite handing the change to a human. It names no
	// defect: on day one, with an empty exemption set, every change is
	// referred including a perfect one.
	StatusRefer Status = "refer"
	// StatusUnmeasured is a gate that did not run — no tooling, no report,
	// a path-filtered CI job. Amber, because reading as a pass is exactly
	// how a green summary ships next to a real regression.
	StatusUnmeasured Status = "unmeasured"
	// StatusDropped is a measurement that a baseline has and the tree no
	// longer produces, because the source left the tree.
	StatusDropped Status = "dropped"
	// StatusNew is a measurement with nothing to compare against yet.
	StatusNew Status = "new"
	// StatusContext carries a reason, a cause, or a next step. Never a
	// verdict, so it never votes on the exit code.
	StatusContext Status = "context"
)

// glyph is the rendering half of the grammar. Four glyphs cover seven
// statuses because several statuses want the same amount of attention while
// voting differently — see the package doc.
func (s Status) glyph() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusFail:
		return "✗"
	case StatusRefer, StatusUnmeasured, StatusDropped:
		return "!"
	default:
		return "→"
	}
}

// Row is one line of the report, plus any detail lines hanging under it.
type Row struct {
	Status Status
	// Label names what was checked. It occupies the left column and is
	// truncated by nothing — a long label pushes the value right rather
	// than being cut, since a clipped package path is worse than a ragged
	// column.
	Label string
	// Value is the measurement or outcome. Empty renders the row as label
	// only, with no leader dots trailing into nothing.
	Value string
	// Detail is the reason, then the cause, then a runnable next step, per
	// the copy rules in docs/design/tokens.md. Rendered as indented context
	// lines, which is also what keeps a finding whose own message contains
	// a glyph from being read as a verdict.
	Detail []string
	// Log is where the whole of what this row's work printed was written,
	// relative to the scan root. Empty for a row that ran no command.
	//
	// It is not rendered in the text grammar — a failing row already carries
	// the path in its Detail, where the reader is looking, and a passing row
	// showing one would put a path nobody wants on every line of a clean run.
	// It is in the document because a consumer cannot parse a path back out
	// of prose: this is what lets a PR comment link the output of the one
	// component that failed.
	Log string
}

// valueColumn is where every row's value begins. Fixed rather than computed
// from the widest label in the run: a column that moves between runs cannot
// be compared by eye across two terminals, which is most of what alignment
// is for.
const valueColumn = 34

// render returns the row and its detail lines, already coloured if pal says
// so.
func (r Row) render(pal palette) []string {
	head := pal.paint(r.Status, r.Status.glyph()) + " " + r.Label
	if r.Value != "" {
		// gap is the run from the label's last character to the value
		// column, and the leaders fill it minus the space on either side.
		// Under three characters there is no room for a leader that reads
		// as one, so the row falls back to a plain separator rather than
		// emitting a lone dot.
		gap := valueColumn - (2 + utf8.RuneCountInString(r.Label))
		if gap < 3 {
			head += " " + r.Value
		} else {
			head += " " + pal.paint(StatusContext, strings.Repeat(".", gap-2)) + " " + r.Value
		}
	}
	lines := []string{head}
	for _, d := range r.Detail {
		lines = append(lines, pal.paint(StatusContext, "  "+StatusContext.glyph()+" "+d))
	}
	return lines
}
