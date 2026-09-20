package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/gitdiff"
)

// captured is one of the reports the pinned gitleaks wrote over the tree beside
// it, with that tree materialised.
//
// The probe files are committed with a `.txt` suffix and written back under
// their real names, as every fixture reproducing a finding here is: they hold
// invented credential-shaped strings, and lydite's own scan reads its testdata.
func captured(t *testing.T, name string) (dir string, rep report) {
	t.Helper()
	probes := map[string]string{
		"gitleaks":         "glprobe",
		"gitleaks-shifted": "glprobe-shifted",
		"gitleaks-ignored": "glprobe-ignored",
	}
	probe, ok := probes[name]
	if !ok {
		t.Fatalf("no probe tree for %s", name)
	}
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return fixture.Tree(t, filepath.Join("testdata", probe)), rep
}

func TestReadsTheRealReport(t *testing.T) {
	dir, rep := captured(t, "gitleaks")
	got, unplaced := findings(dir, rep, unscoped)
	if len(unplaced) != 0 {
		t.Errorf("unplaced = %q, want none — every leak in the capture names a file and a line", unplaced)
	}
	if len(got) != 5 {
		t.Fatalf("got %d claims, want the capture's 5", len(got))
	}
	// Source order, whatever order gitleaks reported in.
	type where struct {
		path string
		line int
	}
	want := []where{{"app.py", 2}, {"app.py", 3}, {"app.py", 4}, {"config.yml", 3}, {"config.yml", 4}}
	for i, w := range want {
		if got[i].Path != w.path || got[i].Line != w.line {
			t.Errorf("claim %d = %s:%d, want %s:%d", i, got[i].Path, got[i].Line, w.path, w.line)
		}
	}
	f := got[0]
	if f.Gate != "gitleaks" {
		t.Errorf("gate = %q, want gitleaks", f.Gate)
	}
	if f.Component != "" {
		t.Errorf("component = %q, want none — this gate is root-scoped", f.Component)
	}
	if f.Rule != "generic-api-key" {
		t.Errorf("rule = %q, want gitleaks' own rule id", f.Rule)
	}
	if f.EndLine != 0 {
		t.Errorf("EndLine = %d, want none for a claim about one line", f.EndLine)
	}
	if len(f.Detail) != 0 {
		t.Errorf("Detail = %q, want none — everything gitleaks would put there is a field already or is the secret", f.Detail)
	}
}

func TestEveryMessageSaysToRotate(t *testing.T) {
	// The author of a pull request reads Message and nothing else. gitleaks'
	// description ends at what it found, and an author who deletes the line has
	// fixed nothing.
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep, unscoped)
	for _, f := range got {
		if !strings.HasSuffix(f.Message, rotate) {
			t.Errorf("message = %q, want it to end with the instruction to rotate", f.Message)
		}
		if !strings.HasPrefix(f.Message, "Detected a Generic API Key") {
			t.Errorf("message = %q, want gitleaks' own description first", f.Message)
		}
	}
}

