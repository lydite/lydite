package referral

import (
	"strings"
	"testing"
)

func TestMatchIsAnchoredAndSegmentAware(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"README.md", "README.md", true},
		// Anchored, unlike gitignore: a slash-less pattern does not float to
		// any depth. These patterns decide what merges without a human, so
		// one that silently covers more than it appears to is the whole
		// failure mode.
		{"README.md", "docs/README.md", false},
		{"**/README.md", "docs/vendor/README.md", true},
		{"docs/**", "docs/adr/0013.md", true},
		{"docs/**", "docsite/index.md", false},
		{"*.md", "README.md", true},
		// "*" never crosses a separator, so a single-star pattern cannot
		// reach into a subdirectory.
		{"*.md", "docs/README.md", false},
		{"src/**/*_test.go", "src/a/b/thing_test.go", true},
		{"src/**/*_test.go", "src/thing_test.go", true},
		{"src/**/*_test.go", "src/thing.go", false},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.target); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}

func parseOrFail(t *testing.T, yaml string) File {
	t.Helper()
	f, err := Parse([]byte(yaml), FileName)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

// Day one, and the correct starting state rather than a transitional one:
// with nothing declared, every change is referred.
func TestEmptyFileRefersEverything(t *testing.T) {
	d := Decide(Change{Paths: []string{"README.md"}}, File{})
	if !d.Referred {
		t.Fatalf("an empty exemption set must refer everything, got %+v", d)
	}
}

func TestMatchingExemptionPasses(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: readme-only
    reason: prose changes nothing executable
    paths: ["README.md"]
`)
	d := Decide(Change{Paths: []string{"README.md"}}, f)
	if d.Referred || d.Exemption != "readme-only" {
		t.Fatalf("expected an unattended pass under readme-only, got %+v", d)
	}
}

// All-or-nothing: an exemption that matched when *some* path matched would
// let an agent staple a README tweak onto a dangerous change and take the
// unattended path.
func TestAnUncoveredPathRefersTheWholeChange(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: readme-only
    reason: prose changes nothing executable
    paths: ["README.md"]
`)
	d := Decide(Change{Paths: []string{"README.md", "src/auth.go"}}, f)
	if !d.Referred {
		t.Fatal("a change with one uncovered path must be referred")
	}
	if len(d.Uncovered) != 1 || d.Uncovered[0] != "src/auth.go" {
		t.Errorf("the referral must name what stood in the way, got %v", d.Uncovered)
	}
}

// An exemption is a shape, not a set of blessed paths. The union of two
// declared shapes is a third shape nobody declared — and under the other
// reading, adding a narrow entry would silently widen every existing one.
func TestUnionOfTwoExemptionsIsStillReferred(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: readme-only
    reason: prose changes nothing executable
    paths: ["README.md"]
  - name: docs-only
    reason: design notes are not shipped
    paths: ["docs/**"]
`)
	d := Decide(Change{Paths: []string{"README.md", "docs/adr/0013.md"}}, f)
	if !d.Referred {
		t.Fatal("a change covered only by two exemptions between them must be referred")
	}
	// Every path is covered by something, so naming an uncovered path would
	// send the reader looking for one that is not there.
	if len(d.Uncovered) != 0 {
		t.Errorf("no single path is uncovered here, got %v", d.Uncovered)
	}
}

func TestDisqualifiersVetoAMatchingExemption(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: everything
    reason: deliberately wide, so the veto is what is under test
    paths: ["**"]
`)
	cases := []struct {
		name string
		ch   Change
		want string
	}{
		{
			"a net-new suppression",
			Change{Paths: []string{"src/a.go"}, AddedLines: []AddedLine{{Path: "src/a.go", Text: "\tx := run(c) // #nosec G204"}}},
			"suppression added",
		},
		{
			"a newly skipped test",
			Change{Paths: []string{"src/a_test.go"}, AddedLines: []AddedLine{{Path: "src/a_test.go", Text: "\tt.Skip(\"flaky\")"}}},
			"test disabled",
		},
		{
			// describe.only silently stops every other test in the file from
			// running, and the suite still reports passes.
			"a focused test that disables its neighbours",
			Change{Paths: []string{"src/a.test.ts"}, AddedLines: []AddedLine{{Path: "src/a.test.ts", Text: "describe.only('auth', () => {"}}},
			"test disabled",
		},
		{
			"an edit to lydite's own config",
			Change{Paths: []string{".lydite.yml"}},
			"lydite config edited",
		},
		{
			"an edit to the exemption set itself",
			Change{Paths: []string{FileName}},
			"lydite config edited",
		},
		{
			"an edit to CI",
			Change{Paths: []string{".github/workflows/ci.yml"}},
			"CI workflow edited",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.ch, f)
			if !d.Referred {
				t.Fatalf("expected a referral, got %+v", d)
			}
			// The exemption still matched — "matched, then vetoed" and
			// "matched nothing" have different remedies, so the report has to
			// be able to tell them apart.
			if d.Exemption != "everything" {
				t.Errorf("the matching exemption must still be named, got %q", d.Exemption)
			}
			found := false
			for _, dq := range d.Disqualifications {
				if dq.Kind == tc.want {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a %q disqualification, got %+v", tc.want, d.Disqualifications)
			}
		})
	}
}

