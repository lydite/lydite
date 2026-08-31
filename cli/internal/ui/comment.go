package ui

import (
	"fmt"
	"strings"
)

// Marker identifies lydite's standing comment on a pull request.
//
// It is an HTML comment in the body rather than a match on the author,
// because the author is whoever's token posted it, and that is not lydite's
// to rely on. It also survives someone editing the prose around it.
const Marker = "<!-- lydite:referral -->"

// markURL is the badge the comment opens with.
//
// Absolute and pinned to the default branch: the comment is posted into the
// pull request of whatever repository is being scanned, where a repository
// relative path resolves against that repository and resolves to nothing. A
// tag rather than a branch would stop resolving for consumers pinned to an
// older release. This path is a published interface — moving the file breaks
// the badge in every consumer's comment, retroactively.
const markURL = "https://raw.githubusercontent.com/lydite/lydite/main/assets/lydite-mark-64.png"

// CommentRow is one line of the comment's table.
//
// The columns follow docs/design/reference/surfaces.dc.html: what was
// checked, what the head says, and what the base says. Which facts fill them
// is lydite's to choose, and a referral's are not measurements — the head
// column carries what the change contains, and the base column what was read
// out of the merge-base, which is the only place the exemption set is ever
// read from.
type CommentRow struct {
	Check string
	Head  string
	Base  string
}

// CommentSection is a named list under the table.
type CommentSection struct {
	Title string
	Items []string
}

// Comment is the pull-request surface.
//
// It is rendered from the same verdict the terminal prints rather than
// re-derived, so the two cannot disagree about what happened — the property
// the JSON document already provides against the text output.
type Comment struct {
	// Verdict decides the badge, and is the report's own.
	Verdict Verdict
	// Headline is the one sentence under the badge. It says what is true
	// now, not what to do about it; the sections carry that.
	Headline string
	Rows     []CommentRow
	Sections []CommentSection
	// Version and Base are the footer. The design's footer also claims
	// parity with the reader's local run, which nothing can yet establish,
	// so it is absent rather than asserted.
	Version string
	Base    string
}

// badge is the verdict as the pull-request surface shows it.
//
// The three read differently on purpose. A referral is not a failure — it
// names no defect, and most referrals mean only that no exemption matched —
// so it must not borrow the failure's mark.
func badge(v Verdict) string {
	switch v {
	case VerdictPass:
		return "✅ **Passed**"
	case VerdictFail:
		return "❌ **Failed**"
	case VerdictRefer:
		return "⏸ **Referred**"
	default:
		return "**" + string(v) + "**"
	}
}

// Render returns the comment body as markdown.
func (c Comment) Render() string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	// A heading rather than a floated image: the mark scales with the
	// heading's own type, and nothing below it wraps around a float. The
	// asset is 64px, so 28 stays crisp on a 2x display instead of being
	// upscaled.
	fmt.Fprintf(&b, "### <img src=%q width=\"28\" align=\"top\" alt=\"\"> lydite\n\n", markURL)

	b.WriteString(badge(c.Verdict))
	if c.Headline != "" {
		b.WriteString(" — " + c.Headline)
	}
	b.WriteString("\n")

	if len(c.Rows) > 0 {
		b.WriteString("\n| Check | Head | Base |\n| --- | --- | --- |\n")
		for _, row := range c.Rows {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", cell(row.Check), cell(row.Head), cell(row.Base))
		}
	}

	for _, section := range c.Sections {
		if len(section.Items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n**%s**\n\n", section.Title)
		for _, item := range section.Items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
	}

	b.WriteString("\n---\n\n")
	footer := c.Version
	if c.Base != "" {
		footer += " · base " + c.Base
	}
	fmt.Fprintf(&b, "<sub><code>%s</code></sub>\n", escapeCell(footer))
	return b.String()
}

// cell renders a table cell, and an empty one as an em dash so a row never
// reads as a missing column.
func cell(s string) string {
	if s == "" {
		return "—"
	}
	return escapeCell(s)
}

// escapeCell keeps a value from breaking out of its cell.
//
// A value here can be a path, a rule name or a line quoted out of a diff, so
// it can contain anything the source contains — a pipe ends the cell early
// and shifts every column after it, and a newline ends the table. This is
// the same hazard indenting detail lines answers in the terminal grammar.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
