package teststages

import (
	"context"
	"strings"
	"testing"

	"lydite/lydite/internal/ui"
)

// declaredRepo declares two components, one watching a path nothing holds, and
// an exclude covering nothing.
func declaredRepo(t *testing.T) string {
	return pushedRepo(t, map[string]string{
		".lydite/components.yml": "components:\n" +
			"  - name: api\n    dir: api\n    runner: go-test\n    watch: [\"docs/openapi.json\"]\n" +
			"  - name: web\n    dir: web\n    command: [\"true\"]\n" +
			"excludes: [\"nothing/**\"]\n",
		"api/go.mod":  "module api\n\ngo 1.26\n",
		"api/main.go": "package api\n",
		"web/run.sh":  "true\n",
	})
}

// Both declaration gates report before the responsibility set is resolved, the
// orphans row first, and the set is what `--component` named — narrowed, so no
// figure over the repository is answered.
func TestDeclarationGatesTheDeclarationAndResolvesWhatThisRunOwns(t *testing.T) {
	root := declaredRepo(t)
	var warned strings.Builder

	out, err := Declaration(context.Background(), DeclarationIn{Dir: root, Components: []string{"web"}, Stderr: &warned})
	if err != nil {
		t.Fatalf("Declaration: %v", err)
	}
	if got := labels(out.Rows); got != "orphans,watch" {
		t.Fatalf("rows = %s, want orphans,watch", got)
	}
	if watch := out.Rows[1]; watch.Status != ui.StatusFail || !strings.Contains(strings.Join(watch.Detail, "\n"), "docs/openapi.json") {
		t.Errorf("watch = %+v, want a failure naming the pattern that covers no file", watch)
	}
	if !strings.Contains(warned.String(), `exclude "nothing/**" covers no file`) {
		t.Errorf("stderr = %q, want the unused exclude named", warned.String())
	}
	if len(out.Decl.Components) != 2 || len(out.Own) != 1 || out.Own[0].Name != "web" || !out.Narrowed {
		t.Errorf("decl = %d component(s), own = %+v, narrowed = %v; want both declared, web owned, narrowed",
			len(out.Decl.Components), out.Own, out.Narrowed)
	}
}

// With no `--component` the whole declaration is owned, and nothing is
// narrowed.
func TestDeclarationOwnsEveryComponentWhenNoneIsNamed(t *testing.T) {
	out, err := Declaration(context.Background(), DeclarationIn{Dir: declaredRepo(t)})
	if err != nil {
		t.Fatalf("Declaration: %v", err)
	}
	if len(out.Own) != 2 || out.Narrowed {
		t.Errorf("own = %+v, narrowed = %v; want every component, not narrowed", out.Own, out.Narrowed)
	}
}

// A component nobody declared is refused, not silently dropped from the set.
func TestDeclarationRefusesAComponentNobodyDeclared(t *testing.T) {
	_, err := Declaration(context.Background(), DeclarationIn{Dir: declaredRepo(t), Components: []string{"ghost"}})
	if err == nil || !strings.Contains(err.Error(), `no component named "ghost"`) {
		t.Errorf("err = %v, want the undeclared component named", err)
	}
}

// Outside a git repository neither gate can run, and both say so rather than
// passing.
func TestDeclarationOutsideARepositoryIsUnmeasured(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".lydite/components.yml", "components:\n  - name: api\n    dir: api\n    runner: go-test\n    watch: [\"Makefile\"]\n")
	write(t, root, "api/main.go", "package api\n")
	out, err := Declaration(context.Background(), DeclarationIn{Dir: root})
	if err != nil {
		t.Fatalf("Declaration: %v", err)
	}
	for _, r := range out.Rows {
		if r.Status != ui.StatusUnmeasured || r.Value != "no git repository" {
			t.Errorf("%s = %+v, want unmeasured for want of a repository", r.Label, r)
		}
	}
}
