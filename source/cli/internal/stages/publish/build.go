package publishstages

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/ui"
)

// ReadLog reads the last lines of a row's log, resolved against the report
// directory the row's document was read from. A log that cannot be read
// answers nil.
type ReadLog func(dir, rel string) []string

// concerns is the order sections appear in, and what each command is called
// where a reader sees it.
//
// Declared rather than derived from what the run happens to have produced, so
// two pull requests never present the same four concerns in a different order.
// A document naming a command that is not here still renders — under its own
// name, after these — because dropping it would hide a result.
var concerns = []struct {
	command string
	title   string
}{
	{"review", "referral"},
	{"scan", "scan"},
	{"test", "test"},
	{"mutation", "mutation"},
}

// BuildIn is every directory GatherReports read, and what the comment is
// rendered with.
type BuildIn struct {
	Gathered []ReportDir
	// Expect names the commands the run was supposed to produce a report
	// for.
	Expect []string
	// Base is the commit the change was measured against, for the footer.
	Base string
	// Version is lydite's own version, for the footer.
	Version string
	// ReadLog reads a failing row's log when the row carries no detail of
	// its own.
	ReadLog ReadLog
	// TailLines bounds how many unanchored claims one row quotes. The CLI
	// fills it from the same number its own ReadLog cuts a log at, so the
	// two bounds agree without either one deriving from the other.
	TailLines int
}

// BuildOut is the comment.
type BuildOut struct {
	Comment ui.Comment
}

// BuildComment folds every gathered directory into one comment.
//
// A directory that contributed nothing is rendered in a missing-reports
// section naming it and why, after every concern — never omitted.
//
// Naming a directory is not enough to catch a concern that never ran on its
// own. A job that dies before it uploads leaves no artifact, so the directory
// it would have been downloaded into never exists and the caller's glob never
// names it — the concern reaches this stage as nothing at all rather than as
// something that could not be read. Expect is what closes that: a command
// named there and found in no directory renders as unmeasured in its declared
// place, exactly as an unreadable directory does. A command not named there is
// considered only if a document for it arrives, which is what lets a run that
// legitimately skips a concern say nothing about it.
func BuildComment(_ context.Context, in BuildIn) (BuildOut, error) {
	found := map[string][]section{}
	var missing []string
	for _, g := range in.Gathered {
		if len(g.Documents) == 0 {
			missing = append(missing, fmt.Sprintf("`%s` — %s", g.Dir, g.Missing))
			continue
		}
		for _, doc := range g.Documents {
			found[doc.Command] = append(found[doc.Command], section{dir: g.Dir, doc: doc})
		}
	}

	expected := map[string]bool{}
	for _, command := range in.Expect {
		if command = strings.TrimSpace(command); command != "" {
			expected[command] = true
		}
	}

	comment := ui.Comment{Standing: true, Version: in.Version, Base: shortSHA(in.Base)}
	for _, concern := range concerns {
		for _, s := range found[concern.command] {
			comment.Sections = append(comment.Sections, s.render(concern.title, in.ReadLog, in.TailLines))
		}
		if len(found[concern.command]) == 0 && expected[concern.command] {
			comment.Sections = append(comment.Sections, absentConcern(concern.command, concern.title))
		}
		delete(found, concern.command)
		delete(expected, concern.command)
	}
	for _, command := range sortedKeys(found) {
		for _, s := range found[command] {
			comment.Sections = append(comment.Sections, s.render(command, in.ReadLog, in.TailLines))
		}
		delete(expected, command)
	}
	// A command expected under a name this binary does not declare renders
	// after the declared concerns, under its own name — the rule a document
	// naming an undeclared command already follows.
	for _, command := range sortedNames(expected) {
		comment.Sections = append(comment.Sections, absentConcern(command, command))
	}
	if len(missing) > 0 {
		comment.Sections = append(comment.Sections, ui.CommentSection{
			Status:  ui.StatusUnmeasured,
			Title:   "missing reports",
			Summary: fmt.Sprintf("%d input(s) produced nothing to read", len(missing)),
			Items:   missing,
		})
	}
	comment.Verdict = verdictOf(comment.Sections)
	comment.Headline = headline(comment.Sections, comment.Verdict)
	return BuildOut{Comment: comment}, nil
}

