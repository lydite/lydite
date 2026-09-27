package publishstages

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/ui"
)

// tailLines is the bound the CLI passes as TailLines: the number of lines its
// log reader cuts a log at.
const tailLines = 40

// noLog is a ReadLog finding no log at all, which is what reading a row's log
// out of a directory that holds none answers.
func noLog(string, string) []string { return nil }

// reportDirWith is one report directory holding one document, the way a run
// leaves it behind and a CI job uploads it.
func reportDirWith(command string, rows ...ui.Row) ReportDir {
	return ReportDir{
		Dir:       "reports-" + command,
		Documents: []ui.Document{{Command: command, Rows: rows}},
	}
}

// buildComment runs BuildComment over gathered with nothing expected, no
// base, and no log to read.
func buildComment(t *testing.T, gathered ...ReportDir) ui.Comment {
	t.Helper()
	out, err := BuildComment(context.Background(), BuildIn{
		Gathered: gathered, ReadLog: noLog, TailLines: tailLines,
	})
	if err != nil {
		t.Fatalf("BuildComment: %v", err)
	}
	return out.Comment
}

// sortedNames is what keeps an undeclared concern's place in the comment
// stable across runs, rather than following Go's randomised map order.
func TestSortedNamesOrdersAlphabetically(t *testing.T) {
	got := sortedNames(map[string]bool{
		"zeta": true, "alpha": true, "mid": true, "kappa": true, "omega": true, "beta": true,
	})
	want := []string{"alpha", "beta", "kappa", "mid", "omega", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sortedNames(...) = %v, want %v", got, want)
	}
}

// A hosting platform refuses a comment over its size limit, and a refused
// comment is no surface at all — which is the outcome this feature exists to
// prevent. Every failing row still appears in the table.
func TestQuotedOutputIsCappedButNoRowIsLost(t *testing.T) {
	doc := ui.Document{Command: "test"}
	for i := range detailCap + 3 {
		doc.Rows = append(doc.Rows, ui.Row{
			Status: ui.StatusFail,
			Label:  fmt.Sprintf("test(c%d)", i),
			Value:  "failed",
			Detail: []string{fmt.Sprintf("component %d blew up", i)},
		})
	}

	comment := buildComment(t, ReportDir{Dir: "reports", Documents: []ui.Document{doc}})
	body := comment.Render()
	if n := len(comment.Sections[0].Details); n != detailCap {
		t.Errorf("%d blocks of output were quoted, want %d", n, detailCap)
	}
	for i := range detailCap + 3 {
		if !strings.Contains(body, fmt.Sprintf("test(c%d)", i)) {
			t.Errorf("row for component %d is missing from the table:\n%s", i, body)
		}
	}
	if !strings.Contains(body, "3 further result(s)") {
		t.Errorf("the comment does not say how many failures it left out:\n%s", body)
	}
}

// StatusDeclined is non-voting, like StatusContext — it must never turn a
// comment's badge into a referral or a failure on its own.
func TestADeclinedSectionDoesNotVoteOnTheVerdict(t *testing.T) {
	dir := reportDirWith("mutation",
		ui.Row{Status: ui.StatusDeclined, Label: "mutation", Value: "declined for this run"},
	)
	comment := buildComment(t, dir)
	if verdictOf(comment.Sections) != ui.VerdictPass {
		t.Fatalf("verdictOf = %q, want pass", verdictOf(comment.Sections))
	}
}

func TestAClaimWithNoLineIsShownAsTheFileAlone(t *testing.T) {
	// A dependency advisory whose manifest line cannot be found carries line
	// zero deliberately: the lookup refuses to guess rather than point at code
	// the author cannot act on. Rendering `go.mod:0` would put that invented
	// reference straight back.
	unlocated := finding.Finding{
		Path: "go.mod", Line: 0, Rule: "GO-2020-0036", Message: "an advisory",
	}
	if got := claimLine(unlocated); strings.Contains(got, ":0") {
		t.Errorf("claimLine = %q, want the file with no line", got)
	} else if !strings.HasPrefix(got, "go.mod GO-2020-0036") {
		t.Errorf("claimLine = %q, want the file, the rule and the message", got)
	}

	located := unlocated
	located.Line = 7
	if got := claimLine(located); !strings.HasPrefix(got, "go.mod:7 ") {
		t.Errorf("claimLine = %q, want the line kept when there is one", got)
	}
}

