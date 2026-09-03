package ui

import (
	"bytes"
	"strings"
	"testing"
)

// The document is written by one process and read by another, so a row that
// does not survive the round trip is a row the pull-request comment cannot
// show.
func TestADocumentSurvivesTheRoundTrip(t *testing.T) {
	rep := NewReport("test")
	rep.Add(Row{Status: StatusPass, Label: "test(cli)", Value: "passed", Log: ".lydite-reports/cli/test.log"})
	rep.Add(Row{
		Status: StatusFail, Label: "test(web)", Value: "failed",
		Detail: []string{"FAIL src/app.test.ts", "1 failed"},
		Log:    ".lydite-reports/web/test.log",
	})
	rep.Add(Row{Status: StatusUnmeasured, Label: "test(sdk)", Value: "not affected"})

	var buf bytes.Buffer
	if err := rep.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	doc, err := ReadDocument(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if doc.Command != "test" {
		t.Errorf("command is %q, want test", doc.Command)
	}
	if doc.Verdict != VerdictFail || doc.Exit != 1 {
		t.Errorf("verdict %q exit %d, want fail and 1", doc.Verdict, doc.Exit)
	}
	if len(doc.Rows) != 3 {
		t.Fatalf("%d rows survived, want 3", len(doc.Rows))
	}
	failing := doc.Rows[1]
	if failing.Log != ".lydite-reports/web/test.log" {
		t.Errorf("the log did not survive: %q", failing.Log)
	}
	if len(failing.Detail) != 2 {
		t.Errorf("the detail did not survive: %v", failing.Detail)
	}
	if doc.Rows[2].Status != StatusUnmeasured {
		t.Errorf("an unmeasured row read back as %q", doc.Rows[2].Status)
	}
}

// The reader is routinely an older binary than the writer: a workflow pins the
// version that renders the comment separately from the one that ran the suite.
// Refusing a key a newer lydite added would fail the comment for a run that
// succeeded.
func TestAKeyTheReaderDoesNotKnowIsAccepted(t *testing.T) {
	doc, err := ReadDocument(strings.NewReader(
		`{"command":"scan","verdict":"pass","exit":0,"duration_ms":12,"shards":4,` +
			`"rows":[{"status":"pass","label":"gosec(cli)","value":"passed","mutants":3}]}`))
	if err != nil {
		t.Fatalf("a document with an unknown key was refused: %v", err)
	}
	if len(doc.Rows) != 1 || doc.Rows[0].Label != "gosec(cli)" {
		t.Fatalf("the known keys did not survive: %+v", doc)
	}
}

// A file that parses as JSON but carries no verdict is not a report, and
// rendering it as one would put an empty section in the comment where a
// missing input should have been named.
func TestSomethingThatIsNotAReportIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"an unrelated object": `{"name":"lydite-biome-pin","version":"0.0.0"}`,
		"no verdict":          `{"command":"scan","rows":[]}`,
		"no command":          `{"verdict":"pass","rows":[]}`,
		"not json":            `passed`,
	} {
		if _, err := ReadDocument(strings.NewReader(body)); err == nil {
			t.Errorf("%s was accepted as a report", name)
		}
	}
}
