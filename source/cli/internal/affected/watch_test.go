package affected

import (
	"testing"

	"lydite/lydite/internal/component"
)

// A watch pattern matching no file in the tree is a component that will not
// run when its input changes — silently, permanently, and green every time.
// It is deliberately fatal where an unmatched exclude only warns: an exclude
// covering nothing leaves the orphan gate stricter, and this leaves a suite
// unrun.
func TestUnmatchedWatchIsFound(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "tally", Dir: "rust", Runner: "cargo-nextest", Watch: []string{"Makefile", "VERSION"}},
		{Name: "sdk", Dir: "go/sdk", Runner: "go-test", Watch: []string{"docs/openapi.json"}},
	}}
	files := []string{"Makefile", "VERSION", "rust/src/lib.rs", "go/sdk/client.go"}

	got := UnmatchedWatch(f, files)
	if len(got) != 1 {
		t.Fatalf("UnmatchedWatch = %+v, want exactly the sdk/docs-openapi pair", got)
	}
	if got[0].Component != "sdk" || got[0].Pattern != "docs/openapi.json" {
		t.Errorf("got %+v, want {sdk docs/openapi.json}", got[0])
	}
}

func TestEveryWatchMatchingSomethingIsSilent(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "tally", Dir: "rust", Runner: "cargo-nextest", Watch: []string{"Makefile", "docs/**"}},
	}}
	files := []string{"Makefile", "docs/adr/0001.md", "rust/src/lib.rs"}
	if got := UnmatchedWatch(f, files); len(got) != 0 {
		t.Errorf("UnmatchedWatch = %+v, want none", got)
	}
}

// A directory name written where a pattern was needed is the typo this gate
// exists for: "docs" matches a file called docs and nothing under it.
func TestDirectoryShapedWatchIsUnmatched(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "a", Dir: ".", Runner: "go-test", Watch: []string{"docs"}},
	}}
	got := UnmatchedWatch(f, []string{"docs/openapi.json", "main.go"})
	if len(got) != 1 || got[0].Pattern != "docs" {
		t.Errorf("UnmatchedWatch = %+v, want the bare directory name reported", got)
	}
}

// Reported in declaration order, then pattern order, so two runs over one
// declaration produce the same rows.
func TestUnmatchedWatchIsOrdered(t *testing.T) {
	f := component.File{Components: []component.Component{
		{Name: "b", Dir: "b", Runner: "go-test", Watch: []string{"z-gone", "a-gone"}},
		{Name: "a", Dir: "a", Runner: "go-test", Watch: []string{"m-gone"}},
	}}
	got := UnmatchedWatch(f, []string{"a/x.go", "b/x.go"})
	want := []string{"b:z-gone", "b:a-gone", "a:m-gone"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Component+":"+got[i].Pattern != w {
			t.Errorf("got[%d] = %s:%s, want %s", i, got[i].Component, got[i].Pattern, w)
		}
	}
}
