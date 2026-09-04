package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

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
}

// newPublishCmd renders the standing pull-request comment from the documents
// one or more runs wrote.
//
// It is pure: no network, no token, and nothing about a hosting platform. What
// it emits is markdown on stdout or in a file, and posting that is a separate
// step with a separate identity. Two things follow, and both are the point. A
// developer can run it locally and read exactly what a reviewer would see. And
// refining the comment never needs a release of whatever posts it, which is
// the coupling that made every change to the human surface a two-repository
// release the last time.
func newPublishCmd() *cobra.Command {
	var (
		reports []string
		out     string
		base    string
	)
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Render the standing pull-request comment from one or more report directories",
		Long: "Render the standing pull-request comment from the report documents lydite wrote.\n\n" +
			"Each --reports directory is a " + runner.ReportDir + " directory: one per job that ran a\n" +
			"lydite command, or one for every command when they shared a scan root. Nothing is\n" +
			"posted — the markdown goes to --out, and posting it is a separate step.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			comment := buildComment(reports, base)
			return writeComment(cmd.OutOrStdout(), out, comment.Render())
		},
	}
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory to read; repeatable")
	cmd.Flags().StringVar(&out, "out", "-", `file to write the comment to ("-" is stdout)`)
	cmd.Flags().StringVar(&base, "base", "", "commit the change was measured against, for the footer")
	return cmd
}

// buildComment folds every named directory into one comment.
//
// A directory that is absent, unreadable, or holds no document is rendered as
// an unmeasured section naming what was missing — never omitted. A section
// that quietly disappears is indistinguishable from a concern that passed,
// which is the wardnet#957 failure: a pull request read green while the gate
// that would have failed it had never run.
func buildComment(dirs []string, base string) ui.Comment {
	found := map[string][]section{}
	var missing []string
	for _, dir := range dirs {
		docs, err := readDocuments(dir)
		if err != nil {
			missing = append(missing, fmt.Sprintf("`%s` — %s", dir, reason(err)))
			continue
		}
		if len(docs) == 0 {
			missing = append(missing, fmt.Sprintf("`%s` — holds no report document", dir))
			continue
		}
		for _, doc := range docs {
			found[doc.Command] = append(found[doc.Command], section{dir: dir, doc: doc})
		}
	}

	comment := ui.Comment{Standing: true, Version: version, Base: shortSHA(base)}
	for _, concern := range concerns {
		for _, s := range found[concern.command] {
			comment.Sections = append(comment.Sections, s.render(concern.title))
		}
		delete(found, concern.command)
	}
	for _, command := range sortedKeys(found) {
		for _, s := range found[command] {
			comment.Sections = append(comment.Sections, s.render(command))
		}
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
	return comment
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
// no comment-rendering code of its own any more.
func (s section) render(title string) ui.CommentSection {
	out := ui.CommentSection{Status: worst(s.doc.Rows), Title: title, Summary: counts(s.doc.Rows)}
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
	// terminal has said this all along; the comment is where the person whose
	// change it is actually reads it.
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
			Lines: failureLines(s.dir, row),
			Log:   row.Log,
		})
	}
	if rest := quoted - detailCap; rest > 0 {
		out.Items = append(out.Items,
			fmt.Sprintf("%d further result(s) are in the run's artifact rather than here", rest))
	}
	return out
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
// the verdict — Biome's findings reach a reader no other way. The log is the
// fallback for every check that streamed its findings instead, where the row
// holds a status and the output holds the reason.
func failureLines(dir string, row ui.Row) []string {
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
// all — no row passed, failed or referred. It is deliberately not promoted by
// a single unmeasured row among decided ones, because that state is ordinary
// and expected: `--affected` reports every component it did not select as
// unmeasured, and `review` reports a dirty working tree the same way. A rule
// that promoted on any of them would mark a normal run as ungated and put
// "reported nothing" in the headline of a run that measured everything it was
// asked to.
//
// What that rule must not cost is a concern that went ungated reading as one
// that passed. It does not: a partly measured section says so in the counts on
// its own summary line, which is visible without opening it, and a concern
// whose report never arrived has no decided row at all and so lands here as
// unmeasured.
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
		}
	}
	return status
}

// counts is the one line visible while a section is shut.
//
// Every state is named, including the ones that vote on nothing. Omitting those
// looked reasonable — a reader scanning shut sections is deciding which to open
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
// It repeats ui.Report.Verdict's precedence over a different collection
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

// reason turns a directory that could not be read into something a reader can
// act on, rather than a Go error string with a path repeated in it.
func reason(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "no such directory, so nothing from it is in this comment"
	}
	return err.Error()
}

func sortedKeys(m map[string][]section) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// writeComment puts the comment where the caller asked for it.
func writeComment(stdout io.Writer, out, body string) error {
	if out == "-" {
		_, err := io.WriteString(stdout, body)
		return err
	}
	if dir := filepath.Dir(out); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(out, []byte(body), 0o600)
}