func TestMessageNormalisesTheDescriptionsFullStop(t *testing.T) {
	cases := []struct {
		name string
		leak leak
		want string
	}{
		{"a description already ending in one", leak{RuleID: "r", Description: "Detected a key."}, "Detected a key. " + rotate},
		{"a description ending without one", leak{RuleID: "r", Description: "Detected a key"}, "Detected a key. " + rotate},
		{"a description ending in several", leak{RuleID: "r", Description: "Detected a key..."}, "Detected a key. " + rotate},
		{"surrounding space", leak{RuleID: "r", Description: "  Detected a key.  "}, "Detected a key. " + rotate},
		{"no description names the rule", leak{RuleID: "some-rule"}, "gitleaks matched some-rule. " + rotate},
		{"no description and no rule", leak{}, "gitleaks matched an unnamed rule. " + rotate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := message(tc.leak); got != tc.want {
				t.Errorf("message = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSiteIsTheLinePrefixAndNeverTheSecret(t *testing.T) {
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep, unscoped)

	// Every invented credential in the probe tree, read back out of the
	// materialised files: none of them may appear in a site, which travels in
	// scan.json and into a comment on a public pull request.
	for _, f := range got {
		for _, secret := range secretsIn(t, filepath.Join(dir, filepath.FromSlash(f.Path))) {
			if strings.Contains(f.Site, secret) {
				t.Errorf("site for %s:%d quotes the secret it found", f.Path, f.Line)
			}
		}
	}

	want := map[string]string{
		// `    connect(secret_key="…"` — the assignment context, which is what
		// tells two secrets in one file apart.
		"app.py:2": site("generic-api-key", "connect("),
		// `    token = "…"` twice: the match begins at the assignment, so the
		// prefix is the indentation alone and normalises away. The ordinal is
		// what keeps these two claims.
		"app.py:3": site("generic-api-key", ""),
		"app.py:4": site("generic-api-key", ""),
		// `aws_key: "…"` — the match begins the line, so the site is the rule
		// alone.
		"config.yml:3": site("generic-api-key", ""),
		// `clé_api: "…"` — the cut lands after a two-byte rune and keeps it
		// whole.
		"config.yml:4": site("generic-api-key", "clé"),
	}
	for _, f := range got {
		key := f.Path + ":" + strconv.Itoa(f.Line)
		if f.Site != want[key] {
			t.Errorf("site for %s = %q, want %q", key, f.Site, want[key])
		}
	}
}

func TestASiteNeverIncludesAnotherMatchOnTheSameLine(t *testing.T) {
	// A line can hold more than one match — a chained assignment, a DSN
	// followed by a token, two -e flags on one docker run line — and gitleaks
	// reports each as its own leak. Cutting a claim at its own StartColumn
	// alone would let an earlier match's secret ride along in a later
	// claim's site, published in scan.json and into a comment on a public
	// pull request.
	dir, _ := captured(t, "gitleaks")
	rep := report{
		// A rule matching right at the start of the line.
		{RuleID: "rule-a", File: "config.yml", StartLine: 3, StartColumn: 2},
		// A second rule matching deep inside the same line — with only its
		// own column applied, its site would carry "aws_key" and part of the
		// hex string that is rule-a's match.
		{RuleID: "rule-b", File: "config.yml", StartLine: 3, StartColumn: 40},
	}
	got, unplaced := findings(dir, rep, unscoped)
	if len(unplaced) != 0 {
		t.Fatalf("unplaced = %v, want none", unplaced)
	}
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	for _, f := range got {
		if f.Site != site(f.Rule, "") {
			t.Errorf("site for %s = %q, want %q — cut at the earliest match on the line", f.Rule, f.Site, site(f.Rule, ""))
		}
	}
}

func TestOrdinalSeparatesTwoClaimsSharingASite(t *testing.T) {
	// Two secrets under one rule with no distinguishing prefix in one file are
	// ordinals 0 and 1 — the cost ADR 0035 names and accepts. Without the
	// ordinal they are one claim and the second is dropped as a duplicate.
	dir, rep := captured(t, "gitleaks")
	got, _ := findings(dir, rep, unscoped)
	var sharing []finding.Finding
	for _, f := range got {
		if f.Path == "app.py" && f.Site == site("generic-api-key", "") {
			sharing = append(sharing, f)
		}
	}
	if len(sharing) != 2 {
		t.Fatalf("got %d claims sharing a site, want the capture's 2", len(sharing))
	}
	if sharing[0].Ordinal != 0 || sharing[1].Ordinal != 1 {
		t.Errorf("ordinals = %d and %d, want 0 and 1 in source order", sharing[0].Ordinal, sharing[1].Ordinal)
	}
	if sharing[0].Fingerprint() == sharing[1].Fingerprint() {
		t.Error("both claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestASiteSurvivesAnEditAboveIt(t *testing.T) {
	// The same tree with two lines inserted above every match, scanned by the
	// same gitleaks: every claim moves and none is re-identified.
	dir, rep := captured(t, "gitleaks")
	before, _ := findings(dir, rep, unscoped)
	shiftedDir, shiftedRep := captured(t, "gitleaks-shifted")
	after, _ := findings(shiftedDir, shiftedRep, unscoped)

	if len(before) != len(after) {
		t.Fatalf("got %d claims before the edit and %d after", len(before), len(after))
	}
	for i := range before {
		if before[i].Line == after[i].Line {
			t.Errorf("claim %d did not move: the fixture proves nothing", i)
		}
		if before[i].Fingerprint() != after[i].Fingerprint() {
			t.Errorf("claim %d at %s:%d was re-identified by an edit above it: site %q became %q",
				i, before[i].Path, before[i].Line, before[i].Site, after[i].Site)
		}
	}
}

func TestSourceOrderIsIndependentOfTheReportsOrder(t *testing.T) {
	// gitleaks scans files concurrently and its report's order varies between
	// runs. Ordinals are counted in the order given, so an unsorted parser hands
	// two claims sharing a site each other's identity from one run to the next.
	dir, rep := captured(t, "gitleaks")
	forwards, _ := findings(dir, rep, unscoped)
	slices.Reverse(rep)
	backwards, _ := findings(dir, rep, unscoped)

	for i := range forwards {
		if forwards[i].Line != backwards[i].Line || forwards[i].Path != backwards[i].Path {
			t.Fatalf("claim %d = %s:%d read forwards and %s:%d read backwards",
				i, forwards[i].Path, forwards[i].Line, backwards[i].Path, backwards[i].Line)
		}
		if forwards[i].Fingerprint() != backwards[i].Fingerprint() {
			t.Errorf("claim %d at %s:%d is identified differently depending on the report's order",
				i, forwards[i].Path, forwards[i].Line)
		}
	}
}

func TestInSourceOrderBreaksATieOnColumn(t *testing.T) {
	// Two leaks sharing a file and a line are only ordered by column. Given
	// them in descending column order, source order — ascending — is the one
	// thing that distinguishes "sorted" from "left as given".
	rep := report{
		{RuleID: "b", File: "a.py", StartLine: 1, StartColumn: 40},
		{RuleID: "a", File: "a.py", StartLine: 1, StartColumn: 10},
	}
	got := inSourceOrder(rep)
	if got[0].RuleID != "a" || got[1].RuleID != "b" {
		t.Errorf("order = %s, %s, want a, b (ascending column)", got[0].RuleID, got[1].RuleID)
	}
}

func TestALeakThatNamesNoLineIsReportedRatherThanDropped(t *testing.T) {
	// A parser that silently discards input is how a gate quietly stops working,
	// and the discarded thing here is a credential somebody committed.
	cases := []struct {
		name string
		leak leak
	}{
		{"no file", leak{RuleID: "generic-api-key", StartLine: 3}},
		{"line zero", leak{RuleID: "generic-api-key", File: "config.yml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unplaced := findings(t.TempDir(), report{tc.leak}, unscoped)
			if len(got) != 0 {
				t.Errorf("got %d claims, want none — neither a thread nor an identity can be made from this", len(got))
			}
			if len(unplaced) != 1 {
				t.Fatalf("unplaced = %q, want the leak named", unplaced)
			}
			if !strings.Contains(unplaced[0], "generic-api-key") {
				t.Errorf("unplaced = %q, want the rule named", unplaced[0])
			}
		})
	}
}

func TestAClaimOnTheFirstLineIsKept(t *testing.T) {
	// Line 1 is a real line; a boundary excluding it would drop every claim on
	// the first line of every file.
	dir, _ := captured(t, "gitleaks")
	got, unplaced := findings(dir, report{{RuleID: "r", File: "config.yml", StartLine: 1, StartColumn: 2}}, unscoped)
	if len(got) != 1 || len(unplaced) != 0 {
		t.Fatalf("got %d claims and %d unplaced, want 1 and 0", len(got), len(unplaced))
	}
}

func TestEarliestColumnPerLineKeepsTheFirstLine(t *testing.T) {
	// A boundary excluding line 1 would drop the earliest-column entry for
	// every match on a file's first line, leaving its site cut at column 0
	// (the map's zero value) rather than the column gitleaks reported.
	cols := earliestColumnPerLine(newScanRoot(t.TempDir()), report{{File: "a.py", StartLine: 1, StartColumn: 10}})
	if got, ok := cols[lineKey{"a.py", 1}]; !ok || got != 10 {
		t.Errorf("cols[a.py:1] = %d, %v, want 10, true", got, ok)
	}
	for _, l := range []int{0, -1} {
		cols := earliestColumnPerLine(newScanRoot(t.TempDir()), report{{File: "a.py", StartLine: l, StartColumn: 10}})
		if _, ok := cols[lineKey{"a.py", l}]; ok {
			t.Errorf("a leak naming line %d entered the map, which has no line to be a key for", l)
		}
	}
}

func TestAPathIsNamedFromTheScanRoot(t *testing.T) {
	// gitleaks names a file the way its target named it: `./config.yml` under
	// some inputs, `config.yml` under others, and an absolute path when the
	// walk was given one. One file named several ways is several claims, only
	// one of which can be anchored — and only the root-relative form is the
	// shape git's own answer is compared against.
	dir, _ := captured(t, "gitleaks")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	cases := []struct {
		name string
		file string
	}{
		{"relative", "config.yml"},
		{"relative with a leading dot", "./config.yml"},
		// darwin's temporary directories live under a symlinked /var, so the
		// root lydite was given and the path a walk of it reports are the same
		// file under two names.
		{"absolute as the root was given", filepath.Join(dir, "config.yml")},
		{"absolute with the root's symlinks resolved", filepath.Join(resolved, "config.yml")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, unplaced := findings(dir, report{{RuleID: "r", File: tc.file, StartLine: 3, StartColumn: 2}}, unscoped)
			if len(got) != 1 || len(unplaced) != 0 {
				t.Fatalf("got %d claims and %d unplaced, want 1 and 0", len(got), len(unplaced))
			}
			if got[0].Path != "config.yml" {
				t.Errorf("path = %q, want it named from the scan root", got[0].Path)
			}
		})
	}
}

func TestARootThatWillNotResolveStillPlacesTheRelativePaths(t *testing.T) {
	// The root is a path lydite was handed and a directory the run does not
	// own: it can be replaced or removed under the scan. A root whose symlinks
	// cannot be resolved still names the shape every relative path in the
	// report is already in, and only a site read from the tree is lost.
	gone := filepath.Join(t.TempDir(), "removed-under-the-run")
	got, unplaced := findings(gone, report{{RuleID: "r", File: "./config.yml", StartLine: 3, StartColumn: 2}}, unscoped)
	if len(got) != 1 || len(unplaced) != 0 {
		t.Fatalf("got %d claims and %d unplaced, want 1 and 0", len(got), len(unplaced))
	}
	if got[0].Path != "config.yml" {
		t.Errorf("path = %q, want it named from the scan root", got[0].Path)
	}
}

func TestAPathOutsideTheScanRootIsReportedRatherThanPlaced(t *testing.T) {
	// A path lydite cannot reduce to the scan root can be compared against
	// neither git's answer nor the tree a site is read from. Naming it anyway
	// would file a claim under a path nothing anchors; dropping it in silence
	// would lose a credential somebody committed.
	dir, _ := captured(t, "gitleaks")
	for _, file := range []string{"../outside.yml", filepath.Join(filepath.Dir(dir), "sibling", "config.yml"), string(filepath.Separator)} {
		got, unplaced := findings(dir, report{{RuleID: "generic-api-key", File: file, StartLine: 3, StartColumn: 2}}, unscoped)
		if len(got) != 0 {
			t.Errorf("%s: got %d claims, want none", file, len(got))
		}
		if len(unplaced) != 1 {
			t.Fatalf("%s: unplaced = %q, want the leak named", file, unplaced)
		}
		if !strings.Contains(unplaced[0], "generic-api-key") {
			t.Errorf("%s: unplaced = %q, want the rule named", file, unplaced[0])
		}
	}
}

func TestASpanIsKeptOnlyWhenItIsOne(t *testing.T) {
	dir, _ := captured(t, "gitleaks")
	cases := []struct {
		name             string
		start, end, want int
	}{
		{"end equals start is one line", 3, 3, 0},
		{"no end stated", 3, 0, 0},
		{"a real span is kept", 3, 7, 7},
		{"an end before the start is not a span", 7, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := findings(dir, report{{RuleID: "r", File: "config.yml", StartLine: tc.start, EndLine: tc.end, StartColumn: 2}}, unscoped)
			if len(got) != 1 {
				t.Fatalf("got %d claims, want 1", len(got))
			}
			if got[0].EndLine != tc.want {
				t.Errorf("EndLine = %d, want %d", got[0].EndLine, tc.want)
			}
		})
	}
}

func TestPrefixEndStopsBeforeTheMatchAndOnARuneBoundary(t *testing.T) {
	// gitleaks reports a start column one byte past the match's first byte, so a
	// match beginning its line reads 2 and yields no prefix at all. Being before
	// the match is the whole of what keeps the secret out of the site.
	const line = `clé_api: "x"` // c l é(two bytes) _ a p i …
	cases := []struct {
		name string
		line string
		col  int
		want int
	}{
		{"a match beginning the line", line, 2, 0},
		{"a column below anything gitleaks reports", line, 1, 0},
		{"a column of zero", line, 0, 0},
		{"a negative column", line, -5, 0},
		{"one byte in", line, 3, 1},
		{"the cut landing after a two-byte rune", line, 6, 4},
		{"the cut landing inside a two-byte rune", line, 5, 2},
		{"the cut at the line's end", line, len(line) + 2, len(line)},
		{"a column past the line's end", line, len(line) + 99, len(line)},
		{"an empty line", "", 40, 0},
		// A file lydite does not own need not hold valid UTF-8, and a line
		// beginning with a continuation byte has no rune boundary to walk back
		// to. The cut is the line's start, not an index before it.
		{"a line that begins mid-rune", "\x80key = \"x\"", 2, 0},
		{"a cut walking back to the line's start", "\x80\x80\x80key", 4, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixEnd(tc.line, tc.col)
			if got != tc.want {
				t.Fatalf("prefixEnd(%q, %d) = %d, want %d", tc.line, tc.col, got, tc.want)
			}
			// Whatever the column says, the cut is a whole prefix of the line.
			if strings.ContainsRune(tc.line[:got], '�') {
				t.Errorf("prefixEnd(%q, %d) = %d, which splits a rune", tc.line, tc.col, got)
			}
		})
	}
}

func TestAPrefixIsEmptyForALineItCannotRead(t *testing.T) {
	// Empty is the right answer rather than an error: the caller is building an
	// identity, and the gate has already reported what it found.
	dir, _ := captured(t, "gitleaks")
	src := newTree(dir)
	cases := []struct {
		name string
		path string
		line int
	}{
		{"a file that is not there", "nothing.yml", 1},
		{"a path climbing out of the tree", "../outside.yml", 1},
		{"a line past the end", "config.yml", 9999},
		{"line zero", "config.yml", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := src.prefix(tc.path, tc.line, 40); got != "" {
				t.Errorf("prefix = %q, want empty", got)
			}
		})
	}
	// The line that is there still reads, so the cases above prove something.
	if got := src.prefix("config.yml", 4, 6); got != "clé" {
		t.Errorf("prefix = %q, want clé", got)
	}
	// A root that cannot be opened answers empty for everything rather than
	// failing the run.
	if got := newTree(filepath.Join(dir, "not-a-directory")).prefix("config.yml", 4, 6); got != "" {
		t.Errorf("prefix = %q, want empty from a tree that could not be opened", got)
	}
}

func TestAPrefixReadsTheLastLineOfAFileWithNoTrailingNewline(t *testing.T) {
	// A file with no trailing newline splits into exactly as many elements as
	// it has lines, so its last line is at n == len(lines) — the one value a
	// boundary of ">=" instead of ">" would refuse to read, even though it
	// names a real line the file has.
	dir := t.TempDir()
	write(t, dir, "a.py", "first\nsecond")
	if got := newTree(dir).prefix("a.py", 2, 4); got != "se" {
		t.Errorf("prefix = %q, want %q — the last line, present with no trailing newline", got, "se")
	}
}

func TestASiteDropsAnEarlierAssignmentEvenWhenGitleaksDidNotFlagIt(t *testing.T) {
	// Cutting at the earliest *flagged* match on a line only protects flagged
	// matches from each other. A chained export, two docker -e flags or a DSN
	// can carry a second, unflagged credential earlier on the same line —
	// gitleaks' own entropy or pattern rules simply never fired on it — and
	// that value is exactly as much a secret as the one gitleaks found.
	// Length and character-class are not trusted to bound it: a password can
	// be eight characters or three, plain or full of punctuation inside
	// quotes — so any of these three cases blanks the whole prefix.
	cases := []struct {
		name   string
		line   string
		secret string
		match  string
	}{
		{"a long unquoted password", "export DB_PASSWORD=hunter2secret API_TOKEN=abcdef1234567890", "hunter2secret", "API_TOKEN"},
		{"a quoted password full of punctuation", `export DB_PASSWORD='P@ssw0rd!' API_TOKEN=abcdef1234567890`, "P@ssw0rd!", "API_TOKEN"},
		{"a password shorter than eight characters", "export DB_PASSWORD=abc API_TOKEN=abcdef1234567890", "abc", "API_TOKEN"},
		{"a DSN password ahead of a flagged token", "postgres://app:changeme@db/app?api_key=abcdef1234567890", "changeme", "api_key"},
		{"a command-line password flag ahead of a flagged token", "mysql -u root -phunter2 --api-key=abcdef1234567890", "hunter2", "api-key"},
		{"a command-line password flag using its own switch", "redis-cli -a hunter2secret --token=abcdef1234567890", "hunter2secret", "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "env.sh", tc.line+"\n")
			// The match starts at tc.match: one past its first byte, per
			// gitleaks' own StartColumn convention.
			col := strings.Index(tc.line, tc.match) + 2
			got, unplaced := findings(dir, report{{RuleID: "generic-api-key", File: "env.sh", StartLine: 1, StartColumn: col}}, unscoped)
			if len(unplaced) != 0 || len(got) != 1 {
				t.Fatalf("got %d claims and %d unplaced, want 1 and 0", len(got), len(unplaced))
			}
			if strings.Contains(got[0].Site, tc.secret) {
				t.Errorf("site = %q, must not carry the earlier, unflagged secret %q", got[0].Site, tc.secret)
			}
		})
	}
}