// absentConcern is the section a concern gets when the run expected it and no
// directory the comment was rendered from holds its document.
//
// Unmeasured, and titled as the concern itself rather than folded into the
// missing-reports section, because what a reader has to be told is which
// concern reached no verdict — the directory it would have been in is the
// caller's bookkeeping and may never have existed to be named.
func absentConcern(command, title string) ui.CommentSection {
	return ui.CommentSection{
		Status:  ui.StatusUnmeasured,
		Title:   title,
		Summary: "nothing reported",
		Items: []string{fmt.Sprintf(
			"the run expected a `%s` report and none of its inputs holds one", command)},
	}
}

// section is one document, and where it was read from — which is what a row's
// log has to be resolved against.
type section struct {
	dir string
	doc ui.Document
}

// render turns one document into one collapsible section.
//
// The mapping is deliberately generic: a row's label is what was checked and
// its value is what that answered, whichever command produced it. So a
// referral's rows, a scan's and a suite's all render through one path, and a
// command lydite grows later needs nothing here. It is also why `review` has
// no comment-rendering code of its own.
func (s section) render(title string, readLog ReadLog, tailLines int) ui.CommentSection {
	out := ui.CommentSection{Status: worst(s.doc.Rows), Title: title, Summary: counts(s.doc.Rows)}
	byRow := map[string][]finding.Finding{}
	for _, f := range s.doc.Findings {
		byRow[f.Row] = append(byRow[f.Row], f)
	}
	for _, row := range s.doc.Rows {
		out.Rows = append(out.Rows, ui.CommentRow{Status: row.Status, Check: row.Label, Result: row.Value})
	}
	// Only a row the reader has to act on. A clean run has a log per check
	// and pasting all of them would bury the verdict under the thing that
	// went right; the row still names its log, and the artifact still holds
	// it.
	//
	// A referral is one of those rows, and the one whose detail is least
	// replaceable: its value says what the verdict is and its detail says
	// what about this change produced it — which paths no exemption covers,
	// or which disqualifier fired, and what the reader can do about it. A
	// section carrying `referral … no exemptions declared` and nothing else
	// reads as a misconfiguration rather than as the answer it is. The
	// terminal says this too; the comment is where the person whose change
	// it is actually reads it.
	var quoted int
	for _, row := range s.doc.Rows {
		if row.Status != ui.StatusFail && row.Status != ui.StatusRefer {
			continue
		}
		quoted++
		if quoted > detailCap {
			continue
		}
		out.Details = append(out.Details, ui.CommentDetail{
			Title: row.Label,
			Lines: detailFor(s.dir, row, byRow[row.Label], readLog, tailLines),
			Log:   row.Log,
		})
	}
	if rest := quoted - detailCap; rest > 0 {
		out.Items = append(out.Items,
			fmt.Sprintf("%d further result(s) are in the run's artifact rather than here", rest))
	}
	return out
}

// detailFor is what a failing row says in the comment, once the claims that
// have a line of their own have been taken out of it.
//
// A finding that reaches the change is a thread on the line it is about, and
// repeating it here would be the same claim in two places — one of which a
// reader cannot resolve and neither of which says which is the real one. So
// the comment keeps the claims that reach the change nowhere, and counts the
// rest rather than listing them: a reader has to be told the section is not
// the whole story, or a failing row with every claim on a line reads as a row
// with nothing under it.
//
// The narrowing is unconditional. It does not ask whether threads are being
// posted, because a developer running `lydite publish` locally has to read
// exactly the comment a reviewer sees — the parity property ADR 0023 exists
// for, and one that a comment rendered differently depending on a hosting
// platform would lose.
//
// A row with no findings at all quotes its own detail, or its log. It is what
// every gate that emits none still has, and what the row's own author chose
// to put next to the verdict.
//
// The cost is that a row's asides — "3 did not compile" — leave the comment
// with the rest of the Detail, because a row renders one or the other. They
// are still on the terminal and in the log the row names.
func detailFor(dir string, row ui.Row, found []finding.Finding, readLog ReadLog, tailLines int) []string {
	if len(found) == 0 {
		return failureLines(dir, row, readLog)
	}
	var lines []string
	var located, over int
	for _, f := range found {
		if f.Anchor != finding.AnchorNowhere {
			located++
			continue
		}
		if len(lines) == tailLines {
			over++
			continue
		}
		lines = append(lines, claimLine(f))
	}
	// Bounded the way a quoted log already is. Every scanner emits findings,
	// so one row over a repository with standing debt lists hundreds; a
	// comment over the platform's 65,536-byte limit is refused outright, and a
	// section that vanishes reads as a concern that passed.
	if over > 0 {
		lines = append(lines, fmt.Sprintf("%d more finding(s) in this row. The run's log holds all of them.", over))
	}
	if located > 0 {
		lines = append(lines, fmt.Sprintf(
			"%d finding(s) reach a line of this change, and are threads on those lines rather than rows here.", located))
	}
	return lines
}

