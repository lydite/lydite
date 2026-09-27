package publishflow

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"lydite/lydite/internal/flow"
	publishstages "lydite/lydite/internal/stages/publish"
	"lydite/lydite/internal/ui"
)

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheFlowBuilds(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Name() != Name {
		t.Errorf("Name() = %q, want %q", f.Name(), Name)
	}
}

// oneDocument is a fake ReadDocuments that answers a single scan document for
// any directory it is asked about — the flows package cannot reach
// cmd/lydite's own reader, so a run here fakes both readers it needs.
func oneDocument(string) ([]ui.Document, error) {
	return []ui.Document{{
		Command: "scan",
		Rows:    []ui.Row{{Status: ui.StatusPass, Label: "gosec", Value: "clean"}},
	}}, nil
}

func noLog(string, string) []string { return nil }

// A run through the whole flow writes exactly what BuildComment and Render
// would produce for the same document, to the buffer Params names.
func TestARunWritesTheRenderedCommentToStdout(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var stdout bytes.Buffer
	params := Params{
		Dirs:          []string{"reports"},
		Base:          "deadbeefcafefeed",
		Version:       "1.2.3",
		ReadDocuments: oneDocument,
		ReadLog:       noLog,
		TailLines:     40,
		Out:           "-",
		Stdout:        &stdout,
	}
	result, err := f.Run(context.Background(), params.Inputs())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	docs, _ := oneDocument("reports")
	built, err := publishstages.BuildComment(context.Background(), publishstages.BuildIn{
		Gathered: []publishstages.ReportDir{{Dir: "reports", Documents: docs}},
		Base:     params.Base,
		Version:  params.Version,
		ReadLog:  noLog,
	})
	if err != nil {
		t.Fatalf("BuildComment: %v", err)
	}
	want := built.Comment.Render()

	if got := stdout.String(); got != want {
		t.Errorf("Run wrote:\n%s\nwant:\n%s", got, want)
	}
	if _, err := flow.Output[publishstages.BuildOut](result, StageBuildComment); err != nil {
		t.Errorf("reading the build-comment output: %v", err)
	}
}

// write-comment's own error is the only one this flow can raise, and it
// reaches the caller as a *flow.StageError wrapping exactly what the
// filesystem said.
func TestAnUnwritableOutSurfacesAsAStageError(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{
		Dirs:          []string{"reports"},
		ReadDocuments: oneDocument,
		ReadLog:       noLog,
		// A path under a file, rather than a directory, is refused by
		// MkdirAll however this process is run.
		Out:    "/dev/null/no-such-directory/comment.md",
		Stdout: &bytes.Buffer{},
	}
	_, err = f.Run(context.Background(), params.Inputs())
	if err == nil {
		t.Fatal("Run: want an error, got nil")
	}
	var stageErr *flow.StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("Run error is %T, want *flow.StageError", err)
	}
	if stageErr.Stage != StageWriteComment {
		t.Errorf("stageErr.Stage = %q, want %q", stageErr.Stage, StageWriteComment)
	}
	if stageErr.Err == nil {
		t.Error("stageErr.Err is nil, want the filesystem error")
	}
}