func TestAPrefixIsBoundedAndNormalised(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("é", maxSiteRunes+50) + `key="secret"`
	write(t, dir, "bundle.min.js", "  spaced \t out  key\n"+long+"\n")
	src := newTree(dir)

	// A multi-word prefix is exactly the shape a command-line flag or a
	// second assignment takes — "mysql -u root -phunter2 " is letters,
	// digits and spaces too — so safePrefix admits only a single identifier
	// and blanks anything with more than one word in it.
	if got := src.prefix("bundle.min.js", 1, 999); got != "" {
		t.Errorf("prefix = %q, want empty — more than one word is not a safe prefix", got)
	}
	// A minified bundle is one line of megabytes, and a site is uploaded in the
	// report document.
	got := src.prefix("bundle.min.js", 2, 4*maxSiteRunes)
	if n := len([]rune(got)); n != maxSiteRunes {
		t.Errorf("prefix is %d runes, want it clipped to %d", n, maxSiteRunes)
	}
}

func TestAFileOverTheReadCapIdentifiesNothing(t *testing.T) {
	// A file read part-way is a Site identifying a claim by text the file does
	// not hold, so an oversized one yields no prefix rather than a truncated
	// one.
	dir := t.TempDir()
	line := "prefix_key = \"x\"\n"
	write(t, dir, "at-the-cap.txt", line+strings.Repeat("f", maxFileBytes-len(line)))
	write(t, dir, "over-the-cap.txt", line+strings.Repeat("f", maxFileBytes-len(line)+1))
	src := newTree(dir)
	if got := src.prefix("at-the-cap.txt", 1, 8); got != "prefix" {
		t.Errorf("prefix = %q, want a file exactly at the cap read whole", got)
	}
	if got := src.prefix("over-the-cap.txt", 1, 8); got != "" {
		t.Errorf("prefix = %q, want none from a file over the cap", got)
	}
}

func TestAReportThatWillNotParseIsNotAReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	for _, data := range []string{"", "not json at all", `[{"RuleID": `, `{"leaks":[]}`} {
		write(t, dir, "report.json", data)
		if _, err := readReport(path); err == nil {
			t.Errorf("readReport(%q) reported success", data)
		}
	}
	write(t, dir, "report.json", `[]`)
	if _, err := readReport(path); err != nil {
		t.Errorf("a report that does parse was rejected: %v", err)
	}
	if _, err := readReport(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("a report that was never written read as one")
	}
}

func TestAReportNothingCouldReadIsNotAPass(t *testing.T) {
	// The claims decide the row, so a run whose claims lydite never got is a
	// gate that could not run — and a gate that could not run renders as a
	// pass nowhere, least of all beside a clean exit, where it is
	// indistinguishable from a tree with no secret in it.
	dir := t.TempDir()
	absent := filepath.Join(dir, "no-report.json")
	corrupt := filepath.Join(dir, "corrupt.json")
	write(t, dir, "corrupt.json", `[{"RuleID": "generic-api-key"`)

	cases := []struct {
		name string
		path string
		in   executil.Result
	}{
		{"no report beside a failing run", absent, executil.Result{Name: Gate, Err: exitedWith(t, 1)}},
		{"no report beside a clean run", absent, executil.Result{Name: Gate}},
		{"a corrupt report beside a failing run", corrupt, executil.Result{Name: Gate, Err: exitedWith(t, 1)}},
		{"a corrupt report beside a clean run", corrupt, executil.Result{Name: Gate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := result(tc.in, dir, tc.path, unscoped, nil)
			if got.Ok() {
				t.Error("Ok() = true for a gate with no report to decide on")
			}
			if !strings.Contains(got.Detail, "could not read the report") {
				t.Errorf("Detail = %q, want the reason the gate has nothing to decide on", got.Detail)
			}
			if len(got.Findings) != 0 {
				t.Errorf("Findings = %d, want none from a report nothing could read", len(got.Findings))
			}
		})
	}
}