// claimLine is one unanchored claim, as the comment shows it.
//
// The rule is carried when the gate has one. It is what a reader acts on for
// a scanner's finding — which rule fired, not only that something did — and
// no other surface carries it: a scanner's claims reach the change nowhere,
// so they are always here rather than on a line.
//
// A claim with no line is shown as the file alone. A dependency advisory whose
// manifest line cannot be found carries line zero deliberately — the lookup
// refuses to guess rather than pointing at code the author cannot act on — and
// `go.mod:0` would put back exactly the invented reference that refusal
// avoids.
func claimLine(f finding.Finding) string {
	at := f.Path
	if f.Line > 0 {
		at = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	if f.Rule == "" {
		return at + " " + clipClaim(f.Message)
	}
	return at + " " + f.Rule + " " + clipClaim(f.Message)
}

// claimRunes bounds one claim's message.
//
// A tool's diagnostic is a scanned repository's own text and nothing bounds it
// at that end — semgrep's messages run to paragraphs. It is the rule
// threads.claimText follows for the same content on the other surface, and the
// reason is the same: a comment a platform refuses is no surface at all.
const claimRunes = 300

// clipClaim bounds a message, stated as a clamp so a message at exactly the
// cap and one under it take the same path.
func clipClaim(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > claimRunes {
		return string(runes[:claimRunes]) + "…"
	}
	return s
}

// detailCap is how many rows get their output quoted in one section.
//
// A hosting platform refuses a comment over a size limit — GitHub's is 65536
// bytes — and a refused comment is no surface at all, which is the outcome
// this whole feature exists to prevent. Forty lines each is generous for the
// handful of failures a change usually has and ruinous for a repository where
// twenty components fail at once, so the tail is capped and the remainder is
// counted. Every failing row is still in the table, and its log is still in
// the artifact.
const detailCap = 5

// failureLines is what a failing row shows: the detail it already carries, or the
// tail of its log when it carries none.
//
// Detail first, because it is the reason the row's author chose to put next to
// the verdict — Biome's findings reach a reader no other way, and clippy,
// cargo-audit and cargo-deny render the same claim-plus-Finding.Detail prose so
// their row never has to quote the raw JSON their one run produces. The log
// is the fallback for every check that only streams its findings, where the row
// holds a status and the output holds the reason.
func failureLines(dir string, row ui.Row, readLog ReadLog) []string {
	if len(row.Detail) > 0 {
		return row.Detail
	}
	if row.Log == "" {
		return nil
	}
	return readLog(dir, row.Log)
}

// worst is the section's status: the state the reader has to act on.
//
// A failure outranks a referral, which is the report's own precedence asked of
// one concern, so a section's mark and the run's verdict cannot disagree about
// which concern is the problem.
//
// Unmeasured is the section's status only when nothing in it was decided at
// all — no row passed, failed, referred or was declined. It is deliberately
// not promoted by a single unmeasured row among decided ones, because that
// state is ordinary and expected: `--affected` reports every component it did
// not select as unmeasured, and `review` reports a dirty working tree the
// same way. A rule that promoted on any of them would mark a normal run as
// ungated and put "reported nothing" in the headline of a run that measured
// everything it was asked to.
//
// What that rule must not cost is a concern that went ungated reading as one
// that passed. It does not: a partly measured section says so in the counts on
// its own summary line, which is visible without opening it, and a concern
// whose report never arrived has no decided row at all and so lands here as
// unmeasured.
//
// StatusDeclined is promoted the same way StatusPass is — out of
// StatusUnmeasured and no further — because a section made entirely of
// declined rows is a decision stated on purpose, not a gap. A real
// StatusFail or StatusRefer elsewhere in the same section still outranks it,
// exactly as either outranks StatusPass.
func worst(rows []ui.Row) ui.Status {
	status := ui.StatusUnmeasured
	for _, row := range rows {
		switch row.Status {
		case ui.StatusFail:
			return ui.StatusFail
		case ui.StatusRefer:
			status = ui.StatusRefer
		case ui.StatusPass:
			if status == ui.StatusUnmeasured {
				status = ui.StatusPass
			}
		case ui.StatusDeclined:
			if status == ui.StatusUnmeasured {
				status = ui.StatusDeclined
			}
		}
	}
	return status
}

// counts is the one line visible while a section is shut.
//
// Every state is named, including the ones that vote on nothing. Omitting those
// looks reasonable — a reader scanning shut sections is deciding which to open
// — and it hides the case the distinction exists for: a run that measured
// coverage without gating it renders every coverage row as context, so a
// summary that counted only the voting rows would describe that run and a
// fully gated one identically. Naming them costs three words and keeps the
// numbers adding up to the rows behind them.
func counts(rows []ui.Row) string {
	tally := map[ui.Status]int{}
	for _, row := range rows {
		tally[row.Status]++
	}
	var parts []string
	for _, s := range []struct {
		status ui.Status
		word   string
	}{
		{ui.StatusFail, "failed"},
		{ui.StatusRefer, "referred"},
		{ui.StatusDeclined, "declined"},
		{ui.StatusUnmeasured, "unmeasured"},
		{ui.StatusPass, "passed"},
		{ui.StatusNew, "new"},
		{ui.StatusContext, "not gated"},
		{ui.StatusDropped, "dropped"},
	} {
		if tally[s.status] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", tally[s.status], s.word))
		}
	}
	if len(parts) == 0 {
		return "nothing reported"
	}
	return strings.Join(parts, ", ")
}

