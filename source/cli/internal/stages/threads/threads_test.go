package threadsstages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/threads"
)

func located(path string, anchor finding.Anchor, site string) finding.Finding {
	return finding.Finding{Gate: "gosec", Path: path, Line: 3, Message: "m", Site: site, Anchor: anchor}
}

func TestReadFindingsKeepsOneLocatedCopyPerFingerprintAndNamesTheRest(t *testing.T) {
	onLine := located("a.go", finding.AnchorLine, "one")
	onFile := located("b.go", finding.AnchorFile, "two")
	nowhere := located("c.go", finding.AnchorNowhere, "three")
	reader := fakeReader{
		found: map[string][]finding.Finding{
			"first":  {onLine, nowhere, onFile},
			"second": {onLine},
		},
		err: map[string]error{"broken": errors.New("open broken: no such file or directory")},
	}

	out, err := ReadFindings(context.Background(), ReadFindingsIn{
		Reports: []string{"first", "broken", "second"},
		Reader:  reader,
	})
	if err != nil {
		t.Fatalf("an unreadable directory is named, not an error: %v", err)
	}
	if want := []finding.Finding{onLine, onFile}; !reflect.DeepEqual(out.Located, want) {
		t.Errorf("Located = %+v, want the line and file claims once each, in order: %+v", out.Located, want)
	}
	if out.Total != 4 {
		t.Errorf("Total = %d, want 4: every finding read, located or not, before dedup", out.Total)
	}
	if want := []string{onLine.Fingerprint()}; !reflect.DeepEqual(out.Dropped, want) {
		t.Errorf("Dropped = %v, want %v", out.Dropped, want)
	}
	if want := []string{"broken holds no findings: open broken: no such file or directory"}; !reflect.DeepEqual(out.Missing, want) {
		t.Errorf("Missing = %q, want %q", out.Missing, want)
	}
}

func TestListThreadsGroupsRepliesUnderTheirRoot(t *testing.T) {
	repository := &threadsFakeRepository{t: t,
		reviewComments: func(_ context.Context, number int) ([]threads.Comment, error) {
			if number != 7 {
				t.Errorf("listed pull request %d, want 7", number)
			}
			return []threads.Comment{
				{ID: 1, Body: threads.Marker("fp1"), Path: "a.go", Line: 3},
				{ID: 2, Body: "a reply", Path: "a.go", InReplyTo: 1},
				{ID: 3, Body: threads.Marker("fp2"), Path: "b.go", Line: 9},
			}, nil
		},
	}

	out, err := ListThreads(context.Background(), ListThreadsIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Standing != 2 || len(out.Threads) != 2 {
		t.Fatalf("Standing = %d with %d threads, want 2 of each", out.Standing, len(out.Threads))
	}
	if got := len(out.Threads[0].Replies); got != 1 {
		t.Errorf("the first thread holds %d replies, want 1", got)
	}
}

func TestListThreadsReturnsTheListingsError(t *testing.T) {
	refused := errors.New("listing refused")
	repository := &threadsFakeRepository{t: t,
		reviewComments: func(context.Context, int) ([]threads.Comment, error) { return nil, refused },
	}
	if _, err := ListThreads(context.Background(), ListThreadsIn{Repository: repository, Ref: forge.PullRequestRef{Number: 7}}); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the listing's own error", err)
	}
}

func TestPlanIsTheDeltaBetweenTheClaimsAndTheThreads(t *testing.T) {
	claims := []finding.Finding{located("a.go", finding.AnchorLine, "one")}
	standing := threads.Threads([]threads.Comment{{ID: 5, Body: threads.Marker("gone"), Path: "z.go", Line: 1}})

	out, err := Plan(context.Background(), PlanIn{Located: claims, Threads: standing, Ref: forge.PullRequestRef{Number: 7, SHA: "abc123"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := threads.Delta(claims, standing, 7, "abc123"); !reflect.DeepEqual(out.Ops, want) {
		t.Errorf("Ops = %+v, want %+v", out.Ops, want)
	}
	if len(out.Ops.Create) != 1 || len(out.Ops.Delete) != 1 {
		t.Errorf("Ops = %+v, want one thread opened and one closed", out.Ops)
	}
}

func sampleOps() threads.Ops {
	return threads.Ops{
		Version: threads.Version, PullRequest: 7, Head: "abc123",
		Create: []threads.Create{{Fingerprint: "fp", Path: "a.go", Line: 3, Subject: "line", Body: "b"}},
		Reply:  []threads.Reply{{Comment: 2, Body: "r"}},
		Delete: []threads.Delete{{Comment: 1, Refused: "x"}},
	}
}

func TestWriteOpsWritesIndentedJSONPrivatelyUnderAPrivateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "ops")
	path := filepath.Join(dir, "threads.json")
	ops := sampleOps()

	if _, err := WriteOps(context.Background(), WriteOpsIn{Path: path, Ops: ops}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(ops, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want)+"\n" {
		t.Errorf("document =\n%s\nwant indented JSON with a trailing newline:\n%s\n", raw, want)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	parent, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := parent.Mode().Perm(); got != 0o750 {
		t.Errorf("directory mode = %o, want 750", got)
	}
}

func TestWriteOpsWritesABareFileNameInTheWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := WriteOps(context.Background(), WriteOpsIn{Path: "threads.json", Ops: sampleOps()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("threads.json"); err != nil {
		t.Fatalf("no document in the working directory: %v", err)
	}
}

func TestWriteOpsReturnsTheDirectorysError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "under", "threads.json")
	if _, err := WriteOps(context.Background(), WriteOpsIn{Path: path, Ops: sampleOps()}); err == nil {
		t.Fatal("a directory that cannot be made is an error, got none")
	}
}
