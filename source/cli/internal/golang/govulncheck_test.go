package golang

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
)

// vulnDir is the module the captured report names, materialised.
func vulnDir(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, "testdata/vulnprobe")
}

// govulncheckFixture is govulncheck's real `-format json` stream over a module
// pinned to a vulnerable dependency, captured from the pinned tool.
func govulncheckFixture(t *testing.T) []govulncheckMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/govulncheck.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return decodeGovulncheck(strings.NewReader(string(data)))
}

func TestGovulncheckReportsOneClaimPerAdvisory(t *testing.T) {
	messages := govulncheckFixture(t)
	var findingMessages int
	for _, m := range messages {
		if m.Finding != nil {
			findingMessages++
		}
	}
	if findingMessages != 17 {
		t.Fatalf("fixture holds %d finding messages, want the 17 govulncheck emits", findingMessages)
	}

	got := govulncheckFindings(vulnDir(t), messages)
	if len(got) != 11 {
		t.Fatalf("got %d claims from %d messages, want 11 — govulncheck emits several messages per advisory at increasing trace depth, and they are one claim cleared by one bump", len(got), findingMessages)
	}
	seen := map[string]bool{}
	for _, f := range got {
		if seen[f.Rule] {
			t.Errorf("%s reported twice", f.Rule)
		}
		seen[f.Rule] = true
	}
}

func TestGovulncheckReportsOneAdvisoryAgainstTwoModulesTwice(t *testing.T) {
	// One record routinely names the standard library and the golang.org/x
	// module the same code is vendored from — 18 of the advisories in this
	// package's own fixture do. When both are in the build they are two claims,
	// cleared by two different bumps, on two different manifest lines, at two
	// different fixed versions.
	messages := []govulncheckMessage{
		{Finding: &govulncheckFinding{
			OSV: "GO-2022-0236", FixedVersion: "v1.26.6",
			Trace: []govulncheckFrame{{Module: "stdlib", Version: "v1.26.5"}},
		}},
		{Finding: &govulncheckFinding{
			OSV: "GO-2022-0236", FixedVersion: "v0.7.0",
			Trace: []govulncheckFrame{
				{Module: "golang.org/x/net", Version: "v0.6.0", Package: "golang.org/x/net/http2", Function: "Serve"},
				{Module: "example.com/m", Package: "example.com/m", Function: "main"},
			},
		}},
	}
	got := govulncheckFindings(t.TempDir(), messages)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2 — one advisory against two modules is two bumps", len(got))
	}
	modules := map[string]string{}
	for _, f := range got {
		modules[f.Site] = f.Message
	}
	if len(modules) != 2 {
		t.Fatalf("the two claims share a site: %v", modules)
	}
	var stdlib, x string
	for site, message := range modules {
		if strings.Contains(site, "stdlib") {
			stdlib = message
		} else {
			x = message
		}
	}
	if !strings.Contains(stdlib, "v1.26.6") {
		t.Errorf("the stdlib claim = %q, want its own fixed version", stdlib)
	}
	if !strings.Contains(x, "golang.org/x/net") || !strings.Contains(x, "v0.7.0") {
		t.Errorf("the x/net claim = %q, want its own module and fixed version", x)
	}
}

func TestGovulncheckKeepsTheRichestTrace(t *testing.T) {
	// The depth-one messages say only that the module is in the build. The
	// deepest one says how the code actually reaches the vulnerability, which
	// is what a reader needs in order to judge it.
	got := govulncheckFindings(vulnDir(t), govulncheckFixture(t))
	var called []string
	for _, f := range got {
		if f.Rule == "GO-2020-0036" {
			called = f.Detail
		}
	}
	if len(called) != 2 {
		t.Fatalf("GO-2020-0036 detail = %q, want the two-frame call path", called)
	}
	// Read outwards from the scanned module's own code, which is where a
	// reader starts.
	if !strings.Contains(called[0], "vulnprobe.main") || !strings.Contains(called[0], "main.go:11") {
		t.Errorf("first detail line = %q, want the scanned module's own call site", called[0])
	}
	if !strings.Contains(called[1], "yaml.v2.Unmarshal") {
		t.Errorf("second detail line = %q, want the vulnerable symbol", called[1])
	}
}