// verdictOf is the comment's badge, from the sections rather than from any one
// document.
//
// It repeats the report verdict's precedence over a different collection
// because that is the same question asked of a whole run: a comment covering a
// failed suite and a passing scan says failed.
func verdictOf(sections []ui.CommentSection) ui.Verdict {
	verdict := ui.VerdictPass
	for _, s := range sections {
		switch s.Status {
		case ui.StatusFail:
			return ui.VerdictFail
		case ui.StatusRefer:
			verdict = ui.VerdictRefer
		}
	}
	return verdict
}

// headline is the one sentence under the badge.
//
// It names the concern the reader has to act on rather than restating the
// badge, which is directly above it and already says the word. An unmeasured
// section is called out even when nothing failed: a run that gated less than
// it was asked to must not read as a clean one.
//
// A declined section is not collected here at all. "No verdict came from
// mutation" is the wardnet#957 sentence for a report that never arrived; a
// concern the repository chose not to run has nothing missing to report, so
// it falls through to "every check passed" alongside the sections that
// genuinely did.
func headline(sections []ui.CommentSection, verdict ui.Verdict) string {
	var failed, referred, unmeasured []string
	for _, s := range sections {
		switch s.Status {
		case ui.StatusFail:
			failed = append(failed, s.Title)
		case ui.StatusRefer:
			referred = append(referred, s.Title)
		case ui.StatusUnmeasured:
			unmeasured = append(unmeasured, s.Title)
		}
	}
	switch {
	case len(failed) > 0:
		return strings.Join(failed, " and ") + " did not pass"
	case len(referred) > 0:
		return strings.Join(referred, " and ") + " needs a human — comment `/lydite clear` to resolve"
	case len(unmeasured) > 0 && verdict == ui.VerdictPass:
		return "everything that ran passed, but no verdict came from " + strings.Join(unmeasured, " or ")
	default:
		return "every check passed"
	}
}

func sortedNames(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]section) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// shortSHA is a commit as the footer names it: its first twelve characters, or
// the whole of one no longer than that.
//
// Stated as a clamp so a SHA of exactly twelve characters and one under take
// the same path, leaving no boundary a test could not observe.
func shortSHA(sha string) string {
	return sha[:min(len(sha), 12)]
}