func TestGitleaksFailingForItsOwnReasonIsNotAPass(t *testing.T) {
	// gitleaks exits 1 for a directory it could not walk exactly as it does for
	// a leak, and writes the same empty report a clean run writes. A row
	// derived from the claims that survived would read that as a scanned tree
	// with nothing in it.
	dir := t.TempDir()
	write(t, dir, "report.json", `[]`)
	path := filepath.Join(dir, "report.json")

	cases := []struct {
		name string
		exit error
	}{
		{"the leaks status with no leak in the report", exitedWith(t, 1)},
		{"a status that is not the leaks one", exitedWith(t, 126)},
		{"a failure carrying no status at all", errors.New("exec: \"gitleaks\": executable file not found in $PATH")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := result(executil.Result{Name: Gate, Err: tc.exit}, dir, path, unscoped, nil)
			if got.Ok() {
				t.Error("Ok() = true for a gitleaks that did not finish its walk")
			}
			if !strings.Contains(got.Detail, "may not have been walked whole") {
				t.Errorf("Detail = %q, want the reason the walk is not a statement about the tree", got.Detail)
			}
		})
	}
}

func TestALeakInAFileGitWouldCarryFailsTheRow(t *testing.T) {
	// The whole capture: leaks in a tracked file and in an untracked one
	// .gitignore does not cover, beside one in the build output. The first two
	// survive the filter, so there is something left to fail on.
	dir, rep := captured(t, "gitleaks-ignored")
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshalling the capture: %v", err)
	}
	write(t, dir, "report.json", string(data))

	leaked := exitedWith(t, 1)
	got := result(executil.Result{Name: Gate, Err: leaked}, dir, filepath.Join(dir, "report.json"), carriedBy(t, dir), nil)
	if len(got.Findings) != 2 {
		t.Errorf("Findings = %d, want the two leaks in files git would carry", len(got.Findings))
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want none — gitleaks prints its own findings", got.Detail)
	}
	if !errors.Is(got.Err, leaked) {
		t.Errorf("Err = %v, want gitleaks' own failure kept for the row it is the whole story of", got.Err)
	}
}