func TestAFailingRowSClaimsAreBoundedInTheComment(t *testing.T) {
	// Every scanner emits findings, so one row over a repository with standing
	// debt lists hundreds. A comment over the platform's byte limit is refused
	// outright, and a section that vanishes reads as a concern that passed.
	var found []finding.Finding
	for i := range 200 {
		found = append(found, finding.Finding{
			Gate: "gosec", Path: "a.go", Line: i + 1, Rule: "G401",
			Message: strings.Repeat("long ", 400),
			Site:    fmt.Sprintf("site-%d", i),
		})
	}
	lines := detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, found, noLog, tailLines)
	if len(lines) > tailLines+2 {
		t.Errorf("the row rendered %d lines, want it bounded near %d", len(lines), tailLines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "more finding(s) in this row") {
		t.Error("claims were dropped without saying so")
	}
	for _, line := range lines {
		if len([]rune(line)) > claimRunes+200 {
			t.Errorf("a claim ran to %d runes, want each bounded near %d", len([]rune(line)), claimRunes)
		}
	}
}

func TestAFailingRowSaysNothingAboutClaimsItDidNotDrop(t *testing.T) {
	// The overflow line is what tells a reader the quote stops short. A row
	// whose claims all fit must not carry it, or every comment says findings
	// were withheld when none were.
	found := []finding.Finding{
		{Gate: "gosec", Path: "a.go", Line: 1, Rule: "G401", Message: "a claim", Site: "one"},
	}
	lines := detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, found, noLog, tailLines)
	for _, line := range lines {
		if strings.Contains(line, "more finding(s) in this row") {
			t.Errorf("lines = %q, want no overflow line when nothing overflowed", lines)
		}
	}
	// And exactly at the cap it still says nothing.
	var atCap []finding.Finding
	for i := range tailLines {
		atCap = append(atCap, finding.Finding{
			Gate: "gosec", Path: "a.go", Line: i + 1, Rule: "G401",
			Message: "a claim", Site: fmt.Sprintf("site-%d", i),
		})
	}
	lines = detailFor(t.TempDir(), ui.Row{Status: ui.StatusFail, Label: "gosec(cli)"}, atCap, noLog, tailLines)
	if len(lines) != tailLines {
		t.Errorf("rendered %d lines for exactly the cap, want %d with no overflow line", len(lines), tailLines)
	}
}

func TestClipClaimAtExactlyTheCap(t *testing.T) {
	// A message at the cap is kept whole; one rune over is clipped. The
	// boundary is what the cap means.
	at := clipClaim(strings.Repeat("x", claimRunes))
	if len([]rune(at)) != claimRunes || strings.HasSuffix(at, "…") {
		t.Errorf("a message at the cap was clipped: %d runes", len([]rune(at)))
	}
	if over := clipClaim(strings.Repeat("x", claimRunes+1)); !strings.HasSuffix(over, "…") {
		t.Error("a message one rune over the cap was not clipped")
	}
}

