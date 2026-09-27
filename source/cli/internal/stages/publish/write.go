package publishstages

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"lydite/lydite/internal/ui"
)

// WriteIn is the comment WriteComment writes, and where.
type WriteIn struct {
	Comment ui.Comment
	// Out is the file the rendered comment is written to, or "-" for
	// Stdout.
	Out    string
	Stdout io.Writer
}

// WriteComment puts the rendered comment where the caller asked for it,
// creating the file's parent directories when they do not exist.
//
// Nothing is posted. Posting the comment is a separate step with a separate
// identity, so what this writes is exactly what a developer running it
// locally reads.
func WriteComment(_ context.Context, in WriteIn) (struct{}, error) {
	body := in.Comment.Render()
	if in.Out == "-" {
		_, err := io.WriteString(in.Stdout, body)
		return struct{}{}, err
	}
	if dir := filepath.Dir(in.Out); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return struct{}{}, err
		}
	}
	return struct{}{}, os.WriteFile(in.Out, []byte(body), 0o600)
}
