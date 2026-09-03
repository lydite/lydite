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
//
// One marker, because there is one comment. A change gets a single standing
// verdict covering every concern lydite has about it; a second marker would be
// a second comment, and a pull request accumulating one per command is the
// surface nobody reads.
const Marker = "<!-- lydite:results -->"

// markURL is the badge the comment opens with.
//
// Absolute and pinned to the default branch: the comment is posted into the
// pull request of whatever repository is being scanned, where a repository
// relative path resolves against that repository and resolves to nothing. A
// tag rather than a branch would stop resolving for consumers pinned to an
// older release. This path is a published interface — moving the file breaks
// the badge in every consumer's comment, retroactively.
const markURL = "https://raw.githubusercontent.com/lydite/lydite/main/assets/lydite-mark-64.png"

// CommentRow is one line of a section's table: what was checked, and what it
// answered.
//
// Two columns and not the reference prototype's three. That design was drawn
// for a referral, whose facts are what the change contains and what was read
// out of the merge-base. A report row carries a label and a value, so a third
// column could only be filled by inventing a fact or left empty on every row.
type CommentRow struct {
	Check  string
	Result string
}

// CommentDetail is a collapsed block of output under a section.
//
// It carries what a failing row printed, which is the thing a reader wants
// next and the thing a comment has never been able to give them. Collapsed
// because it is dozens of lines: a comment that pastes them inline buries the
// verdict it exists to deliver.
type CommentDetail struct {
	Title string
	Lines []string
	// Log names where the whole of the output is, for the reader whom the
	// tail did not answer. It is prose here rather than a link, because the
	// path is inside a CI artifact and lydite cannot know its URL.
	Log string
}

// CommentSection is one concern's part of the comment — one command's report.
//
// Every section is collapsible, and one that failed opens itself. A reader
// arriving at a red comment sees what failed without a click, and the four
// concerns that did not fail stay one line each.
type CommentSection struct {
	// Status decides the glyph and whether the section opens. It is the
	// row-level status rather than a verdict, so an unmeasured section is
	// visibly distinct from a passing one — a section that could not run must
	// never render as one that ran and found nothing.
	Status Status
	Title  string
	// Summary is the counts after the title, on the one line that is visible
	// while the section is shut.
	Summary string
	Rows    []CommentRow
	Items   []string
	Details []CommentDetail
}

// Comment is the pull-request surface.
//
// It is rendered from the same documents the terminal printed rather than
// re-derived, so the two cannot disagree about what happened — the property
// the JSON document already provides against the text output.
type Comment struct {
	// Verdict decides the badge, and is the worst of the sections'.
	Verdict Verdict
	// Headline is the one sentence under the badge. It says what is true
	// now, not what to do about it; the sections carry that.
	Headline string
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
// so it must not borrow the failure's mark, and the words say what is wanted
// rather than what is wrong.
func badge(v Verdict) string {
	switch v {
	case VerdictPass:
		return "✅ **Looks good**"
	case VerdictFail:
		return "❌ **Failed**"
	case VerdictRefer:
		return "⏸ **Needs attention**"
	default:
		return "**" + string(v) + "**"
	}
}

// Render returns the comment body as markdown.
func (c Comment) Render() string {
	var b strings.Builder
	b.WriteString(Marker + "\n")
	// Rendered at the asset's own 64px rather than scaled down: the mark is
	// the first thing identifying whose verdict this is, and both dimensions
	// are given so the row's height is known before the image loads and the
	// comment does not reflow under the reader.
	fmt.Fprintf(&b, "<img src=%q width=\"64\" height=\"64\" align=\"left\" alt=\"lydite\">\n\n", markURL)

	b.WriteString(badge(c.Verdict))
	if c.Headline != "" {
		b.WriteString(" — ")
		b.WriteString(c.Headline)
	}
	b.WriteString("\n")

	for _, section := range c.Sections {
		section.render(&b)
	}

	b.WriteString("\n---\n\n")
	footer := c.Version
	if c.Base != "" {
		footer += " · base " + c.Base
	}
	fmt.Fprintf(&b, "<sub><code>%s</code></sub>\n", escapeCell(footer))
	return b.String()
}

// mark is a section's status as the pull-request surface shows it.
//
// Not the terminal's glyph. The text grammar has four marks for seven statuses
// because a terminal is monochrome and aligned to a column; a comment is
// neither, and a reader scanning four shut sections is picking which to open,
// which is what colour does for them here. The three that vote share their
// vocabulary with the badge deliberately, so a section and the headline above
// it read as one sentence.
func mark(s Status) string {
	switch s {
	case StatusPass:
		return "✅"
	case StatusFail:
		return "❌"
	case StatusRefer:
		return "⏸"
	default:
		return "⚠️"
	}
}

// render writes one section, opened when it is the one the reader came for.
func (s CommentSection) render(b *strings.Builder) {
	open := ""
	if s.Status == StatusFail || s.Status == StatusRefer {
		open = " open"
	}
	summary := mark(s.Status) + " <b>" + escapeCell(s.Title) + "</b>"
	if s.Summary != "" {
		summary += " — " + escapeCell(s.Summary)
	}
	// The blank lines inside <details> are load-bearing: GitHub stops
	// rendering markdown inside an HTML block that runs on without one, so a
	// table written flush against the summary is shown as its own source.
	fmt.Fprintf(b, "\n<details%s>\n<summary>%s</summary>\n\n", open, summary)

	if len(s.Rows) > 0 {
		b.WriteString("| Check | Result |\n| --- | --- |\n")
		for _, row := range s.Rows {
			fmt.Fprintf(b, "| %s | %s |\n", cell(row.Check), cell(row.Result))
		}
		b.WriteString("\n")
	}
	for _, item := range s.Items {
		fmt.Fprintf(b, "- %s\n", item)
	}
	if len(s.Items) > 0 {
		b.WriteString("\n")
	}
	for _, detail := range s.Details {
		detail.render(b)
	}
	b.WriteString("</details>\n")
}

// render writes one block of captured output.
func (d CommentDetail) render(b *strings.Builder) {
	if d.Title != "" {
		fmt.Fprintf(b, "**%s**\n\n", escapeCell(d.Title))
	}
	if len(d.Lines) > 0 {
		// Fenced with no language, and the fence is what makes this safe to
		// render at all: output quotes source, and source can contain
		// anything source contains — including a line shaped like a heading,
		// a table row, or lydite's own verdict.
		b.WriteString("```\n")
		for _, line := range d.Lines {
			b.WriteString(strings.ReplaceAll(line, "```", "'''") + "\n")
		}
		b.WriteString("```\n")
	}
	if d.Log != "" {
		fmt.Fprintf(b, "\n<sub>full output: <code>%s</code></sub>\n", escapeCell(d.Log))
	}
	b.WriteString("\n")
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
