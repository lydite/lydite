package mutationstages

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/mutation"
)

func completed(name string, s mutation.Summary, elapsed time.Duration) ComponentOutcome {
	return ComponentOutcome{Component: component.Component{Name: name}, Kind: KindCompleted, Summary: s, Elapsed: elapsed}
}

// Every outcome it is given whose mutants ran is counted, and only those. A
// component that did not run is absent rather than zeroed, which would read,
// permanently, as a suite that killed everything.
func TestExactlyTheOutcomesThatRanAreRecorded(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"a.txt": "seed\n"})
	reports := filepath.Join(root, ".lydite-reports")
	var ignored []string
	var warnings bytes.Buffer
	_, err := RecordMutants(t.Context(), RecordMutantsIn{
		Dir:        root,
		ReportsDir: reports,
		Ignore: func(dir string) error {
			// The directory exists by the time it is ignored, since what
			// ignores it is written inside it.
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("ignored %s before it existed: %v", dir, err)
			}
			ignored = append(ignored, dir)
			return nil
		},
		Components: []ComponentOutcome{
			completed("app", mutation.Summary{Killed: 3, Survived: 1}, 2*time.Second),
			{Component: component.Component{Name: "web"}, Kind: KindBaselineFailed, Scheduled: true},
			{Component: component.Component{Name: "cli"}, Kind: KindNotRun, Scheduled: true},
			{Component: component.Component{Name: "docs"}, Kind: KindBlocked},
			func() ComponentOutcome {
				o := completed("api", mutation.Summary{Survived: 2}, time.Second)
				o.Scheduled = true
				return o
			}(),
		},
		Warnings: &warnings,
	})
	if err != nil {
		t.Fatalf("recording failed the run: %v", err)
	}
	if warnings.Len() != 0 {
		t.Errorf("warned: %s", warnings.String())
	}
	if !reflect.DeepEqual(ignored, []string{reports}) {
		t.Errorf("ignored %v, want the reports directory once", ignored)
	}
	doc, err := mutation.ReadCounts(reports)
	if err != nil {
		t.Fatal(err)
	}
	if want := git(t, root, "rev-parse", "HEAD^{tree}"); doc.Tree != want {
		t.Errorf("tree = %s, want HEAD's %s", doc.Tree, want)
	}
	want := map[string]mutation.ComponentCounts{
		"app": mutation.CountsOf(mutation.Summary{Killed: 3, Survived: 1}, 2*time.Second),
		"api": mutation.CountsOf(mutation.Summary{Survived: 2}, time.Second),
	}
	if !reflect.DeepEqual(doc.Components, want) {
		t.Errorf("components = %+v, want app and api alone", doc.Components)
	}
}

// A run that mutated nothing still names its tree: "this tree, and nothing on
// it" is an answer, and a later fold tells it from a shard whose job died.
func TestARunThatMutatedNothingStillNamesItsTree(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"a.txt": "seed\n"})
	reports := filepath.Join(root, ".lydite-reports")
	if _, err := RecordMutants(t.Context(), RecordMutantsIn{Dir: root, ReportsDir: reports,
		Ignore: func(string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	doc, err := mutation.ReadCounts(reports)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Tree == "" || doc.Components != nil {
		t.Errorf("document = %+v, want a tree and no components", doc)
	}
}

// Every failure to record warns and none of them fails the run: the mutants
// ran and their verdict is in the report.
func TestAFailureToRecordWarnsAndNeverFails(t *testing.T) {
	var warnings bytes.Buffer
	if _, err := RecordMutants(t.Context(), RecordMutantsIn{Dir: t.TempDir(), ReportsDir: t.TempDir(),
		Ignore: func(string) error { return nil }, Warnings: &warnings}); err != nil {
		t.Fatalf("a tree that could not be resolved failed the run: %v", err)
	}
	if !strings.HasPrefix(warnings.String(), "warning: could not resolve this tree, so the mutant counts were not written: ") {
		t.Errorf("warned %q", warnings.String())
	}

	root := mutationRepoWithOrigin(t, map[string]string{"a.txt": "seed\n"})
	// A file where the reports directory should be.
	blocked := filepath.Join(root, "blocked")
	writeFile(t, root, "blocked", "")
	warnings.Reset()
	if _, err := RecordMutants(t.Context(), RecordMutantsIn{Dir: root, ReportsDir: filepath.Join(blocked, "reports"),
		Ignore: func(string) error { return nil }, Warnings: &warnings}); err != nil {
		t.Fatalf("a directory that could not be created failed the run: %v", err)
	}
	if !strings.HasPrefix(warnings.String(), "warning: could not write the mutant counts: ") {
		t.Errorf("warned %q", warnings.String())
	}
}

// Keeping the reports out of git is best-effort: a directory that cannot hold
// what ignores it is no reason to lose the counts.
func TestACountsDocumentIsWrittenWhenItCannotBeIgnored(t *testing.T) {
	root := mutationRepoWithOrigin(t, map[string]string{"a.txt": "seed\n"})
	reports := filepath.Join(root, ".lydite-reports")
	var warnings bytes.Buffer
	if _, err := RecordMutants(t.Context(), RecordMutantsIn{Dir: root, ReportsDir: reports,
		Ignore: func(string) error { return errors.New("read-only") }, Warnings: &warnings}); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.ReadCounts(reports); err != nil {
		t.Errorf("the counts were lost to the ignore file: %v", err)
	}
	if warnings.Len() != 0 {
		t.Errorf("warned %q", warnings.String())
	}
}
