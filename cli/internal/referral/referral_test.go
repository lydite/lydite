package referral

import (
	"strings"
	"testing"

	"lydite/lydite/internal/config"
)

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
			Change{Paths: []string{"src/a.go"}, Added: []DiffLine{{Path: "src/a.go", Text: "\tx := run(c) // #nosec G204"}}},
			"suppression added",
		},
		{
			"a newly skipped test",
			Change{Paths: []string{"src/a_test.go"}, Added: []DiffLine{{Path: "src/a_test.go", Text: "\tt.Skip(\"flaky\")"}}},
			"test disabled",
		},
		{
			// describe.only silently stops every other test in the file from
			// running, and the suite still reports passes.
			"a focused test that disables its neighbours",
			Change{Paths: []string{"src/a.test.ts"}, Added: []DiffLine{{Path: "src/a.test.ts", Text: "describe.only('auth', () => {"}}},
			"test disabled",
		},
		{
			"an edit to lydite's own config",
			Change{Paths: []string{config.FileName}},
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
	got, _, _, err := parseNameStatus("R100\tdocs/old.md\tsrc/new.go\nM\tREADME.md\n")
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

func TestParseDiffLinesSeparatesAdditionsFromRemovals(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/src/a.go b/src/a.go",
		"--- a/src/a.go",
		"+++ b/src/a.go",
		"@@ -1,0 +2 @@",
		"+\tadded line",
		"-\tremoved line",
	}, "\n")
	got, removed, err := parseDiffLines(patch)
	if err != nil {
		t.Fatalf("parseDiffLines: %v", err)
	}
	if len(got) != 1 || got[0].Path != "src/a.go" || got[0].Text != "\tadded line" {
		t.Fatalf("got %+v, want the single added line in src/a.go", got)
	}
	if len(removed) != 1 || removed[0].Text != "\tremoved line" {
		t.Fatalf("got %+v, want the single removed line", removed)
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
			Paths: []string{"src/a.go"},
			Added: []DiffLine{{Path: "src/a.go", Text: line}},
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
			Paths: []string{"src/a.go"},
			Added: []DiffLine{{Path: "src/a.go", Text: line}},
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
		Added: []DiffLine{
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

// An added source line whose own text is "++ /dev/null" renders in the patch
// as "+++ /dev/null". Read by prefix alone it looks like a file header, which
// blanks the current file and drops every added line after it — so a
// suppression placed below one is never scanned. Content can only appear
// inside a hunk, which is what makes the two distinguishable.
func TestAddedContentCannotForgeAFileHeader(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/evil.js b/evil.js",
		"--- /dev/null",
		"+++ b/evil.js",
		"@@ -0,0 +1,3 @@",
		"+/*",
		"++ /dev/null",
		"+*/",
		"+const a = 1; // eslint-disable-line",
	}, "\n")
	added, _, err := parseDiffLines(patch)
	if err != nil {
		t.Fatalf("parseDiffLines: %v", err)
	}
	for _, l := range added {
		if l.Path != "evil.js" {
			t.Fatalf("a content line changed the current file: %+v", added)
		}
	}
	if len(added) != 4 {
		t.Fatalf("got %d added lines, want 4: %+v", len(added), added)
	}
	d := Disqualifications(Change{Paths: []string{"evil.js"}, Added: added}, Disqualifiers{})
	if len(d) != 1 || d[0].Kind != "suppression added" {
		t.Errorf("the suppression below the forged header must still be seen, got %+v", d)
	}
}

// A line whose content begins with "++" renders as "+++x;" and is real
// content, not a header.
func TestAddedLineBeginningWithPlusPlusIsContent(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/a.js b/a.js",
		"--- a/a.js",
		"+++ b/a.js",
		"@@ -1 +1 @@",
		"++x; // eslint-disable-line",
	}, "\n")
	added, _, err := parseDiffLines(patch)
	if err != nil {
		t.Fatalf("parseDiffLines: %v", err)
	}
	if len(added) != 1 || added[0].Text != "+x; // eslint-disable-line" {
		t.Fatalf("got %+v, want the ++x line kept as content", added)
	}
}

