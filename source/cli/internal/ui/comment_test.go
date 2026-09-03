package ui

import (
	"strings"
	"testing"
)

func TestCommentCarriesTheMarkerSoItCanBeFoundAgain(t *testing.T) {
	body := Comment{Verdict: VerdictRefer, Version: "v0.3.0"}.Render()
	if !strings.Contains(body, Marker) {
		t.Fatal("the comment carries no marker, so the next run cannot find it to edit")
	}
}

// The comment is posted into the pull request of whatever repository is
// being scanned, where a repository-relative path resolves against that
// repository and finds nothing.
func TestCommentReferencesTheMarkByAbsoluteURL(t *testing.T) {
	body := Comment{Verdict: VerdictPass}.Render()
	if !strings.Contains(body, "https://raw.githubusercontent.com/") {
		t.Fatal("the mark is not referenced absolutely")
	}
}

func TestCommentRendersTheVerdictAndTheTable(t *testing.T) {
	body := Comment{
		Verdict:  VerdictRefer,
		Headline: "no exemption matched",
		Rows: []CommentRow{
			{Check: "Changed paths", Head: "12"},
			{Check: "Exemptions", Base: "3 declared"},
		},
		Sections: []CommentSection{{Title: "Uncovered", Items: []string{"`cli/main.go`"}}},
		Version:  "v0.3.0",
		Base:     "4c2eaea",
	}.Render()

	for _, want := range []string{
		"Referred", "no exemption matched",
		"| Check | Head | Base |",
		"| Changed paths | 12 | — |",
		"| Exemptions | — | 3 declared |",
		"**Uncovered**", "- `cli/main.go`",
		"base 4c2eaea",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q\n---\n%s", want, body)
		}
	}
}

// A value here can be a path or a line quoted out of a diff, so it carries
// whatever the source carries. A pipe would end the cell early and shift
// every column after it; a newline would end the table.
func TestCommentCellsCannotBreakTheTable(t *testing.T) {
	body := Comment{
		Verdict: VerdictRefer,
		Rows:    []CommentRow{{Check: "a | b", Head: "one\ntwo"}},
	}.Render()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		// An escaped pipe is content, not a column boundary, so only
		// unescaped ones are counted.
		if n := strings.Count(strings.ReplaceAll(line, "\\|", ""), "|"); n != 4 {
			t.Fatalf("row has %d column boundaries, want 4: %q", n, line)
		}
	}
	if strings.Contains(body, "one\ntwo") {
		t.Error("a newline survived into a table cell")
	}
}

// Nothing can yet establish that a run matches the reader's local one, so
// the footer must not say so.
func TestCommentDoesNotClaimParityItCannotEstablish(t *testing.T) {
	body := Comment{Verdict: VerdictPass, Version: "v0.3.0", Base: "abc1234"}.Render()
	if strings.Contains(body, "same as your local run") {
		t.Fatal("the footer claims parity that nothing verifies")
	}
}

// An empty section would render a heading with nothing under it, which reads
// as data that failed to load.
func TestCommentOmitsAnEmptySection(t *testing.T) {
	body := Comment{Verdict: VerdictPass, Sections: []CommentSection{{Title: "Uncovered"}}}.Render()
	if strings.Contains(body, "Uncovered") {
		t.Fatal("an empty section was rendered")
	}
}

// The mark is rendered at the asset's own size rather than scaled down, and
// carries both dimensions so the comment does not reflow once it loads.
func TestCommentRendersTheMarkAtItsNativeSize(t *testing.T) {
	body := Comment{Verdict: VerdictPass}.Render()
	for _, want := range []string{`width="64"`, `height="64"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the mark is missing %s:\n%s", want, body)
		}
	}
}
