package publishstages

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/ui"
)

func writtenComment() ui.Comment {
	return ui.Comment{Standing: true, Verdict: ui.VerdictPass, Headline: "every check passed", Version: "v1.2.3"}
}

// "-" is stdout: the rendered comment and nothing else reaches it.
func TestWriteCommentToStdout(t *testing.T) {
	var stdout bytes.Buffer
	if _, err := WriteComment(context.Background(), WriteIn{
		Comment: writtenComment(), Out: "-", Stdout: &stdout,
	}); err != nil {
		t.Fatalf("WriteComment: %v", err)
	}
	if got, want := stdout.String(), writtenComment().Render(); got != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got, want)
	}
}

// A file is written with the rendered comment, its parent directories created
// when they do not exist, and nothing reaches stdout.
func TestWriteCommentToAFileCreatesItsDirectories(t *testing.T) {
	dir := t.TempDir()
	const rel = "nested/deeper/comment.md"
	var stdout bytes.Buffer
	if _, err := WriteComment(context.Background(), WriteIn{
		Comment: writtenComment(), Out: filepath.Join(dir, filepath.FromSlash(rel)), Stdout: &stdout,
	}); err != nil {
		t.Fatalf("WriteComment: %v", err)
	}
	got, err := fs.ReadFile(os.DirFS(dir), rel)
	if err != nil {
		t.Fatal(err)
	}
	if want := writtenComment().Render(); string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout received %q, want nothing", stdout.String())
	}
}

// A file that cannot be written is the stage's error, returned as the
// filesystem gave it so the caller names exactly what failed.
func TestWriteCommentReturnsAWriteThatFailed(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := WriteComment(context.Background(), WriteIn{
		Comment: writtenComment(), Out: filepath.Join(blocker, "comment.md"), Stdout: &bytes.Buffer{},
	})
	var pathErr *os.PathError
	if err == nil {
		t.Fatal("WriteComment under a regular file succeeded")
	}
	if !errors.As(err, &pathErr) {
		t.Errorf("err = %v (%T), want the filesystem's own *os.PathError", err, err)
	}
}
