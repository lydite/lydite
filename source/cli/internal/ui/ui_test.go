package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The grammar in docs/design/tokens.md puts every value at the same column so
// two rows, and two runs, can be compared by eye.
func TestLeaderDotsAlignTheValueColumn(t *testing.T) {
	for _, label := range []string{"go", "typescript patch", "rust floor: crates/daemon"} {
		var buf bytes.Buffer
		rep := NewReport("coverage")
		rep.Add(Row{Status: StatusPass, Label: label, Value: "86.1%"})
		if err := rep.WriteText(&buf, false); err != nil {
			t.Fatalf("WriteText: %v", err)
		}
		line := strings.SplitN(buf.String(), "\n", 2)[0]
		idx := strings.Index(line, "86.1%")
		if idx < 0 {
			t.Fatalf("value missing from %q", line)
		}
		if got := utf8.RuneCountInString(line[:idx]); got != valueColumn {
			t.Errorf("label %q put the value at column %d, want %d: %q", label, got, valueColumn, line)
		}
	}
}

// A label long enough to reach the value column leaves no room for a leader
// that reads as one. Emitting a lone dot there would look like a typo, so the
// row falls back to a plain separator rather than mangling the alignment it
// can no longer keep.
func TestOverlongLabelFallsBackToASingleSpace(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("scan")
	long := strings.Repeat("x", valueColumn)
	rep.Add(Row{Status: StatusPass, Label: long, Value: "passed"})
	if err := rep.WriteText(&buf, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if want := "✓ " + long + " passed"; !strings.Contains(buf.String(), want) {
		t.Errorf("got %q, want it to contain %q", buf.String(), want)
	}
}

// Colour is the only part of the grammar that is ever dropped. A reader
// piping lydite into a pager still has to be able to tell a pass from a
// referral, so the glyphs survive.
func TestNoColorKeepsEveryGlyph(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("review")
	rep.Add(Row{Status: StatusPass, Label: "a", Value: "v"})
	rep.Add(Row{Status: StatusRefer, Label: "b", Value: "v"})
	rep.Add(Row{Status: StatusFail, Label: "c", Value: "v"})
	rep.Add(Row{Status: StatusNew, Label: "d", Value: "v"})
	if err := rep.WriteText(&buf, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("--no-color must emit no escape sequences, got %q", out)
	}
	for _, glyph := range []string{"✓", "!", "✗", "→"} {
		if !strings.Contains(out, glyph) {
			t.Errorf("glyph %q dropped along with the colour:\n%s", glyph, out)
		}
	}
}

// The exit code comes from the verdict, never from a row's glyph. Three
// statuses share the "!" glyph and only one of them votes — an unmeasured
// gate is loud in the terminal and silent in CI, because a path-filtered
// coverage job is not a reason to fail somebody's build.
func TestExitCodeComesFromTheVerdictNotTheGlyph(t *testing.T) {
	cases := []struct {
		name string
		rows []Status
		want int
	}{
		{"all passing", []Status{StatusPass, StatusPass}, 0},
		{"unmeasured does not vote", []Status{StatusPass, StatusUnmeasured}, 0},
		{"dropped does not vote", []Status{StatusPass, StatusDropped}, 0},
		{"new does not vote", []Status{StatusPass, StatusNew}, 0},
		{"a referral is exit 2", []Status{StatusPass, StatusRefer}, 2},
		{"a failure is exit 1", []Status{StatusPass, StatusFail}, 1},
		// A failure outranks a referral because it is actionable by the
		// author: fetching a human for a change that does not build yet
		// wastes the human.
		{"a failure outranks a referral", []Status{StatusRefer, StatusFail}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := NewReport("review")
			for _, s := range tc.rows {
				rep.Add(Row{Status: s, Label: "x"})
			}
			if got := rep.ExitCode(); got != tc.want {
				t.Errorf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// A run that passed returns no error at all, so a caller cannot accidentally
// treat "passed" as "something went wrong".
func TestPassingReportReturnsNoError(t *testing.T) {
	rep := NewReport("scan")
	rep.Add(Row{Status: StatusPass, Label: "biome(.)"})
	if err := rep.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// The verdict line is the last thing a reader sees and the only place the
// duration appears.
func TestVerdictLineNamesTheCommandAndTheOutcome(t *testing.T) {
	for status, word := range map[Status]string{
		StatusPass:  "review passed in",
		StatusRefer: "review referred in",
		StatusFail:  "review failed in",
	} {
		var buf bytes.Buffer
		rep := NewReport("review")
		rep.Add(Row{Status: status, Label: "referral"})
		if err := rep.WriteText(&buf, false); err != nil {
			t.Fatalf("WriteText: %v", err)
		}
		if !strings.Contains(buf.String(), word) {
			t.Errorf("status %q: expected a verdict line containing %q, got:\n%s", status, word, buf.String())
		}
	}
}

// The machine surface carries statuses by name. A consumer that had to map
// "!" back onto three different meanings would be doing the work this
// document exists to save it.
func TestJSONCarriesStatusNamesNotGlyphs(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("review")
	rep.Add(Row{Status: StatusRefer, Label: "referral", Value: "no exemption matched", Detail: []string{"ask a human"}})
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.ContainsAny(buf.String(), "✓!✗→") {
		t.Errorf("the machine report must not carry glyphs: %s", buf.String())
	}
	var got jsonReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Verdict != VerdictRefer || got.Exit != 2 || got.Command != "review" {
		t.Errorf("got %+v, want review/refer/2", got)
	}
	if len(got.Rows) != 1 || got.Rows[0].Status != StatusRefer || len(got.Rows[0].Detail) != 1 {
		t.Errorf("row did not survive: %+v", got.Rows)
	}
}

// A detail line is indented so it can never begin a line the way a status row
// does. Scanner findings quote source, which can contain anything the source
// contains — including something shaped like a verdict.
func TestDetailLinesCannotForgeAStatusRow(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("scan")
	rep.Add(Row{Status: StatusFail, Label: "biome(.)", Value: "failed", Detail: []string{"✓ biome(.) ... passed"}})
	if err := rep.WriteText(&buf, false); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "✓") {
			t.Errorf("a detail line forged a status row: %q", line)
		}
	}
}

// The JSON document is what anything automated reads, so its keys are a
// published contract rather than an incidental encoding of whatever the
// renderer happens to hold.
// A row that ran no command has no log, and a key carrying an empty string
// would have a consumer link nowhere.
func TestJSONOmitsAnEmptyLog(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("scan")
	rep.Add(Row{Status: StatusPass, Label: "gosec", Value: "passed"})
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	if strings.Contains(buf.String(), `"log"`) {
		t.Errorf("document carries an empty log key: %s", buf.String())
	}
}

func TestJSONKeysArePartOfTheContract(t *testing.T) {
	var buf bytes.Buffer
	rep := NewReport("scan")
	rep.Add(Row{Status: StatusFail, Label: "biome(.)", Value: "failed", Detail: []string{"a finding"}, Log: ".lydite-reports/cli/test.log"})
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"command", "verdict", "exit", "duration_ms", "rows"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("the report is missing the %q key: %s", key, buf.String())
		}
	}
	rows, ok := doc["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected one row, got %v", doc["rows"])
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		t.Fatalf("expected a row object, got %v", rows[0])
	}
	for _, key := range []string{"status", "label", "value", "detail", "log"} {
		if _, ok := row[key]; !ok {
			t.Errorf("the row is missing the %q key: %s", key, buf.String())
		}
	}
}