func TestGovulncheckLocatesAnAdvisoryAtTheManifestLine(t *testing.T) {
	got := govulncheckFindings(vulnDir(t), govulncheckFixture(t))
	mod := readGoMod(vulnDir(t))
	var dependency, stdlib int
	for _, f := range got {
		if f.Path != goModFile {
			t.Errorf("%s located in %q, want %s — the one edit that clears an advisory is the bump", f.Rule, f.Path, goModFile)
		}
		switch f.Rule {
		case "GO-2020-0036":
			dependency = f.Line
		case "GO-2026-6088":
			stdlib = f.Line
		}
	}
	if want := mod.Line("gopkg.in/yaml.v2"); dependency != want || want == 0 {
		t.Errorf("the yaml advisory is on line %d, want %d — the require naming it", dependency, want)
	}
	// A standard-library advisory is cleared by a toolchain bump, and that is
	// the line the bump is written on.
	if want := mod.Line("stdlib"); stdlib != want || want == 0 {
		t.Errorf("the stdlib advisory is on line %d, want %d — the go directive", stdlib, want)
	}
}

func TestGovulncheckIdentifiesAnAdvisoryByItsIDAndTheModule(t *testing.T) {
	// A manifest line names a module and cannot tell two advisories against
	// one module apart.
	got := govulncheckFindings(vulnDir(t), govulncheckFixture(t))
	sites := map[string]bool{}
	for _, f := range got {
		if sites[f.Site] {
			t.Errorf("site %q is shared, so two advisories are one claim", f.Site)
		}
		sites[f.Site] = true
		if !strings.HasPrefix(f.Site, f.Rule) {
			t.Errorf("site = %q, want it to begin with the advisory id %q", f.Site, f.Rule)
		}
	}
}

func TestGovulncheckMessageNamesTheModuleAndTheFix(t *testing.T) {
	got := govulncheckFindings(vulnDir(t), govulncheckFixture(t))
	for _, f := range got {
		if !strings.Contains(f.Message, "fixed in") {
			t.Errorf("%s message = %q, want the version that clears it", f.Rule, f.Message)
		}
	}
}