// git pads a "+++ b/<path>" header with a tab when the path contains a
// space, so the raw remainder of the header is not the path.
func TestFileHeaderPathDropsGitsPadding(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/my file.txt b/my file.txt",
		"--- a/my file.txt",
		"+++ b/my file.txt\t",
		"@@ -1 +1 @@",
		"+x // #nosec",
	}, "\n")
	added, _, err := parseDiffLines(patch)
	if err != nil {
		t.Fatalf("parseDiffLines: %v", err)
	}
	if len(added) != 1 || added[0].Path != "my file.txt" {
		t.Fatalf("got %+v, want path \"my file.txt\" with no trailing tab", added)
	}
}

// The whole-file and whole-crate suppression forms are strictly more
// powerful than the per-line ones, so catching only the narrow form would
// veto the small suppression and wave the large one through.
func TestBroadSuppressionFormsDisqualify(t *testing.T) {
	cases := map[string]string{
		"#![allow(clippy::all)]":       "suppression added",
		"#[expect(dead_code)]":         "suppression added",
		"#![expect(dead_code)]":        "suppression added",
		"// @ts-nocheck":               "suppression added",
		"func f() { //nolint:errcheck": "suppression added",
		"//go:build ignore":            "test disabled",
		"// +build ignore":             "test disabled",
	}
	for line, want := range cases {
		d := Disqualifications(Change{
			Paths: []string{"src/a"},
			Added: []DiffLine{{Path: "src/a", Text: line}},
		}, Disqualifiers{})
		if len(d) != 1 || d[0].Kind != want {
			t.Errorf("%q: got %+v, want one %q", line, d, want)
		}
	}
}

// A change that edits .gitattributes is changing how git renders the very
// diff this package reads about it.
func TestGitAttributesEditDisqualifies(t *testing.T) {
	d := Disqualifications(Change{Paths: []string{".gitattributes"}}, Disqualifiers{})
	if len(d) != 1 || d[0].Kind != "diff rendering edited" {
		t.Fatalf("got %+v, want a diff-rendering veto", d)
	}
}

// Deleting a test is how a check stops failing without anything being fixed
// — the same "made a verdict go away" evidence a suppression is.
func TestRemovedTestsDisqualify(t *testing.T) {
	deleted := Disqualifications(Change{
		Paths:   []string{"src/foo_test.go"},
		Deleted: []string{"src/foo_test.go"},
	}, Disqualifiers{})
	if len(deleted) != 1 || deleted[0].Kind != "tests removed" {
		t.Errorf("deleting a test file must veto, got %+v", deleted)
	}

	gutted := Disqualifications(Change{
		Paths:   []string{"src/foo_test.go"},
		Removed: []DiffLine{{Path: "src/foo_test.go", Text: "func TestThing(t *testing.T) {"}},
	}, Disqualifiers{})
	if len(gutted) != 1 || gutted[0].Kind != "tests removed" {
		t.Errorf("removing a test declaration must veto, got %+v", gutted)
	}

	// A removed line in ordinary source is not a test being deleted, and
	// `test(` or `describe(` there can mean anything.
	source := Disqualifications(Change{
		Paths:   []string{"src/app.ts"},
		Removed: []DiffLine{{Path: "src/app.ts", Text: "  test(value)"}},
	}, Disqualifiers{})
	if len(source) != 0 {
		t.Errorf("a removal in non-test source must not veto, got %+v", source)
	}
}

// Patterns are repository-root-relative, so a repo scanned with --dir source
// declares "source/README.md". Matching them against the scan-root-relative
// form would leave every exemption silently dead in a monorepo.
func TestPatternsAreRepositoryRootRelative(t *testing.T) {
	f := parseOrFail(t, `
exemptions:
  - name: readme-only
    reason: prose changes nothing executable
    paths: ["source/README.md"]
`)
	if d := Decide(Change{Paths: []string{"source/README.md"}}, f); d.Referred {
		t.Errorf("a repository-root-relative pattern must match the path git reports, got %+v", d)
	}
}

