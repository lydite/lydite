package declaration

import "testing"

func TestDeclaredReadsTheMarkerInEitherSpelling(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"a bang in the type", "feat!: drop the v1 client", true},
		{"a bang after a scope", "fix(api)!: narrow Store.Put", true},
		{"a breaking change footer", "feat: widen Config\n\nThe timeout is now int64.\n\nBREAKING CHANGE: Config.Timeout changed from int to int64", true},
		{"the hyphenated footer", "feat: widen Config\n\nBREAKING-CHANGE: Config.Timeout changed from int to int64", true},
		{"a footer separated by a hash", "feat: widen Config\n\nBREAKING CHANGE #4", true},
		{"a bang in the description", "feat: add a ! flag to the CLI", false},
		{"a bang inside the scope", "feat(a!b): rename the parser", false},
		{"a bang ahead of the scope", "feat!(scope): not a conventional header", false},
		{"a bang with no colon behind it", "feat! drop the v1 client", false},
		{"a footer spelled in the wrong case", "feat: widen Config\n\nBreaking change: Config.Timeout changed", false},
		{"a footer spelled with an underscore", "feat: widen Config\n\nBREAKING_CHANGE: Config.Timeout changed", false},
		{"the words without a footer separator", "docs: explain what a BREAKING CHANGE footer is", false},
		{"no marker anywhere", "fix(scan): order the findings\n\nModuleChanges iterates a map.", false},
		{"a header quoted in the body", "docs: quote a subject\n\nThe commit said feat!: drop the v1 client.", false},
		{"nothing at all", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Declared(c.text); got != c.want {
				t.Errorf("Declared(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// The title and the commits are two sources of one claim, and neither subsumes
// the other: a squash merge lands the title, and a local run has no title at
// all. Either one declaring is enough, and neither is required.
func TestAnyDeclaredReadsBothSources(t *testing.T) {
	title := "feat!: drop the v1 client"
	commits := []string{"feat: add the v2 client", "chore: tidy"}

	if !AnyDeclared(append([]string{title}, commits...)...) {
		t.Error("a title-only declaration was not read as declared")
	}
	if !AnyDeclared(append([]string{"feat: add the v2 client"}, "refactor!: drop the v1 client", "chore: tidy")...) {
		t.Error("a commit-only declaration was not read as declared")
	}
	if AnyDeclared(append([]string{"feat: add the v2 client"}, commits...)...) {
		t.Error("a range with no marker in the title or any commit was read as declared")
	}
	if AnyDeclared() {
		t.Error("no text at all was read as declared")
	}
}
