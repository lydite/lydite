package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Verdict is a whole run's answer, and the only thing that decides the exit
// code.
type Verdict string

const (
	// VerdictPass is exit 0.
	VerdictPass Verdict = "pass"
	// VerdictFail is exit 1: a gate the author clears by doing more work.
	VerdictFail Verdict = "fail"
	// VerdictRefer is exit 2: not "no", but "not yet, and not by you".
	// Distinct from VerdictFail because no edit to the branch satisfies it —
	// it is resolved by a human, outside the repository.
	VerdictRefer Verdict = "refer"
)

// ExitError asks main to exit with Code without printing anything further.
// A run that reached a verdict has already said so on its last line; letting
// the error surface as "lydite: ..." on top of that would report a referral
// as a malfunction.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// Report accumulates one run's rows and renders them, in either grammar.
type Report struct {
	command string
	rows    []Row
	started time.Time
}

// NewReport starts a report, and the clock — the verdict line carries the
// run's duration, so timing begins when the report does rather than being
// stitched on at the end.
func NewReport(command string) *Report {
	return &Report{command: command, started: time.Now()}
}

// Add appends a row.
func (r *Report) Add(row Row) { r.rows = append(r.rows, row) }

// Rows returns what has been added so far.
func (r *Report) Rows() []Row { return r.rows }

// Verdict is the worst state any row reached. A failure outranks a referral
// because it is actionable by the author: telling someone to fetch a human
// for a change that does not yet build wastes the human.
func (r *Report) Verdict() Verdict {
	verdict := VerdictPass
	for _, row := range r.rows {
		switch row.Status {
		case StatusFail:
			return VerdictFail
		case StatusRefer:
			verdict = VerdictRefer
		}
	}
	return verdict
}

// ExitCode maps the verdict onto a process exit code. StatusUnmeasured and
// StatusDropped reach this function and change nothing, which is the point:
// they are loud in the terminal and silent in CI, because a path-filtered
// coverage job is not a reason to fail someone's build.
func (r *Report) ExitCode() int {
	switch r.Verdict() {
	case VerdictFail:
		return 1
	case VerdictRefer:
		return 2
	default:
		return 0
	}
}

// Err returns the ExitError for a non-zero verdict, or nil.
func (r *Report) Err() error {
	if code := r.ExitCode(); code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

var verdictWord = map[Verdict]string{
	VerdictPass:  "passed",
	VerdictFail:  "failed",
	VerdictRefer: "referred",
}

// WriteText renders the human grammar: every row, a blank line, then the
// verdict and how long it took.
func (r *Report) WriteText(w io.Writer, color bool) error {
	pal := palette{enabled: color}
	for _, row := range r.rows {
		for _, line := range row.render(pal) {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	verdict := r.Verdict()
	status := StatusPass
	switch verdict {
	case VerdictFail:
		status = StatusFail
	case VerdictRefer:
		status = StatusRefer
	}
	_, err := fmt.Fprintf(w, "\n%s\n", pal.paint(status,
		fmt.Sprintf("%s %s in %.1fs", r.command, verdictWord[verdict], time.Since(r.started).Seconds())))
	return err
}

// jsonRow is the wire shape of a row. Statuses travel as their own names
// rather than as glyphs: a consumer that has to map "!" back onto three
// different meanings is doing the work this document exists to save it.
//
// It is a separate type from Row even though the two currently agree field
// for field, because they answer to different pressures — Row follows what
// the terminal needs, and this follows what a consumer has been promised.
// TestJSONKeysArePartOfTheContract pins the keys, so a field added to Row for
// rendering reasons cannot quietly become part of the published document.
type jsonRow struct {
	Status Status   `json:"status"`
	Label  string   `json:"label"`
	Value  string   `json:"value,omitempty"`
	Detail []string `json:"detail,omitempty"`
	// Log is the path to everything this row's work printed. A consumer
	// linking it is the whole reason it travels here and not only in the
	// prose a human reads.
	Log string `json:"log,omitempty"`
}

type jsonReport struct {
	Command    string    `json:"command"`
	Verdict    Verdict   `json:"verdict"`
	Exit       int       `json:"exit"`
	DurationMS int64     `json:"duration_ms"`
	Rows       []jsonRow `json:"rows"`
}

// WriteJSON renders the machine grammar. It carries the same rows as the
// text, so the two can never disagree about what happened, and it is what
// anything automated reads: screen-scraping the human surface couples a
// consumer to a rendering that is free to change, which costs that consumer a
// synchronised release for every refinement to the terminal output.
func (r *Report) WriteJSON(w io.Writer) error {
	rows := make([]jsonRow, 0, len(r.rows))
	for _, row := range r.rows {
		rows = append(rows, jsonRow(row))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jsonReport{
		Command:    r.command,
		Verdict:    r.Verdict(),
		Exit:       r.ExitCode(),
		DurationMS: time.Since(r.started).Milliseconds(),
		Rows:       rows,
	})
}

// Write picks a grammar and renders.
func (r *Report) Write(w io.Writer, asJSON, color bool) error {
	if asJSON {
		return r.WriteJSON(w)
	}
	return r.WriteText(w, color)
}