// A pattern the matcher cannot parse is rejected when the file loads. An
// exemption that silently matches nothing is one nobody notices is broken.
func TestMalformedPatternIsRejected(t *testing.T) {
	if _, err := Parse([]byte("exemptions:\n  - name: n\n    reason: r\n    paths: [\"src/[unclosed\"]\n"), FileName); err == nil {
		t.Error("a malformed glob must be rejected at load time")
	}
	if _, err := Parse([]byte("disqualifiers:\n  paths: [\"src/[unclosed\"]\n"), FileName); err == nil {
		t.Error("a malformed disqualifier glob must be rejected at load time")
	}
}

// A patch line longer than the scanner's buffer stops the scan. Returning
// what was read so far would leave every content-based veto looking at an
// empty diff while Paths, read from a separate command, stays complete — a
// run that looks like a normal evaluation and found nothing. --text renders
// a binary blob or a minified bundle as exactly such a line, so this needs
// no attacker.
func TestAnUnreadableDiffIsAnErrorNotAnEmptyOne(t *testing.T) {
	huge := strings.Repeat("x", 11*1024*1024)
	patch := strings.Join([]string{
		"diff --git a/blob.bin b/blob.bin",
		"--- /dev/null",
		"+++ b/blob.bin",
		"@@ -0,0 +1 @@",
		"+" + huge,
		"diff --git a/a.go b/a.go",
		"--- a/a.go",
		"+++ b/a.go",
		"@@ -1 +1 @@",
		"+foo() //nolint:all",
	}, "\n")
	if _, _, err := parseDiffLines(patch); err == nil {
		t.Fatal("a diff the parser cannot read must be an error, not a silently empty result")
	}
}

// A rename takes a test out of the runner's view exactly as a deletion does,
// and leaves nothing in Deleted or Removed to notice.
func TestRenamingATestAwayDisqualifies(t *testing.T) {
	d := Disqualifications(Change{
		Paths:   []string{"src/foo_test.go", "src/foo_disabled.go"},
		Renamed: []Rename{{From: "src/foo_test.go", To: "src/foo_disabled.go"}},
	}, Disqualifiers{})
	if len(d) != 1 || d[0].Kind != "tests removed" {
		t.Fatalf("got %+v, want a tests-removed veto", d)
	}
	// Moving a test to another test path is reorganisation, not removal.
	moved := Disqualifications(Change{
		Paths:   []string{"src/foo_test.go", "tests/foo_test.go"},
		Renamed: []Rename{{From: "src/foo_test.go", To: "tests/foo_test.go"}},
	}, Disqualifiers{})
	if len(moved) != 0 {
		t.Errorf("moving a test between test paths must not veto, got %+v", moved)
	}
}

// A veto that fires on ordinary code teaches readers to ignore it, so a
// selector call is not a test declaration.
func TestSelectorCallsAreNotTestDeclarations(t *testing.T) {
	clean := Disqualifications(Change{
		Paths:   []string{"src/a.spec.ts"},
		Removed: []DiffLine{{Path: "src/a.spec.ts", Text: "  if (re.test(x)) {"}},
	}, Disqualifiers{})
	if len(clean) != 0 {
		t.Errorf("re.test(x) is a method call, not a test, got %+v", clean)
	}
	real := Disqualifications(Change{
		Paths:   []string{"src/a.spec.ts"},
		Removed: []DiffLine{{Path: "src/a.spec.ts", Text: "test('adds', () => {"}},
	}, Disqualifiers{})
	if len(real) != 1 || real[0].Kind != "tests removed" {
		t.Errorf("a removed test declaration must veto, got %+v", real)
	}
}