func TestACleanRunSaysNothingOfItsOwn(t *testing.T) {
	// gitleaks prints its own findings, so Detail carries only what lydite has
	// to say — and over a clean tree that is nothing. A row that failed here
	// would fail every scan of a repository with no secrets in it.
	dir := t.TempDir()
	write(t, dir, "report.json", `[]`)
	got := result(executil.Result{Name: Gate}, dir, filepath.Join(dir, "report.json"), unscoped, nil)
	if !got.Ok() {
		t.Errorf("Ok() = false on a clean run: %v", got.Err)
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want none", got.Detail)
	}
	if len(got.Findings) != 0 {
		t.Errorf("Findings = %d, want none", len(got.Findings))
	}
}

func TestResultSaysWhatItCouldNotLocate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "report.json", `[{"RuleID":"generic-api-key","StartLine":3}]`)
	path := filepath.Join(dir, "report.json")

	clean := result(executil.Result{Name: Gate}, dir, path, unscoped, nil)
	if clean.Ok() {
		t.Error("a leak reported beside a clean exit read as a pass")
	}
	if !strings.Contains(clean.Detail, "generic-api-key") {
		t.Errorf("Detail = %q, want the leak lydite could not place", clean.Detail)
	}

	failing := result(executil.Result{Name: Gate, Err: exitedWith(t, 1)}, dir, path, unscoped, nil)
	if failing.Ok() {
		t.Error("a leak lydite could not locate read as a pass beside gitleaks' own failure")
	}
	if !strings.Contains(failing.Detail, "generic-api-key") {
		t.Errorf("Detail = %q, want the unplaced leak said whatever the status", failing.Detail)
	}
}

func TestOnlyAFileGitWouldCarryIsClaimed(t *testing.T) {
	// The capture is the pinned gitleaks over a tree with a warm build
	// directory in it: one leak in a tracked file, one in an untracked file
	// .gitignore does not cover, and one in the compiled output git will not
	// carry. The third is not a file this gate has a claim over.
	dir, rep := captured(t, "gitleaks-ignored")
	got, unplaced := findings(dir, rep, carriedBy(t, dir))
	if len(unplaced) != 0 {
		t.Errorf("unplaced = %q, want none — every leak in the capture names a file and a line", unplaced)
	}
	want := []string{"app.py", "draft.py"}
	var paths []string
	for _, f := range got {
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, want) {
		t.Errorf("claims = %q, want %q — the tracked file and the untracked one .gitignore does not cover", paths, want)
	}
}

func TestAReportOfNothingButIgnoredOutputIsAPass(t *testing.T) {
	// gitleaks exits 1 for a leak anywhere it walked, so a repository with a
	// warm target/ fails a gate whose every claim was filtered away. The row
	// has nothing left to fail on.
	dir, rep := captured(t, "gitleaks-ignored")
	var ignoredOnly report
	for _, l := range rep {
		if strings.HasPrefix(l.File, "target/") {
			ignoredOnly = append(ignoredOnly, l)
		}
	}
	if len(ignoredOnly) == 0 {
		t.Fatal("the capture names no leak in ignored output, so this test proves nothing")
	}
	data, err := json.Marshal(ignoredOnly)
	if err != nil {
		t.Fatalf("marshalling the capture: %v", err)
	}
	write(t, dir, "report.json", string(data))

	got := result(executil.Result{Name: Gate, Err: exitedWith(t, 1)}, dir, filepath.Join(dir, "report.json"), carriedBy(t, dir), nil)
	if !got.Ok() {
		t.Errorf("Ok() = false: %v", got.Err)
	}
	if len(got.Findings) != 0 {
		t.Errorf("Findings = %d, want none", len(got.Findings))
	}
	if got.Detail != "" {
		t.Errorf("Detail = %q, want none", got.Detail)
	}
}