// A directory that contributed nothing is named in a section of its own after
// every concern, in the order the directories were named, with the reason it
// was gathered with.
func TestADirectoryThatContributedNothingIsNamedAfterEveryConcern(t *testing.T) {
	comment := buildComment(t,
		ReportDir{Dir: "gone", Missing: "no such directory, so nothing from it is in this comment"},
		reportDirWith("test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"}),
		ReportDir{Dir: "empty", Missing: "holds no report document"},
	)
	if len(comment.Sections) != 2 {
		t.Fatalf("%d sections, want the concern and the missing reports", len(comment.Sections))
	}
	last := comment.Sections[1]
	want := ui.CommentSection{
		Status:  ui.StatusUnmeasured,
		Title:   "missing reports",
		Summary: "2 input(s) produced nothing to read",
		Items: []string{
			"`gone` — no such directory, so nothing from it is in this comment",
			"`empty` — holds no report document",
		},
	}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("missing reports section = %#v, want %#v", last, want)
	}
	if comment.Verdict != ui.VerdictPass {
		t.Errorf("verdict is %q; a missing input is unmeasured and votes on nothing", comment.Verdict)
	}
	if !strings.Contains(comment.Headline, "no verdict came from missing reports") {
		t.Errorf("the headline reads as a clean run: %q", comment.Headline)
	}
}

// A failing row carrying no detail of its own quotes its log, read through
// the ReadLog it was given against the directory its document came from.
func TestAFailingRowReadsItsLogThroughTheGivenReader(t *testing.T) {
	type call struct{ dir, rel string }
	var calls []call
	readLog := func(dir, rel string) []string {
		calls = append(calls, call{dir, rel})
		return []string{"G306: Expect WriteFile permissions to be 0600 or less"}
	}
	out, err := BuildComment(context.Background(), BuildIn{
		Gathered: []ReportDir{reportDirWith("scan", ui.Row{
			Status: ui.StatusFail, Label: "gosec(cli)", Value: "failed",
			Log: ".lydite-reports/scan/gosec-cli.log",
		})},
		ReadLog:   readLog,
		TailLines: tailLines,
	})
	if err != nil {
		t.Fatalf("BuildComment: %v", err)
	}
	if want := []call{{"reports-scan", ".lydite-reports/scan/gosec-cli.log"}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("ReadLog calls = %v, want %v", calls, want)
	}
	if body := out.Comment.Render(); !strings.Contains(body, "G306") {
		t.Fatalf("the log's tail did not reach the comment:\n%s", body)
	}
}

// The footer carries lydite's version and the base as a short commit, and the
// comment is the standing one a later run edits in place.
func TestTheFooterCarriesTheVersionAndAShortBase(t *testing.T) {
	out, err := BuildComment(context.Background(), BuildIn{
		Gathered: []ReportDir{reportDirWith("test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})},
		Base:     "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091",
		Version:  "v1.2.3",
		ReadLog:  noLog, TailLines: tailLines,
	})
	if err != nil {
		t.Fatalf("BuildComment: %v", err)
	}
	c := out.Comment
	if !c.Standing || c.Version != "v1.2.3" || c.Base != "4c2eaea1f2b3" {
		t.Errorf("comment = standing %t, version %q, base %q; want standing, v1.2.3, 4c2eaea1f2b3",
			c.Standing, c.Version, c.Base)
	}
}

// An expected concern that arrived in no directory is a section in its
// declared place, and a blank expectation names nothing.
func TestAnExpectedConcernFromNoDirectoryIsItsOwnSection(t *testing.T) {
	out, err := BuildComment(context.Background(), BuildIn{
		Gathered: []ReportDir{reportDirWith("test", ui.Row{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"})},
		Expect:   []string{"test", "review", " ", "coverage"},
		ReadLog:  noLog, TailLines: tailLines,
	})
	if err != nil {
		t.Fatalf("BuildComment: %v", err)
	}
	var titles []string
	for _, s := range out.Comment.Sections {
		titles = append(titles, s.Title)
	}
	if want := []string{"referral", "test", "coverage"}; !reflect.DeepEqual(titles, want) {
		t.Fatalf("sections are %v, want %v", titles, want)
	}
	if got := out.Comment.Sections[0]; got.Status != ui.StatusUnmeasured ||
		!reflect.DeepEqual(got.Items, []string{"the run expected a `review` report and none of its inputs holds one"}) {
		t.Errorf("the absent concern's section is %#v", got)
	}
}