// Renames come off --name-status, whose rename records carry both paths and
// a similarity-suffixed status letter.
func TestParseNameStatusCollectsRenamesAndDeletions(t *testing.T) {
	_, deleted, renamed, err := parseNameStatus("R100\tsrc/a_test.go\tsrc/a.go\nD\tsrc/b_test.go\nM\tREADME.md\n")
	if err != nil {
		t.Fatalf("parseNameStatus: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "src/b_test.go" {
		t.Errorf("deleted = %v, want [src/b_test.go]", deleted)
	}
	if len(renamed) != 1 || renamed[0] != (Rename{From: "src/a_test.go", To: "src/a.go"}) {
		t.Errorf("renamed = %+v, want one src/a_test.go → src/a.go", renamed)
	}
}

// A change that edits the exemption set must edit nothing else. The attack
// this closes is not a forged exemption but an unremarkable one riding along
// in a large change approved for its other contents — after which the
// widening is permanent and nobody read it.
func TestBundledExemptionChangeIsReported(t *testing.T) {
	d := Decide(Change{Paths: []string{FileName, "src/app.go"}}, File{})
	if len(d.Bundled) != 1 || d.Bundled[0] != "src/app.go" {
		t.Fatalf("Bundled = %v, want the path riding along", d.Bundled)
	}

	// Alone, it is the isolated change the rule asks for — still referred,
	// because editing it is a disqualifier, but not a failure.
	alone := Decide(Change{Paths: []string{FileName}}, File{})
	if len(alone.Bundled) != 0 {
		t.Errorf("an isolated exemption change must not be reported as bundled, got %v", alone.Bundled)
	}
	if !alone.Referred {
		t.Error("editing the exemption set is still a disqualifier")
	}

	// The ordinary config file carries no isolation requirement: report
	// paths change alongside code for honest reasons.
	bundled := Decide(Change{Paths: []string{config.FileName, "src/app.go"}}, File{})
	if len(bundled.Bundled) != 0 {
		t.Errorf("%s must carry no isolation requirement, got %v", config.FileName, bundled.Bundled)
	}
}

// A bare base-name test would read any config.yml or exemptions.yml anywhere
// in the tree as the files that decide what lydite checks and what merges
// unattended.
func TestLyditeConfigMatchingIsNotByBaseName(t *testing.T) {
	for _, p := range []string{"src/config.yml", "deploy/exemptions.yml", "config.yml"} {
		if InConfigDir(p) {
			t.Errorf("%s is not lydite configuration", p)
		}
		if IsExemptionsPath(p) {
			t.Errorf("%s is not the exemption set", p)
		}
		if d := Decide(Change{Paths: []string{p}}, File{}); len(d.Bundled) != 0 {
			t.Errorf("%s must carry no isolation requirement, got %v", p, d.Bundled)
		}
	}
}

// Every file under .lydite/ configures lydite, so an edit to the component
// declaration is a disqualifier exactly as an edit to the config is: it
// changes what gets tested.
func TestEveryFileUnderTheConfigDirIsADisqualifier(t *testing.T) {
	for _, p := range []string{config.Dir + "/components.yml", config.FileName, FileName, "source/" + config.Dir + "/config.yml"} {
		d := Decide(Change{Paths: []string{p}}, File{Exemptions: []Exemption{{Name: "everything", Reason: "r", Paths: []string{"**"}}}})
		if !d.Referred {
			t.Errorf("%s must be a disqualifier", p)
		}
	}
}

// The referral diff covers the whole repository while lydite may scan a
// subdirectory of it, so a monorepo's own declaration still governs its scan
// root.
func TestExemptionsPathIsRecognisedUnderAScanRoot(t *testing.T) {
	if !IsExemptionsPath("source/" + FileName) {
		t.Errorf("source/%s is an exemption set", FileName)
	}
	d := Decide(Change{Paths: []string{"source/" + FileName, "src/app.go"}}, File{})
	if len(d.Bundled) != 1 || d.Bundled[0] != "src/app.go" {
		t.Errorf("bundled = %v, want the non-exemption path", d.Bundled)
	}
}