func TestAScopeGitCouldNotAnswerForIsNotACleanScan(t *testing.T) {
	// A tree git will not answer for is a gate that could not be scoped, and a
	// row that passed there reads exactly like one scoped to what git carries
	// and clean. The claims are kept — losing them is the one cost worse than
	// a red row — and the row says which of the two it is.
	dir, rep := captured(t, "gitleaks-ignored")
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshalling the capture: %v", err)
	}
	write(t, dir, "report.json", string(data))
	path := filepath.Join(dir, "report.json")

	clean := result(executil.Result{Name: Gate}, dir, path, nil, gitdiff.ErrNoRepository)
	if clean.Ok() {
		t.Error("a scope git could not answer for read as a pass")
	}
	if !errors.Is(clean.Err, gitdiff.ErrNoRepository) {
		t.Errorf("Err = %v, want git's own reason kept", clean.Err)
	}
	if !strings.Contains(clean.Detail, gitdiff.ErrNoRepository.Error()) {
		t.Errorf("Detail = %q, want the reason git could not be asked", clean.Detail)
	}
	if len(clean.Findings) != len(rep) {
		t.Errorf("Findings = %d, want the capture's %d — an unscoped run reports every leak", len(clean.Findings), len(rep))
	}

	// A run that leaked as well as going unscoped fails on the scope: the leaks
	// gitleaks printed are on the terminal either way, and the scope is the
	// half of the row a reader cannot see anywhere else.
	failing := result(executil.Result{Name: Gate, Err: exitedWith(t, 1)}, dir, path, nil, gitdiff.ErrNoRepository)
	if !errors.Is(failing.Err, gitdiff.ErrNoRepository) {
		t.Errorf("Err = %v, want the reason the run went unscoped", failing.Err)
	}

	// A report nothing could read leaves the scope unsaid nowhere either.
	missing := result(executil.Result{Name: Gate}, dir, filepath.Join(dir, "no-report.json"), nil, gitdiff.ErrNoRepository)
	if missing.Ok() || !strings.Contains(missing.Detail, gitdiff.ErrNoRepository.Error()) {
		t.Errorf("Ok() = %v, Detail = %q, want a failing row naming why the scope is unknown", missing.Ok(), missing.Detail)
	}
}

func TestTrackedNamesPathsTheWayAClaimDoes(t *testing.T) {
	// gitdiff.Tracked answers relative to the root and slash-separated, which
	// is the shape a finding's Path carries. A mismatch there filters every
	// claim away and reports a clean scan of a leaking tree.
	dir, _ := captured(t, "gitleaks-ignored")
	initRepo(t, dir, ".gitignore", "app.py")
	keep, err := tracked(t.Context(), dir)
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	for _, p := range []string{"app.py", "draft.py"} {
		if !keep(p) {
			t.Errorf("%s is not carried, and git both tracks it or leaves it unignored", p)
		}
	}
	if keep("target/debug/deps/probe.rmeta") {
		t.Error("a path .gitignore covers is carried")
	}

	if _, err := tracked(t.Context(), t.TempDir()); !errors.Is(err, gitdiff.ErrNoRepository) {
		t.Errorf("err = %v, want ErrNoRepository from a tree git knows nothing about", err)
	}
}

func TestAScopeGitListsNothingForIsNoScope(t *testing.T) {
	// `git ls-files` run inside an ignored subtree lists nothing and exits
	// zero, which as a filter drops every claim and reports clean over a tree
	// nothing scoped. The empty answer is its own error, and the filter that
	// comes with it keeps everything rather than nothing.
	root := t.TempDir()
	write(t, root, ".gitignore", "vendored/\n")
	initRepo(t, root, ".gitignore")
	dir := filepath.Join(root, "vendored")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making the ignored subtree: %v", err)
	}
	write(t, dir, "app.py", "token = \"abcdef1234567890\"\n")

	keep, err := tracked(t.Context(), dir)
	if !errors.Is(err, errNoScope) {
		t.Fatalf("err = %v, want errNoScope from a root git lists nothing under", err)
	}
	if keep == nil || !keep("app.py") {
		t.Error("the filter that comes with an empty scope drops a path, which is the clean row it exists to prevent")
	}
}

func TestAnEmptyScopeCostsTheRowOnlyWhereItChangesTheAnswer(t *testing.T) {
	// A repository git lists nothing for and a gitleaks that found nothing
	// agree, so there is nothing for the row to be wrong about. A report
	// naming a leak is the case where the empty scope would filter every claim
	// away and call the tree clean.
	dir := t.TempDir()
	write(t, dir, "empty.json", `[]`)
	write(t, dir, "leaking.json", `[{"RuleID":"generic-api-key","File":"app.py","StartLine":1,"StartColumn":2}]`)

	clean := result(executil.Result{Name: Gate}, dir, filepath.Join(dir, "empty.json"), unscoped, errNoScope)
	if !clean.Ok() {
		t.Errorf("Ok() = false over a tree with no file and no leak in it: %v", clean.Err)
	}
	if clean.Detail != "" {
		t.Errorf("Detail = %q, want none", clean.Detail)
	}

	leaking := result(executil.Result{Name: Gate, Err: exitedWith(t, 1)}, dir, filepath.Join(dir, "leaking.json"), unscoped, errNoScope)
	if leaking.Ok() {
		t.Error("a leak reported against a scope git listed nothing for read as a pass")
	}
	if !errors.Is(leaking.Err, errNoScope) {
		t.Errorf("Err = %v, want the reason the run went unscoped", leaking.Err)
	}
	if !strings.Contains(leaking.Detail, errNoScope.Error()) {
		t.Errorf("Detail = %q, want the reason nothing could be scoped", leaking.Detail)
	}
	if len(leaking.Findings) != 1 {
		t.Errorf("Findings = %d, want the leak reported unscoped rather than filtered away", len(leaking.Findings))
	}
}

func TestALeakUnderANestedRepositoryIsInScope(t *testing.T) {
	// `git ls-files` names a submodule by its gitlink path alone and stops at
	// an embedded repository's directory, while gitleaks walks both as
	// ordinary source. Comparing the two answers as text drops every leak
	// underneath one in silence.
	dir := t.TempDir()
	write(t, dir, "app.py", "x = 1\n")
	initRepo(t, dir, "app.py")
	gitIn(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("0", 39)+"1,sub")
	if err := os.MkdirAll(filepath.Join(dir, "embedded"), 0o750); err != nil {
		t.Fatalf("making the embedded repository: %v", err)
	}
	gitIn(t, filepath.Join(dir, "embedded"), "init", "--quiet")
	write(t, filepath.Join(dir, "embedded"), "e.py", "token = \"abcdef1234567890\"\n")

	keep, err := tracked(t.Context(), dir)
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	for _, p := range []string{"sub/s.py", "sub/deep/s.py", "embedded/e.py"} {
		if !keep(p) {
			t.Errorf("%s is out of scope, and git's own answer says nothing about it either way", p)
		}
	}
	if keep("subterfuge.py") {
		t.Error("a path merely beginning with a nested repository's name is carried")
	}
}

