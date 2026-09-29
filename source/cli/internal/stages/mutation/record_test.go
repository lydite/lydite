package mutationstages

import (
	"bytes"
	"encoding/json"
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

// The reused count of a component travels into the document it hands on, and a
// component that reused nothing leaves the field out of the JSON entirely.
func TestAComponentsReusedCountIsCarriedIntoTheDocument(t *testing.T) {
	reusing := completed("app", mutation.Summary{Killed: 3, Survived: 1}, time.Second)
	reusing.Reused = 3
	fresh := completed("api", mutation.Summary{Survived: 2}, time.Second)

	doc := countsOf("tree", []ComponentOutcome{reusing, fresh})
	if got := doc.Components["app"].Reused; got != 3 {
		t.Errorf("app reused = %d, want 3", got)
	}
	if got := doc.Components["api"].Reused; got != 0 {
		t.Errorf("api reused = %d, want 0", got)
	}
	app, err := json.Marshal(doc.Components["app"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), `"reused":3`) {
		t.Errorf("app = %s, want reused present", app)
	}
	api, err := json.Marshal(doc.Components["api"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(api), "reused") {
		t.Errorf("api = %s, want reused absent", api)
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

// A component the deadline stopped is recorded apart from the complete ones,
// with how far it got beside the verdicts it reached: dropped, its survivors
// would reach no fold, and listed among the complete, its partial count would
// read as a finished score to anything unaware of the marker.
func TestAnIncompleteOutcomeIsRecordedAsIncomplete(t *testing.T) {
	cut := completed("app", mutation.Summary{Killed: 1, Survived: 1}, 7*time.Second)
	cut.Kind, cut.Measured, cut.Wanted, cut.Reused = KindIncomplete, 2, 5, 1

	doc := countsOf("tree", []ComponentOutcome{cut, completed("lib", mutation.Summary{Killed: 3}, time.Second)})
	if _, ok := doc.Components["app"]; ok {
		t.Errorf("the incomplete component is among the complete: %+v", doc.Components)
	}
	want := mutation.CountsOf(mutation.Summary{Killed: 1, Survived: 1}, 7*time.Second)
	want.Reused = 1
	want.Incomplete = &mutation.IncompleteCounts{Measured: 2, Wanted: 5}
	if got := doc.IncompleteComponents["app"]; !reflect.DeepEqual(got, want) {
		t.Errorf("app = %+v, want %+v", got, want)
	}
	if _, ok := doc.Components["lib"]; !ok || len(doc.IncompleteComponents) != 1 {
		t.Errorf("document = %+v, want lib complete and app alone incomplete", doc)
	}
	if none := countsOf("tree", []ComponentOutcome{completed("lib", mutation.Summary{Killed: 3}, time.Second)}); none.IncompleteComponents != nil {
		t.Errorf("a run nothing stopped wrote incomplete components: %+v", none.IncompleteComponents)
	}
}
