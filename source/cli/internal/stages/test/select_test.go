package teststages

import (
	"context"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/runner"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/ui"
)

// selectionDecl is two suites and a component declaring none.
func selectionDecl() component.File {
	return component.File{Components: []component.Component{
		{Name: "a", Dir: "moda", Runner: runner.GoTest},
		{Name: "b", Dir: "modb", Runner: runner.GoTest},
		{Name: "s", Dir: "scripts", DeclaredLang: runner.Shell},
	}}
}

// A run not asked to narrow runs everything it owns, and selection reports no
// row and skips nothing.
func TestSelectAffectedNotAskedRunsEverythingOwned(t *testing.T) {
	decl := selectionDecl()
	out, err := SelectAffected(context.Background(), SelectAffectedIn{Dir: t.TempDir(), Decl: decl, Own: decl.Components})
	if err != nil {
		t.Fatalf("SelectAffected: %v", err)
	}
	if len(out.Selected) != 3 || out.Ordered != nil || out.Skipped != nil || len(out.Rows) != 0 {
		t.Errorf("out = %+v, want every owned component selected and nothing else", out)
	}
}

// An empty declaration is not a change that selected nothing, and selection is
// never asked which it is: outside any repository, where a git walk would
// fail, it still answers.
func TestSelectAffectedOverNoDeclarationAsksNothing(t *testing.T) {
	out, err := SelectAffected(context.Background(), SelectAffectedIn{Dir: t.TempDir(), Affected: true})
	if err != nil {
		t.Fatalf("SelectAffected over no declaration: %v", err)
	}
	if len(out.Selected) != 0 || len(out.Rows) != 0 {
		t.Errorf("out = %+v, want nothing selected and no select row", out)
	}
}

// A change to one component runs that one. Every other owned component takes
// its test row from selection — `not affected`, or the row its declaration
// gives it when it declares no suite — for the run to interleave into
// declaration order.
func TestSelectAffectedRunsWhatTheChangeTouched(t *testing.T) {
	decl := selectionDecl()
	root := pushedRepo(t, map[string]string{
		"moda/x.go":       "package moda\n",
		"modb/x.go":       "package modb\n",
		"scripts/sync.sh": "true\n",
	})
	commit(t, root, "moda/x.go", "package moda\n\n// X changed.\nconst X = 1\n")

	out, err := SelectAffected(context.Background(), SelectAffectedIn{Dir: root, Decl: decl, Own: decl.Components, Affected: true})
	if err != nil {
		t.Fatalf("SelectAffected: %v", err)
	}
	if len(out.Selected) != 1 || out.Selected[0].Name != "a" {
		t.Fatalf("selected = %+v, want a alone", out.Selected)
	}
	if len(out.Ordered) != 3 {
		t.Errorf("ordered = %+v, want the owned declaration", out.Ordered)
	}
	if got := out.Skipped["b"]; got.Status != ui.StatusUnmeasured || got.Label != "test(b)" || got.Value != "not affected" {
		t.Errorf("skipped b = %+v, want test(b) unmeasured as not affected", got)
	}
	if got, want := out.Skipped["s"], testrun.NoSuiteTestRow("test", "s"); got.Value != want.Value || got.Label != want.Label {
		t.Errorf("skipped s = %+v, want its declaration's row %+v", got, want)
	}
	if len(out.Rows) != 1 || out.Rows[0].Label != "select" || out.Rows[0].Value != "1 of 3 affected" {
		t.Errorf("rows = %+v, want one select row counting 1 of 3", out.Rows)
	}
}

// Selection over the whole declaration is scoped to what this run owns, so a
// shard reports only its own share of the one answer.
func TestSelectAffectedIsScopedToWhatThisRunOwns(t *testing.T) {
	decl := selectionDecl()
	root := pushedRepo(t, map[string]string{"moda/x.go": "package moda\n", "modb/x.go": "package modb\n", "scripts/sync.sh": "true\n"})
	commit(t, root, "moda/x.go", "package moda\n\n// X changed.\nconst X = 1\n")

	out, err := SelectAffected(context.Background(), SelectAffectedIn{Dir: root, Decl: decl, Own: decl.Components[1:2], Affected: true})
	if err != nil {
		t.Fatalf("SelectAffected: %v", err)
	}
	if len(out.Selected) != 0 || len(out.Skipped) != 1 || out.Skipped["b"].Label != "test(b)" {
		t.Errorf("out = %+v, want b's skip alone, and a — owned elsewhere — nowhere", out)
	}
	if out.Rows[0].Value != "1 of 3 affected" {
		t.Errorf("select = %q, want the whole declaration's answer", out.Rows[0].Value)
	}
}

// No merge-base is an error, never a fallback in either direction.
func TestSelectAffectedWithoutAMergeBaseIsAnError(t *testing.T) {
	decl := selectionDecl()
	_, err := SelectAffected(context.Background(), SelectAffectedIn{Dir: t.TempDir(), Decl: decl, Own: decl.Components, Affected: true})
	if err == nil || !strings.Contains(err.Error(), "--affected needs the merge-base") {
		t.Errorf("err = %v, want the missing merge-base named", err)
	}
}