func TestGitlinksReadsOnlyAnEntryGitWroteAsOne(t *testing.T) {
	// A prefix out of this list is one every claim beneath it is kept against,
	// so an entry not in the shape `git ls-files --stage` writes names nothing
	// to keep rather than whatever text happened to follow the mode.
	const object = "0000000000000000000000000000000000000000"
	cases := []struct {
		name   string
		output string
		want   []string
	}{
		{"a gitlink", gitlinkMode + " " + object + " 0\tsub", []string{"sub/"}},
		{"an ordinary file", "100644 " + object + " 0\tapp.py", nil},
		{"the split's trailing empty element", gitlinkMode + " " + object + " 0\tsub\x00", []string{"sub/"}},
		{"an entry with no path at all", gitlinkMode + " " + object + " 0", nil},
		{"an entry whose path is empty", gitlinkMode + " " + object + " 0\t", nil},
		{"no output", "", nil},
		{"two gitlinks", gitlinkMode + " " + object + " 0\tsub\x00" + gitlinkMode + " " + object + " 0\tvendor/dep", []string{"sub/", "vendor/dep/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitlinks(tc.output); !slices.Equal(got, tc.want) {
				t.Errorf("gitlinks = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNestedRepositoriesFailsWhereGitWillNotAnswer(t *testing.T) {
	// The prefixes are half the scope, and a half lydite did not get is not an
	// empty one: reporting no nested repository for a tree git refused to list
	// drops every claim under a submodule in silence.
	got, err := nestedRepositories(t.Context(), t.TempDir(), nil)
	if err == nil {
		t.Fatalf("nestedRepositories = %q, want the reason git could not be asked", got)
	}
	if !strings.Contains(err.Error(), "git ls-files --stage") {
		t.Errorf("err = %v, want the command that would not answer named", err)
	}
}

func TestAnAbsolutePathIsUnplaceableAgainstARootThatIsNot(t *testing.T) {
	// A root whose absolute form lydite never got is one no absolute path can
	// be related to. Empty is what says so, and the caller reports the leak as
	// one it could not place rather than filing it under a path nothing
	// anchors.
	root := scanRoot{dir: "relative-root", resolved: "relative-root"}
	if got := root.rel(filepath.Join(t.TempDir(), "config.yml")); got != "" {
		t.Errorf("rel = %q, want empty — an absolute path relates to no relative root", got)
	}
}

func TestALeakBesideACleanExitIsNotAPass(t *testing.T) {
	// gitleaks contradicting itself — a report naming a leak beside the status
	// of a tree with none — is a row the leak has to survive: the claim is in
	// the document, and a passing row beside it reads as a scanned, clean tree.
	dir := t.TempDir()
	write(t, dir, "app.py", "token = \"abcdef1234567890abcdef1234567890\"\n")
	write(t, dir, "report.json", `[{"RuleID":"generic-api-key","File":"app.py","StartLine":1,"StartColumn":9}]`)

	got := result(executil.Result{Name: Gate}, dir, filepath.Join(dir, "report.json"), unscoped, nil)
	if got.Ok() {
		t.Error("Ok() = true beside a report naming a leak")
	}
	if !strings.Contains(got.Err.Error(), "gitleaks reported a leak") {
		t.Errorf("Err = %v, want the leak named as the row's reason", got.Err)
	}
	if len(got.Findings) != 1 {
		t.Errorf("Findings = %d, want the leak the report names", len(got.Findings))
	}
}

// carriedBy is what git would carry out of the materialised probe tree, with
// the tree made a repository first: the .gitignore and one source file added,
// and the draft left untracked so the "--others --exclude-standard" half of
// the answer is exercised rather than assumed.
func carriedBy(t *testing.T, dir string) carried {
	t.Helper()
	initRepo(t, dir, ".gitignore", "app.py")
	keep, err := tracked(t.Context(), dir)
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	return keep
}

// exitedWith is a real *exec.ExitError carrying code, which is the shape
// executil.Run hands result for a gitleaks that exited non-zero.
//
// A plain error carries no status, and the status is what separates the leaks
// gitleaks reported from a walk it abandoned — a test asserting on the verdict
// with an invented error asserts on the statusless case whatever it says it is
// testing.
func exitedWith(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	var status *exec.ExitError
	if !errors.As(err, &status) {
		t.Fatalf("running a command that exits %d: %v", code, err)
	}
	return err
}

func initRepo(t *testing.T, dir string, add ...string) {
	t.Helper()
	gitIn(t, dir, "init", "--quiet")
	gitIn(t, dir, append([]string{"add", "--"}, add...)...)
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// secretsIn is every credential-shaped string in a probe file, so a test can
// assert that no site quotes one.
func secretsIn(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- a path this test just materialised
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var out []string
	for _, field := range strings.FieldsFunc(string(data), func(r rune) bool { return !isHex(r) }) {
		if len(field) >= 32 {
			out = append(out, field)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no credential-shaped string, so this test proves nothing", path)
	}
	return out
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}
