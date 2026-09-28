package shard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/ui"
)

// shardDocument writes rep as the document named file inside a fresh
// directory, and returns the directory.
func shardDocument(t *testing.T, file string, rep *ui.Report) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, file))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := rep.WriteJSON(f); err != nil {
		t.Fatal(err)
	}
	return dir
}

// shardFile writes content as the file named file inside a fresh directory,
// and returns the directory.
func shardFile(t *testing.T, file, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadReadsAShardsDocument(t *testing.T) {
	rep := ui.NewReport("mutation")
	rep.Add(ui.Row{Status: ui.StatusFail, Label: "mutation(api)", Value: "1 of 2 mutant(s) survived"})
	claim := finding.Finding{Gate: "mutation", Component: "api", Path: "api/a.go", Line: 3,
		Message: "survived", Site: "a < b"}
	rep.AddFindings(claim)
	dir := shardDocument(t, "mutation.json", rep)

	shard := Read(dir, "mutation")
	if !shard.Read || shard.Err != nil {
		t.Fatalf("shard = %+v, want read with no error", shard)
	}
	if shard.Dir != dir {
		t.Errorf("Dir = %q, want %q", shard.Dir, dir)
	}
	doc := shard.Document
	if doc.Command != "mutation" || doc.Verdict != ui.VerdictFail {
		t.Errorf("document = %s/%s, want mutation/fail", doc.Command, doc.Verdict)
	}
	if len(doc.Rows) != 1 || doc.Rows[0].Label != "mutation(api)" {
		t.Errorf("rows = %+v, want the shard's one row", doc.Rows)
	}
	if len(doc.Findings) != 1 || doc.Findings[0].Path != claim.Path || doc.Findings[0].Message != claim.Message {
		t.Errorf("findings = %+v, want the shard's one claim", doc.Findings)
	}
}

// A directory holding no document is a shard whose job died or never
// uploaded, and it is still a shard: the caller's report has to say it went
// missing.
func TestReadReturnsAnAbsentDocumentAsTheShardsError(t *testing.T) {
	dir := t.TempDir()

	shard := Read(dir, "test")
	if shard.Read {
		t.Fatalf("shard = %+v, want unread", shard)
	}
	if !errors.Is(shard.Err, fs.ErrNotExist) {
		t.Errorf("Err = %v, want a not-exist error", shard.Err)
	}
	if shard.Dir != dir {
		t.Errorf("Dir = %q, want %q", shard.Dir, dir)
	}
}

// A document that is there and will not parse is named by path, so the row a
// caller builds from the error says which file to look at.
func TestReadNamesAnUnparseableDocumentByPath(t *testing.T) {
	for name, content := range map[string]string{
		"not json":     "{not json",
		"not a report": `{"rows": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := shardFile(t, "test.json", content)

			shard := Read(dir, "test")
			if shard.Read || shard.Err == nil {
				t.Fatalf("shard = %+v, want unread with an error", shard)
			}
			if path := filepath.Join(dir, "test.json"); !strings.HasPrefix(shard.Err.Error(), path+": ") {
				t.Errorf("Err = %q, want it to name %s", shard.Err, path)
			}
			if errors.Is(shard.Err, fs.ErrNotExist) {
				t.Errorf("Err = %v reads as absent, but the document is there", shard.Err)
			}
		})
	}
}

// The command picks the document by name, and only by name: another command's
// document beside it is not this command's, and a document at this command's
// name is read whatever its own command field says.
func TestReadFindsTheDocumentByTheCommandsNameAlone(t *testing.T) {
	t.Run("another command's document", func(t *testing.T) {
		dir := shardDocument(t, "scan.json", ui.NewReport("scan"))

		shard := Read(dir, "test")
		if shard.Read || !errors.Is(shard.Err, fs.ErrNotExist) {
			t.Errorf("shard = %+v, want test's document absent beside scan's", shard)
		}
	})
	t.Run("a document naming another command", func(t *testing.T) {
		dir := shardDocument(t, "test.json", ui.NewReport("scan"))

		shard := Read(dir, "test")
		if !shard.Read || shard.Err != nil {
			t.Fatalf("shard = %+v, want read", shard)
		}
		if shard.Document.Command != "scan" {
			t.Errorf("Command = %q, want the document's own %q", shard.Document.Command, "scan")
		}
	})
}