func TestGoModFindsAModuleAtTheLineThatDecidesItsVersion(t *testing.T) {
	dir := t.TempDir()
	manifest := "module example.com/m\n" + // 1
		"\n" + // 2
		"go 1.24\n" + // 3
		"\n" + // 4
		"require single.example/one v1.0.0\n" + // 5
		"\n" + // 6
		"require (\n" + // 7
		"\tblock.example/two v2.0.0\n" + // 8
		"\tblock.example/three v3.0.0 // indirect\n" + // 9
		")\n" + // 10
		"\n" + // 11
		"replace block.example/two => fork.example/two v9.0.0\n" + // 12
		"\n" + // 13
		"replace (\n" + // 14
		"\tblock.example/three => fork.example/three v8.0.0\n" + // 15
		"\tlocal.example/five => ../five\n" + // 16
		")\n" + // 17
		"\n" + // 18
		"exclude excluded.example/six v6.0.0\n" // 19
	if err := os.WriteFile(dir+"/"+goModFile, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	mod := readGoMod(dir)
	cases := []struct {
		module string
		want   int
	}{
		{"stdlib", 3},
		{"single.example/one", 5},
		{"block.example/two", 8},
		// A `// indirect` comment annotates the entry and is not part of it.
		{"block.example/three", 9},
		// A module the manifest never names is reached transitively, and is
		// reported as having no line rather than guessed onto one.
		{"absent.example/four", 0},
		// A replace names the module the build actually resolves, which is
		// the one govulncheck reports — and the require it displaces names a
		// different path entirely, so nothing else in the manifest locates it.
		{"fork.example/two", 12},
		{"fork.example/three", 15},
		// A replace onto a local path names no module and has no version to
		// bump; the edit that clears such an advisory is to the replacement's
		// own tree.
		{"local.example/five", 0},
		{"../five", 0},
		// An exclude says a version is *not* used. Anchoring an advisory there
		// would point an author at the one edit that cannot clear it.
		{"excluded.example/six", 0},
	}
	for _, tc := range cases {
		if got := mod.Line(tc.module); got != tc.want {
			t.Errorf("Line(%q) = %d, want %d", tc.module, got, tc.want)
		}
	}
}

func TestGoModAnswersZeroWhenItCannotBeRead(t *testing.T) {
	mod := readGoMod(t.TempDir())
	if got := mod.Line("anything"); got != 0 {
		t.Errorf("Line = %d, want 0", got)
	}
	if got := mod.Line("stdlib"); got != 0 {
		t.Errorf("stdlib line = %d, want 0", got)
	}
}

func TestDecodeGovulncheckKeepsWhatParsedBeforeATruncatedStream(t *testing.T) {
	// A truncated stream is a run that was killed, and the advisories it did
	// report are true.
	stream := `{"finding":{"osv":"GO-1","trace":[{"module":"m"}]}}` + "\n" + `{"finding":{"osv":`
	if got := decodeGovulncheck(strings.NewReader(stream)); len(got) != 1 {
		t.Fatalf("got %d messages, want the 1 that parsed", len(got))
	}
}

func TestGovulncheckArgvKeepsTheVerdictOnTheTextPass(t *testing.T) {
	// Under -format json govulncheck exits 0 whether or not it found
	// anything, while the text run exits 3. The text pass therefore carries
	// no format flag and is the one that decides the row; a flag added here,
	// or the second pass dropped, silently turns the gate off.
	text := govulncheckArgv(false)
	if slices.Contains(text, "-format") {
		t.Errorf("text pass = %q, want no format flag — its exit status is the verdict", text)
	}
	if got := strings.Join(govulncheckArgv(true), " "); got != "-format json ./..." {
		t.Errorf("data pass = %q", got)
	}
}

func TestVulnerableModuleIsTheTracesFirstFrame(t *testing.T) {
	// The first frame is the vulnerable module; later frames walk back
	// towards the scanned code. A finding with no trace at all is the
	// standard library, which is how a toolchain advisory with no traced call
	// arrives.
	withTrace := &govulncheckFinding{Trace: []govulncheckFrame{{Module: "gopkg.in/yaml.v2"}, {Module: "example.com/m"}}}
	if got := vulnerableModule(withTrace); got != "gopkg.in/yaml.v2" {
		t.Errorf("vulnerableModule = %q, want the first frame's module", got)
	}
	if got := vulnerableModule(&govulncheckFinding{}); got != "stdlib" {
		t.Errorf("vulnerableModule = %q, want stdlib for a finding with no trace", got)
	}
}

func TestGovulncheckNumbersTwoAdvisoriesAlikeInPathAndSite(t *testing.T) {
	// Two findings for one advisory against one module reach here when
	// govulncheck reports it at two trace depths and the collapse keeps both
	// — which it does not — but the ordinal is what the fingerprint relies on
	// if it ever did. Numbering is not optional because the site is shared.
	same := &govulncheckFinding{OSV: "GO-2020-0036", Trace: []govulncheckFrame{{Module: "m"}}}
	got := govulncheckFindings(t.TempDir(), []govulncheckMessage{{Finding: same}})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if got[0].Ordinal != 0 {
		t.Errorf("ordinal = %d, want 0 for the only claim at its site", got[0].Ordinal)
	}
	// A second module under the same advisory is a second claim, and both are
	// numbered from the same pass.
	two := []govulncheckMessage{
		{Finding: &govulncheckFinding{OSV: "GO-1", Trace: []govulncheckFrame{{Module: "m"}}}},
		{Finding: &govulncheckFinding{OSV: "GO-1", Trace: []govulncheckFrame{{Module: "n"}}}},
	}
	pair := govulncheckFindings(t.TempDir(), two)
	if len(pair) != 2 || pair[0].Fingerprint() == pair[1].Fingerprint() {
		t.Errorf("got %d claims with shared fingerprints, want 2 distinct", len(pair))
	}
}

func TestGovulncheckKeepsTheFirstTraceWhenTwoAreEquallyDeep(t *testing.T) {
	// The richer trace wins, and "richer" is strictly deeper: two traces of
	// one depth are not an improvement on each other, so the first is kept
	// and the result does not depend on arrival order.
	messages := []govulncheckMessage{
		{Finding: &govulncheckFinding{OSV: "GO-1", FixedVersion: "v1", Trace: []govulncheckFrame{{Module: "m", Function: "First"}}}},
		{Finding: &govulncheckFinding{OSV: "GO-1", FixedVersion: "v2", Trace: []govulncheckFrame{{Module: "m", Function: "Second"}}}},
	}
	got := govulncheckFindings(t.TempDir(), messages)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if !strings.Contains(got[0].Message, "v1") {
		t.Errorf("message = %q, want the first of two equally deep traces kept", got[0].Message)
	}
}

func TestGovulncheckMessageWithoutASummaryOrAVersion(t *testing.T) {
	// Every part of the claim line is optional at the source, and each
	// absence has to read as a sentence rather than as a gap: no advisory
	// record, no version on the frame, no fixed version.
	bare := &govulncheckFinding{OSV: "GO-1", Trace: []govulncheckFrame{{Module: "m"}}}
	got := govulncheckMessageText("GO-1", "m", bare, nil)
	if got != "GO-1 in m" {
		t.Errorf("message = %q, want the id and the module alone", got)
	}
	// An advisory record with an empty summary is the same as none.
	empty := govulncheckMessageText("GO-1", "m", bare, &govulncheckOSV{ID: "GO-1"})
	if empty != "GO-1 in m" {
		t.Errorf("message = %q, want the id when the summary is empty", empty)
	}
	// A finding with no trace at all names no version.
	none := govulncheckMessageText("GO-1", "stdlib", &govulncheckFinding{OSV: "GO-1"}, nil)
	if none != "GO-1 in stdlib" {
		t.Errorf("message = %q, want no version for a finding with no trace", none)
	}
	full := govulncheckMessageText("GO-1", "m",
		&govulncheckFinding{OSV: "GO-1", FixedVersion: "v2", Trace: []govulncheckFrame{{Module: "m", Version: "v1"}}},
		&govulncheckOSV{ID: "GO-1", Summary: "a flaw"})
	if full != "a flaw in m v1 (fixed in v2)" {
		t.Errorf("message = %q", full)
	}
}

func TestGovulncheckTraceNamesAModuleWithNoVersion(t *testing.T) {
	// A frame naming only a module says the module is in the build and no
	// call into it was traced, which is the whole of what a depth-one finding
	// reports. Its version is stated when there is one and omitted when not.
	withVersion := govulncheckTrace(&govulncheckFinding{Trace: []govulncheckFrame{{Module: "m", Version: "v1"}}})
	if len(withVersion) != 1 || withVersion[0] != "m v1" {
		t.Errorf("trace = %q, want the module with its version", withVersion)
	}
	without := govulncheckTrace(&govulncheckFinding{Trace: []govulncheckFrame{{Module: "m"}}})
	if len(without) != 1 || without[0] != "m" {
		t.Errorf("trace = %q, want the module alone", without)
	}
}

func TestGovulncheckReadsAnAdvisorySummaryFromItsRecord(t *testing.T) {
	// The `osv` messages carry the prose the `finding` messages only
	// reference by id. Filing them under anything but that id leaves every
	// claim wearing its identifier where a sentence should be.
	messages := []govulncheckMessage{
		{OSV: &govulncheckOSV{ID: "GO-2020-0036", Summary: "a flaw in the parser"}},
		{Finding: &govulncheckFinding{OSV: "GO-2020-0036", Trace: []govulncheckFrame{{Module: "m"}}}},
	}
	got := govulncheckFindings(t.TempDir(), messages)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if !strings.HasPrefix(got[0].Message, "a flaw in the parser in m") {
		t.Errorf("message = %q, want the summary its record states", got[0].Message)
	}
}

func TestGovulncheckIgnoresAnAdvisoryRecordWithNoID(t *testing.T) {
	// An osv message with no id names nothing and must not become the record
	// every finding then reads its summary from.
	messages := []govulncheckMessage{
		{OSV: &govulncheckOSV{Summary: "unattributed"}},
		{Finding: &govulncheckFinding{OSV: "GO-1", Trace: []govulncheckFrame{{Module: "m"}}}},
	}
	got := govulncheckFindings(t.TempDir(), messages)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if strings.Contains(got[0].Message, "unattributed") {
		t.Errorf("message = %q, want the id rather than an unattributed summary", got[0].Message)
	}
}

func TestGovulncheckResultLeavesTheTextPassAlone(t *testing.T) {
	// The text pass decides the row: under -format json govulncheck exits 0
	// whether or not it found anything. An empty data stream must therefore
	// change neither the verdict nor the output.
	failing := executil.Result{Name: "govulncheck", Err: errors.New("exit status 3"), Output: "govulncheck said so"}
	got := govulncheckResult(failing, t.TempDir(), "")
	if got.Err == nil || got.Output != "govulncheck said so" {
		t.Errorf("an empty stream changed the verdict: %+v", got)
	}
	if len(got.Findings) != 0 {
		t.Errorf("an empty stream produced %d claims", len(got.Findings))
	}

	// A stream that parses adds claims and still leaves the verdict alone.
	stream := `{"finding":{"osv":"GO-1","fixed_version":"v2","trace":[{"module":"m"}]}}`
	withClaims := govulncheckResult(failing, t.TempDir(), stream)
	if len(withClaims.Findings) != 1 {
		t.Fatalf("got %d claims, want 1", len(withClaims.Findings))
	}
	if withClaims.Err == nil {
		t.Error("parsing the data pass cleared the text pass's failure")
	}
}
