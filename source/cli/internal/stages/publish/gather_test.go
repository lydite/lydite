package publishstages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lydite/lydite/internal/ui"
)

// Every named directory comes back, in the order it was named, carrying
// either its documents or why it contributed none — an unreadable directory
// is content for the comment, never an error that takes the comment down.
func TestGatherReportsPairsEveryDirectoryWithWhatItHeld(t *testing.T) {
	doc := ui.Document{Command: "test", Rows: []ui.Row{{Status: ui.StatusPass, Label: "test(cli)", Value: "passed"}}}
	read := func(dir string) ([]ui.Document, error) {
		switch dir {
		case "present":
			return []ui.Document{doc}, nil
		case "empty":
			return nil, nil
		case "broken":
			return nil, errors.New("broken/scan.json: read report document: unexpected EOF")
		default:
			// What os.ReadDir answers for a directory that is not there.
			_, err := os.ReadDir(filepath.Join(t.TempDir(), dir))
			return nil, err
		}
	}

	out, err := GatherReports(context.Background(), GatherIn{
		Dirs:          []string{"never-uploaded", "present", "broken", "empty"},
		ReadDocuments: read,
	})
	if err != nil {
		t.Fatalf("GatherReports: %v", err)
	}
	want := []ReportDir{
		{Dir: "never-uploaded", Missing: "no such directory, so nothing from it is in this comment"},
		{Dir: "present", Documents: []ui.Document{doc}},
		{Dir: "broken", Missing: "broken/scan.json: read report document: unexpected EOF"},
		{Dir: "empty", Missing: "holds no report document"},
	}
	if !reflect.DeepEqual(out.Gathered, want) {
		t.Errorf("Gathered =\n%#v\nwant\n%#v", out.Gathered, want)
	}
}

// Naming no directory gathers nothing, rather than failing.
func TestGatherReportsOverNoDirectoryGathersNothing(t *testing.T) {
	out, err := GatherReports(context.Background(), GatherIn{
		ReadDocuments: func(string) ([]ui.Document, error) {
			t.Fatal("ReadDocuments: unexpected call")
			return nil, nil
		},
	})
	if err != nil || len(out.Gathered) != 0 {
		t.Errorf("GatherReports() = %v, %v; want nothing and no error", out.Gathered, err)
	}
}
