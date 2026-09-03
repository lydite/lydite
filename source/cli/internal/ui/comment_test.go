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

func TestCommentRendersTheVerdictAndEachSection(t *testing.T) {
	body := Comment{
		Verdict:  VerdictFail,
		Headline: "test did not pass",
		Sections: []CommentSection{{
			Status:  StatusFail,
			Title:   "test",
			Summary: "1 failed, 2 passed",
			Rows: []CommentRow{
				{Check: "test(cli)", Result: "passed"},
				{Check: "test(web)", Result: "failed"},
			},
			Details: []CommentDetail{{
				Title: "test(web)",
				Lines: []string{"FAIL src/app.test.ts"},
				Log:   ".lydite-reports/web/test.log",
			}},
		}},
		Version: "v0.3.0",
		Base:    "4c2eaea",
	}.Render()

	for _, want := range []string{
		"Failed", "test did not pass",
		"<b>test</b>", "1 failed, 2 passed",
		"| Check | Result |",
		"| test(cli) | passed |",
		"FAIL src/app.test.ts",
		".lydite-reports/web/test.log",
		"base 4c2eaea",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("comment is missing %q\n---\n%s", want, body)
		}
	}
}

// A reader arriving at a red comment must see what failed without a click,
// while the concerns that passed stay one line each.
func TestAFailingSectionOpensItselfAndAPassingOneDoesNot(t *testing.T) {
	failing := Comment{Sections: []CommentSection{{Status: StatusFail, Title: "test"}}}.Render()
	if !strings.Contains(failing, "<details open>") {
		t.Errorf("a failing section is shut:\n%s", failing)
	}
	passing := Comment{Sections: []CommentSection{{Status: StatusPass, Title: "scan"}}}.Render()
	if strings.Contains(passing, "<details open>") {
		t.Errorf("a passing section opened itself:\n%s", passing)
	}
}

// A referral is what the comment exists to surface to a human, so it opens
// for the same reason a failure does.
func TestAReferredSectionOpensItself(t *testing.T) {
	body := Comment{Sections: []CommentSection{{Status: StatusRefer, Title: "referral"}}}.Render()
	if !strings.Contains(body, "<details open>") {
		t.Fatalf("a referred section is shut:\n%s", body)
	}
}

// A section with nothing under it must still render. Omitting one is
// indistinguishable from a concern that passed, which is exactly how a pull
// request reads green while the gate that would have failed it never ran.
func TestASectionWithNoRowsIsStillRendered(t *testing.T) {
	body := Comment{Sections: []CommentSection{{
		Status: StatusUnmeasured, Title: "scan", Summary: "no report document",
	}}}.Render()
	if !strings.Contains(body, "<b>scan</b>") {
		t.Fatalf("an unmeasured section was dropped:\n%s", body)
	}
	if !strings.Contains(body, "no report document") {
		t.Errorf("the section does not say why it is unmeasured:\n%s", body)
	}
}

// A value here can be a path or a line quoted out of a diff, so it carries
// whatever the source carries. A pipe would end the cell early and shift
// every column after it; a newline would end the table.
func TestCommentCellsCannotBreakTheTable(t *testing.T) {
	body := Comment{
		Verdict: VerdictRefer,
		Sections: []CommentSection{{
			Title: "referral",
			Rows:  []CommentRow{{Check: "a | b", Result: "one\ntwo"}},
		}},
	}.Render()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		// An escaped pipe is content, not a column boundary, so only
		// unescaped ones are counted.
		if n := strings.Count(strings.ReplaceAll(line, "\\|", ""), "|"); n != 3 {
			t.Fatalf("row has %d column boundaries, want 3: %q", n, line)
		}
	}
	if strings.Contains(body, "one\ntwo") {
		t.Error("a newline survived into a table cell")
	}
}

// Captured output quotes source, so it can contain anything source contains
// — including a fence, which would close the block early and let the rest of
// a test failure render as markdown.
func TestCapturedOutputCannotCloseItsOwnFence(t *testing.T) {
	body := Comment{Sections: []CommentSection{{
		Status:  StatusFail,
		Title:   "test",
		Details: []CommentDetail{{Lines: []string{"```", "## not a heading"}}},
	}}}.Render()
	if n := strings.Count(body, "```"); n != 2 {
		t.Fatalf("the block has %d fences, want 2:\n%s", n, body)
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

// The three verdicts read differently on purpose: a referral names no defect
// and must not borrow the failure's mark.
func TestEachVerdictHasItsOwnBadge(t *testing.T) {
	seen := map[string]Verdict{}
	for _, v := range []Verdict{VerdictPass, VerdictFail, VerdictRefer} {
		got := badge(v)
		if prior, ok := seen[got]; ok {
			t.Fatalf("%s and %s share the badge %q", prior, v, got)
		}
		seen[got] = v
	}
}
