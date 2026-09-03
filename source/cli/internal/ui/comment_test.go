package ui

import (
	"strings"
	"testing"
)

func TestTheStandingCommentCarriesTheMarkerSoItCanBeFoundAgain(t *testing.T) {
	body := Comment{Standing: true, Verdict: VerdictRefer, Version: "v0.3.0"}.Render()
	if !strings.Contains(body, Marker) {
		t.Fatal("the comment carries no marker, so the next run cannot find it to edit")
	}
}

// A reply answers a question somebody asked and is a new comment, not the
// standing verdict. One carrying the marker would be found by a later run's
// marker search — which takes the first body that matches — and edited in
// place of the verdict it is answering about.
func TestAReplyDoesNotCarryTheStandingMarker(t *testing.T) {
	body := Comment{Verdict: VerdictPass, Headline: "cleared by @someone"}.Render()
	if strings.Contains(body, Marker) {
		t.Fatalf("a reply carries the standing marker, so a later run would edit it:\n%s", body)
	}
}

// The App is the identity now, so a mark above the verdict restates the
// byline and spends a row doing it.
func TestCommentCarriesNoLogo(t *testing.T) {
	body := Comment{Verdict: VerdictPass}.Render()
	for _, unwanted := range []string{"<img", "raw.githubusercontent.com"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the comment still carries a logo (%s):\n%s", unwanted, body)
		}
	}
}

// A reader who takes only the first line must still have taken the answer.
func TestTheStatusIsTheFirstThingRendered(t *testing.T) {
	for verdict, want := range map[Verdict]string{
		VerdictPass:  "Success",
		VerdictFail:  "Failure",
		VerdictRefer: "Needs attention",
	} {
		body := Comment{Verdict: verdict, Headline: "something happened"}.Render()
		lines := strings.Split(strings.TrimPrefix(body, Marker+"\n"), "\n")
		if !strings.HasPrefix(lines[0], "### ") {
			t.Errorf("%s: the first line is not a heading: %q", verdict, lines[0])
		}
		if !strings.Contains(lines[0], want) {
			t.Errorf("%s: the heading does not say %q: %q", verdict, want, lines[0])
		}
	}
}

// The explanation sits under the status, not beside it: one line saying what
// the heading means for this change.
func TestTheHeadlineFollowsTheStatusOnItsOwnLine(t *testing.T) {
	body := Comment{Verdict: VerdictRefer, Headline: "referral needs a human"}.Render()
	if !strings.Contains(body, "Needs attention\n\nreferral needs a human\n") {
		t.Fatalf("the headline is not on its own line under the status:\n%s", body)
	}
}

// A referral names no defect and is resolved by a person, so it must not
// borrow the failure's colour. Unmeasured must not borrow the referral's
// either: "nobody looked" is not "somebody must".
func TestEveryStatusThatReadsDifferentlyLooksDifferent(t *testing.T) {
	seen := map[string]Status{}
	for _, s := range []Status{StatusPass, StatusFail, StatusRefer, StatusUnmeasured, StatusContext} {
		got := mark(s)
		if prior, ok := seen[got]; ok && prior != s {
			t.Errorf("%s and %s share the mark %q", prior, s, got)
		}
		seen[got] = s
	}
	if mark(StatusRefer) == mark(StatusFail) {
		t.Error("a referral borrows the failure's mark")
	}
	if mark(StatusUnmeasured) == mark(StatusRefer) {
		t.Error("an unmeasured gate borrows the referral's mark")
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
		"### ", "Failure", "test did not pass",
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
		if !strings.HasPrefix(line, "|") {
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

// The three verdicts read differently on purpose.
func TestEachVerdictHasItsOwnTitle(t *testing.T) {
	seen := map[string]Verdict{}
	for _, v := range []Verdict{VerdictPass, VerdictFail, VerdictRefer} {
		got := title(v)
		if prior, ok := seen[got]; ok {
			t.Fatalf("%s and %s share the title %q", prior, v, got)
		}
		seen[got] = v
	}
}

// A table has no colour of its own, so a reader scanning an open section needs
// the row that wants acting on to find them.
func TestARowThatWantsAttentionIsMarkedAndEmphasised(t *testing.T) {
	body := Comment{Sections: []CommentSection{{
		Status: StatusFail,
		Title:  "test",
		Rows: []CommentRow{
			{Status: StatusPass, Check: "test(cli)", Result: "passed"},
			{Status: StatusFail, Check: "test(web)", Result: "failed"},
		},
	}}}.Render()

	if !strings.Contains(body, "| "+mark(StatusFail)+" | test(web) | **failed** |") {
		t.Errorf("the failing row is neither marked nor emphasised:\n%s", body)
	}
	// Emphasising every row emphasises none.
	if !strings.Contains(body, "| "+mark(StatusPass)+" | test(cli) | passed |") {
		t.Errorf("the passing row was emphasised too:\n%s", body)
	}
}