// The built-in vetoes are not expressible as absent: a repo's own list only
// adds. A veto list that can be emptied is not a floor.
func TestDeclaredDisqualifiersOnlyAdd(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: everything
    reason: deliberately wide
    paths: ["**"]
disqualifiers:
  paths: ["infra/**"]
`)
	if d := Decide(Change{Paths: []string{".github/workflows/ci.yml"}}, f); !d.Referred {
		t.Error("declaring a disqualifier list must not displace the built-in vetoes")
	}
	if d := Decide(Change{Paths: []string{"infra/main.tf"}}, f); !d.Referred {
		t.Error("a declared disqualifying path must veto a match")
	}
}

// A change touching nothing cannot be dangerous and has nothing for a human
// to read. Leaving it to the matching loop would make the verdict depend on
// whether the file happens to be empty, since every exemption covers it
// vacuously.
func TestAnEmptyChangeIsNotReferred(t *testing.T) {
	if d := Decide(Change{}, File{}); d.Referred || !d.Empty {
		t.Errorf("an empty change must not be referred, got %+v", d)
	}
}

// An unknown key is an error rather than being dropped. If a future lydite
// grows a condition field and an older binary ignores it, the exemption
// widens to whatever it says without the field — nobody edited the file and
// nobody reviewed a change.
func TestUnknownKeysAreRejected(t *testing.T) {
	_, err := Parse([]byte(`
exemptions:
  - name: readme-only
    reason: prose
    paths: ["README.md"]
    unless_sca_dirty: true
`), FileName)
	if err == nil {
		t.Fatal("an unrecognised key must be an error, not silently ignored")
	}
}

func TestValidationRequiresNameReasonAndPaths(t *testing.T) {
	cases := map[string]string{
		"a missing name":   "exemptions:\n  - reason: r\n    paths: [\"a\"]\n",
		"a missing reason": "exemptions:\n  - name: n\n    paths: [\"a\"]\n",
		// An exemption with no patterns covers only a change that touches
		// nothing, so it can never fire — a dead entry in the one file that
		// is supposed to state completely what merges unread.
		"no path patterns": "exemptions:\n  - name: n\n    reason: r\n    paths: []\n",
		"a duplicate name": "exemptions:\n  - name: n\n    reason: r\n    paths: [\"a\"]\n  - name: n\n    reason: r\n    paths: [\"b\"]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc), FileName); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}
}

func TestEmptyDocumentParsesToTheDayOneState(t *testing.T) {
	f, err := Parse(nil, FileName)
	if err != nil {
		t.Fatalf("an empty file is the day-one state, not an error: %v", err)
	}
	if len(f.Exemptions) != 0 {
		t.Errorf("got %+v, want no exemptions", f.Exemptions)
	}
}

// A rename contributes both of its paths. Counting only the destination
// would let a file be moved out of an exempt tree — or into one — while the
// exemption still matched.
func TestRenameContributesBothPaths(t *testing.T) {
	got, err := parseNameStatus("R100\tdocs/old.md\tsrc/new.go\nM\tREADME.md\n")
	if err != nil {
		t.Fatalf("parseNameStatus: %v", err)
	}
	want := map[string]bool{"docs/old.md": true, "src/new.go": true, "README.md": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want the three paths in %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected path %q", p)
		}
	}
}

func TestParseAddedLinesIgnoresRemovalsAndHeaders(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/src/a.go b/src/a.go",
		"--- a/src/a.go",
		"+++ b/src/a.go",
		"@@ -1,0 +2 @@",
		"+\tadded line",
		"-\tremoved line",
	}, "\n")
	got := parseAddedLines(patch)
	if len(got) != 1 || got[0].Path != "src/a.go" || got[0].Text != "\tadded line" {
		t.Fatalf("got %+v, want the single added line in src/a.go", got)
	}
}

// A disqualifier that fires on ordinary code is worse than one that
// occasionally misses: readers learn to ignore the tag, and it stops working
// for the cases it exists for. "xit(" sits inside "os.Exit(", so a bare
// substring search reports every Go file that exits a process as disabling a
// test.
func TestTokensMatchWholeWordsOnly(t *testing.T) {
	clean := []string{
		"\tos.Exit(exit.Code)",
		"\treturn unit.only(x)",
		"\tresult := context.Skip(v)",
	}
	for _, line := range clean {
		d := Disqualifications(Change{
			Paths:      []string{"src/a.go"},
			AddedLines: []AddedLine{{Path: "src/a.go", Text: line}},
		}, Disqualifiers{})
		if len(d) != 0 {
			t.Errorf("%q must not disqualify, got %+v", line, d)
		}
	}
	dirty := map[string]string{
		"\txit('broken', () => {})":    "test disabled",
		"\tt.Skip(\"flaky\")":          "test disabled",
		"\tx := run(c) // #nosec G204": "suppression added",
	}
	for line, want := range dirty {
		d := Disqualifications(Change{
			Paths:      []string{"src/a.go"},
			AddedLines: []AddedLine{{Path: "src/a.go", Text: line}},
		}, Disqualifiers{})
		if len(d) != 1 || d[0].Kind != want {
			t.Errorf("%q: got %+v, want one %q", line, d, want)
		}
	}
}

// One row per kind per file. A file that adds forty suppressions has one
// problem, and forty rows saying so would bury the verdict.
func TestDisqualificationsCollapsePerFile(t *testing.T) {
	d := Disqualifications(Change{
		Paths: []string{"src/a.go"},
		AddedLines: []AddedLine{
			{Path: "src/a.go", Text: "a() // #nosec"},
			{Path: "src/a.go", Text: "b() // #nosec"},
			{Path: "src/a.go", Text: "c() // nosemgrep"},
			{Path: "src/b.go", Text: "d() // #nosec"},
		},
	}, Disqualifiers{})
	if len(d) != 2 {
		t.Fatalf("got %d rows, want one per file: %+v", len(d), d)
	}
}
