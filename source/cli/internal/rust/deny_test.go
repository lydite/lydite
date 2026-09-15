package rust

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/licence"
)

// denyFixture is cargo-deny's real NDJSON stream, captured from the pinned
// tool. It arrives on stderr rather than stdout, which is why runDeny reads
// Stderr.
func denyFixture(t *testing.T) []denyMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/deny.ndjson")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return decodeDeny(strings.NewReader(string(data)))
}

func TestDenyReadsTheRealStream(t *testing.T) {
	got := denyFindings(crateDir(t), denyFixture(t))
	if len(got) != 3 {
		t.Fatalf("got %d claims, want the 3 diagnostics in the stream", len(got))
	}
	for _, f := range got {
		if f.Gate != "cargo-deny" {
			t.Errorf("gate = %q, want cargo-deny", f.Gate)
		}
		if f.Path != cargoLockFile {
			t.Errorf("%s located in %q, want %s", f.Rule, f.Path, cargoLockFile)
		}
		if f.Rule == "" {
			t.Errorf("a claim with no code: %+v", f)
		}
	}
}

func TestDenyKeepsALogLineOutOfTheFindings(t *testing.T) {
	// The stream mixes `log` and `summary` lines in with the diagnostics, and
	// neither is a claim about the code.
	messages := append(denyFixture(t), denyMessage{Type: "log"}, denyMessage{Type: "summary"})
	if got := denyFindings(crateDir(t), messages); len(got) != 3 {
		t.Fatalf("got %d claims, want 3", len(got))
	}
}

func TestDenyReportsBothSeveritiesItStates(t *testing.T) {
	// cargo-deny reports a warning for a crate with no licence field and an
	// error for the same crate being unlicensed. Which of the two a policy
	// makes fatal is the policy's business; the row's verdict stays the
	// tool's exit status, and lydite reports every claim it made.
	got := denyFindings(crateDir(t), denyFixture(t))
	severities := map[string]bool{}
	for _, f := range got {
		severities[f.Severity] = true
	}
	if !severities["warning"] || !severities["error"] {
		t.Errorf("severities = %v, want both warning and error carried unchanged", severities)
	}
}

func TestDenyReportsADiagnosticWhoseCodeItDoesNotRecognise(t *testing.T) {
	got := denyFindings(crateDir(t), []denyMessage{{
		Type: "diagnostic",
		Fields: denyDiagnostic{
			Code:     "a-check-from-a-later-cargo-deny",
			Severity: "note",
			Message:  "something new",
			Graphs:   []denyGraph{{Krate: denyKrate{Name: "time", Version: "0.1.44"}}},
		},
	}})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1 — a parser that drops what it does not recognise is how a gate stops gating", len(got))
	}
}

func TestDenyReportsADiagnosticThatNamesNoCrate(t *testing.T) {
	// Many diagnostics carry no graph at all. The claim survives with no
	// line rather than being dropped or guessed onto one.
	got := denyFindings(crateDir(t), []denyMessage{{
		Type:   "diagnostic",
		Fields: denyDiagnostic{Code: "unlicensed", Severity: "error", Message: "no crate named here"},
	}})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if got[0].Line != 0 || got[0].Anchor != "" {
		t.Errorf("line = %d, anchor = %q, want 0 and unanchorable", got[0].Line, got[0].Anchor)
	}
}

func TestDenyReadsARealTransitiveDependencyPath(t *testing.T) {
	// The nested `parents` key is what makes a ban or a licence claim
	// actionable: the crate is rarely a direct dependency, and the edit that
	// clears it is to whichever dependent pulled it in. Every other test here
	// builds its graph by hand, which proves the walk and not the shape it is
	// given — so this one reads a real multi-hop graph, where a key named
	// wrongly yields a claim naming only the banned crate and nothing that
	// pulled it in. Casing alone would not: encoding/json falls back to a
	// case-insensitive match, so `Parents` reads `parents` perfectly well.
	data, err := os.ReadFile("testdata/deny-transitive.ndjson")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	got := denyFindings(crateDir(t), decodeDeny(strings.NewReader(string(data))))
	if len(got) != 1 {
		t.Fatalf("got %d claims, want the one banned crate", len(got))
	}
	if len(got[0].Detail) != 1 {
		t.Fatalf("detail = %q, want the one path that pulled it in", got[0].Detail)
	}
	// libc is banned, time depends on it, and the probe depends on time.
	for _, crate := range []string{"libc", "time 0.1.44", "auditprobe"} {
		if !strings.Contains(got[0].Detail[0], crate) {
			t.Errorf("detail = %q, want every hop named — missing %q", got[0].Detail[0], crate)
		}
	}
	if got[0].Rule != "banned" {
		t.Errorf("rule = %q, want banned", got[0].Rule)
	}
}

func TestDenyDetailIsTheDependencyPath(t *testing.T) {
	// The crate is rarely a direct dependency, and the edit that clears a ban
	// or a licence claim is to whichever dependent pulled it in.
	got := denyFindings(crateDir(t), []denyMessage{{
		Type: "diagnostic",
		Fields: denyDiagnostic{
			Code: "banned", Severity: "error", Message: "banned crate",
			Graphs: []denyGraph{{
				Krate:   denyKrate{Name: "leaf", Version: "1.0.0"},
				Parents: []denyGraph{{Krate: denyKrate{Name: "mid", Version: "2.0.0"}}},
			}},
		},
	}})
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if len(got[0].Detail) != 1 || !strings.Contains(got[0].Detail[0], "leaf 1.0.0") || !strings.Contains(got[0].Detail[0], "mid 2.0.0") {
		t.Errorf("detail = %q, want the path from the offending crate to its dependent", got[0].Detail)
	}
}

func TestDenyWritesEverySiblingDependencyPath(t *testing.T) {
	// Two dependents pulling in one crate are two paths, and a shared backing
	// array would let the second overwrite the first.
	got := denyFindings(crateDir(t), []denyMessage{{
		Type: "diagnostic",
		Fields: denyDiagnostic{
			Code: "banned", Severity: "error", Message: "banned crate",
			Graphs: []denyGraph{{
				Krate: denyKrate{Name: "leaf", Version: "1.0.0"},
				Parents: []denyGraph{
					{Krate: denyKrate{Name: "first", Version: "1.0.0"}},
					{Krate: denyKrate{Name: "second", Version: "1.0.0"}},
				},
			}},
		},
	}})
	if len(got[0].Detail) != 2 {
		t.Fatalf("detail = %q, want one line per dependent", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail[0], "first") || !strings.Contains(got[0].Detail[1], "second") {
		t.Errorf("detail = %q, want each sibling's own path", got[0].Detail)
	}
}

func TestDenyMessageNamesTheCrateWhenTheToolDoesNot(t *testing.T) {
	// "a valid license expression could not be retrieved for the crate" says
	// nothing a reader can act on in a comment listing twenty of them.
	got := denyFindings(crateDir(t), []denyMessage{{
		Type: "diagnostic",
		Fields: denyDiagnostic{
			Code: "unlicensed", Severity: "warning", Message: "a valid license expression could not be retrieved for the crate",
			Graphs: []denyGraph{{Krate: denyKrate{Name: "time", Version: "0.1.44"}}},
		},
	}})
	if !strings.Contains(got[0].Message, "time 0.1.44") {
		t.Errorf("message = %q, want the crate named in it", got[0].Message)
	}
}

func TestDecodeDenySkipsOnlyTheMalformedLine(t *testing.T) {
	// The stream mixes log lines in with the diagnostics, and cargo-deny may
	// write something it does not promise. One bad line costs only itself.
	stream := `{"type":"diagnostic","fields":{"code":"first","severity":"error","message":"a"}}` + "\n" +
		"not json at all\n" +
		`{"type":"diagnostic","fields":{"code":"second","severity":"error","message":"b"}}` + "\n"
	got := decodeDeny(strings.NewReader(stream))
	if len(got) != 2 {
		t.Fatalf("got %d messages, want the 2 that parsed either side of the bad line", len(got))
	}
	if got[1].Fields.Code != "second" {
		t.Errorf("second message = %q, want the one after the malformed line", got[1].Fields.Code)
	}
}

func TestDenyArgvIsOneJSONRunWithFormatBeforeTheSubcommand(t *testing.T) {
	// One run decides the row and carries the data. --format goes on the
	// top-level command: cargo-deny rejects `check --format json` outright, so
	// the order is the difference between a run and a usage error.
	argv := denyArgv()
	format := slices.Index(argv, "--format")
	check := slices.Index(argv, "check")
	if format < 0 || check < 0 || format > check {
		t.Errorf("argv = %q, want --format before the subcommand", argv)
	}
	// advisories is excluded: cargo-audit already covers RustSec CVEs, and
	// running both would double-report them.
	if slices.Contains(argv, "advisories") {
		t.Errorf("argv = %q, want advisories left to cargo-audit", argv)
	}
}

func TestDenyPassesOnACleanReport(t *testing.T) {
	r := denyResult(crateDir(t), stderrRun(t, "deny-clean.ndjson"))
	if !r.Ok() {
		t.Errorf("row failed on a clean report: %v", r.Err)
	}
	if len(r.Findings) != 0 {
		t.Errorf("findings = %+v, want none", r.Findings)
	}
	if r.Detail != "" {
		t.Errorf("detail = %q, want none — report() prints it under a failing row", r.Detail)
	}
}

func TestDenyDetailIsEveryDiagnosticWithItsDependencyPathUnderIt(t *testing.T) {
	// The JSON run is the only run, so this Detail is the whole of what a
	// developer is told — and the stream is what the check's log holds, which
	// is read from Output and arrives on stderr.
	r := denyResult(crateDir(t), stderrRun(t, "deny.ndjson"))
	if r.Ok() {
		t.Fatal("row passed a report cargo-deny exited 4 over")
	}
	if len(r.Findings) != 3 {
		t.Fatalf("got %d claims, want the 3 diagnostics in the stream", len(r.Findings))
	}
	if r.Output != r.Stderr || r.Output == "" {
		t.Error("Output does not hold the stream: the check's log is written from it, and stdout is empty under --format json")
	}
	for _, f := range r.Findings {
		if !strings.Contains(r.Detail, f.Message) {
			t.Errorf("detail =\n%s\nwant the claim %q", r.Detail, f.Message)
		}
	}
}

func TestDenyFailsOnAnyNonZeroStatusAndNotOnlyOne(t *testing.T) {
	// cargo-deny's status is a bitmask of which checks failed — licenses 4,
	// bans 2, 6 for both — so a verdict comparing it against 1 passes every
	// failure it reports. This capture exited 2.
	r := denyResult(crateDir(t), stderrRun(t, "deny-transitive.ndjson"))
	if r.Ok() {
		t.Fatal("row passed a report cargo-deny exited 2 over")
	}
	if len(r.Findings) != 1 {
		t.Fatalf("got %d claims, want the one banned crate", len(r.Findings))
	}
	if !strings.Contains(r.Detail, "\n  libc") {
		t.Errorf("detail =\n%s\nwant the dependency path indented under the claim", r.Detail)
	}
}

func TestDenyFailingWithAnUnreadableReportNamesTheExitStatus(t *testing.T) {
	// cargo-deny can set its status for reasons no diagnostic in the stream
	// states — a config it would not read is the standing example. A row
	// failing with empty Detail tells the reader only that something is wrong.
	r := denyResult(t.TempDir(), executil.Result{
		Stderr: "not json at all\n",
		Err:    fmt.Errorf("exit status 4"),
	})
	if len(r.Findings) != 0 {
		t.Fatalf("got %d claims from a stream that does not parse", len(r.Findings))
	}
	if !strings.Contains(r.Detail, "exit status 4") {
		t.Errorf("detail = %q, want the status cargo-deny exited with", r.Detail)
	}
}

func TestDenyNumbersTwoDiagnosticsAlikeInPathAndSite(t *testing.T) {
	// cargo-deny states one crate's licence twice — a warning that no
	// expression could be retrieved and an error that it is unlicensed share a
	// code and a crate, so they share a site. The ordinal is what keeps them
	// two claims.
	one := denyMessage{Type: "diagnostic", Fields: denyDiagnostic{
		Code: "unlicensed", Severity: "warning", Message: "a",
		Graphs: []denyGraph{{Krate: denyKrate{Name: "time", Version: "0.1.44"}}},
	}}
	two := one
	two.Fields.Severity = "error"
	got := denyFindings(crateDir(t), []denyMessage{one, two})
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	if got[0].Site != got[1].Site {
		t.Fatalf("the claims do not share a site, so this no longer guards the ordinal")
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("the two claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestDenySubjectNamesNothingWithoutAGraph(t *testing.T) {
	// Many diagnostics carry no graph at all — they name a check, not a
	// crate. Answering with a name none of them has would locate the claim on
	// a stranger's lockfile line.
	name, version := denySubject(nil)
	if name != "" || version != "" {
		t.Errorf("denySubject(nil) = %q, %q, want neither", name, version)
	}
	name, version = denySubject([]denyGraph{{Krate: denyKrate{Name: "time", Version: "0.1.44"}}})
	if name != "time" || version != "0.1.44" {
		t.Errorf("denySubject = %q, %q, want the first graph's root", name, version)
	}
}

// denyProbeDir is the crate the captured licence runs name, materialised. The
// captures live beside the language-neutral gate they were taken for, and there
// is one probe rather than a second copy here: a fixture duplicated is a fixture
// two packages can disagree about.
func denyProbeDir(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, "../licence/testdata/denyprobe")
}

// licenceRun is a captured cargo-deny licences run, with the status it exited
// with — a bitmask of which checks failed, so the licences-only code is 4.
func licenceRun(t *testing.T, name string) executil.Result {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "licence", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	status, err := os.ReadFile(filepath.Join("..", "licence", "testdata", strings.TrimSuffix(name, filepath.Ext(name))+".exit"))
	if err != nil {
		t.Fatalf("reading the fixture's exit status: %v", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(status)))
	if err != nil {
		t.Fatalf("exit status of %s: %v", name, err)
	}
	r := executil.Result{Stderr: string(data)}
	if code != 0 {
		r.Err = fmt.Errorf("exit status %d", code)
	}
	return r
}

func TestDenyArgvChecksBansAloneUnderTheComponentsOwnConfig(t *testing.T) {
	// A --config pointed at a generated file replaces cargo-deny's whole
	// document, so a combined run would take the component's [bans],
	// [advisories] and [sources] tables with it.
	argv := denyArgv()
	if got := strings.Join(argv, " "); got != "deny --format json check bans" {
		t.Errorf("argv = %q", got)
	}
	if slices.Contains(argv, "--config") {
		t.Errorf("argv = %q, want the component's own configuration deciding bans", argv)
	}
}

func TestDenyLicencesArgvCarriesTheGeneratedConfigBeforeTheSubcommand(t *testing.T) {
	// cargo-deny declares --format and --config on the top-level command and
	// rejects them after the subcommand, so the order is the difference between
	// a run and a usage error.
	argv := denyLicencesArgv("/tmp/generated.toml")
	if got := strings.Join(argv, " "); got != "deny --format json --config /tmp/generated.toml check licenses" {
		t.Errorf("argv = %q", got)
	}
	if slices.Contains(argv, "bans") {
		t.Errorf("argv = %q, want licenses alone", argv)
	}
}

func TestDenyLicencesArgvOmitsTheFlagWithNoConfig(t *testing.T) {
	// No --config at all is the component's own deny.toml deciding, which is
	// what PolicyFromConsumer means. An empty path would be a file cargo-deny
	// cannot read and a check that never runs.
	argv := denyLicencesArgv("")
	if got := strings.Join(argv, " "); got != "deny --format json check licenses" {
		t.Errorf("argv = %q", got)
	}
}

func TestGeneratedPolicyAllowsTheUnusedAllowance(t *testing.T) {
	// At its default, every allowed licence the graph does not use becomes a
	// `license-not-encountered` warning naming no crate, locating at line 0 and
	// sharing one site with every other one. A repository-wide allow-list is by
	// construction broader than any one component's graph.
	got := denyLicencePolicy(licence.NewPolicy([]string{"MIT", "Apache-2.0"}))
	want := "[licenses]\nallow = [\"MIT\", \"Apache-2.0\"]\nunused-allowed-license = \"allow\"\n"
	if got != want {
		t.Errorf("generated config =\n%s\nwant\n%s", got, want)
	}
}

func TestGeneratedPolicyQuotesEveryIdentifier(t *testing.T) {
	// The identifiers arrive out of the repository's own configuration, and one
	// carrying a quote would otherwise write a document cargo-deny refuses to
	// parse — a licence check that cannot run.
	got := denyLicencePolicy(licence.NewPolicy([]string{`MIT" ]`}))
	if !strings.Contains(got, `"MIT\" ]"`) {
		t.Errorf("generated config = %q, want the identifier quoted", got)
	}
}

func TestGeneratedPolicySuppressesEveryUnusedAllowanceDiagnostic(t *testing.T) {
	// The capture taken with the key left out: every allowance the probe's graph
	// does not use is a diagnostic naming no crate, and all of them share the
	// one site. The set the gate compares must hold the rejected crate alone.
	run := licenceRun(t, "deny-licenses-unused-allowance.ndjson")
	// What the key is for, in the capture taken without it: claims that locate
	// nowhere and share one site, separated only by an ordinal.
	unlocated := map[string]int{}
	for _, f := range denyFindings(denyProbeDir(t), decodeDeny(strings.NewReader(run.Stderr))) {
		if f.Line == 0 {
			unlocated[f.Site]++
		}
	}
	if len(unlocated) != 1 || unlocated["license-not-encountered\x1f "] < 2 {
		t.Fatalf("unlocated claims = %v, want the colliding site the default produces", unlocated)
	}
	set, err := denyLicenceSet(denyProbeDir(t), run)
	if err != nil {
		t.Fatalf("reading the set: %v", err)
	}
	if set.Len() != 1 || !set.Has(licence.Pair{Package: "cbindgen", Licence: "MPL-2.0"}) {
		t.Errorf("set = %+v, want cbindgen's MPL-2.0 alone", set.Dependencies())
	}
}

func TestDenyLicenceSetReadsTheRejectedCrateAndItsExpression(t *testing.T) {
	// cbindgen is MPL-2.0 under an allow-list that omits it; libc's
	// `Apache-2.0 OR MIT` is satisfied by either half and is no pair at all.
	set, err := denyLicenceSet(denyProbeDir(t), licenceRun(t, "deny-licenses-rejected.ndjson"))
	if err != nil {
		t.Fatalf("reading the set: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("set = %+v, want the one rejected crate", set.Dependencies())
	}
	got := set.Dependencies()[0]
	if got.Package != "cbindgen" || got.Licence != "MPL-2.0" || got.Version != "0.27.0" {
		t.Errorf("pair = %+v, want cbindgen 0.27.0 at MPL-2.0", got)
	}
}

func TestDenyLicenceSetIsEmptyOnAPassingRun(t *testing.T) {
	// The same graph under an allow-list that includes MPL-2.0 exits 0 with the
	// summary line alone. The bans warning in the stream is not a licence pair.
	set, err := denyLicenceSet(denyProbeDir(t), licenceRun(t, "deny-licenses-allowed.ndjson"))
	if err != nil {
		t.Fatalf("reading the set: %v", err)
	}
	if set.Len() != 0 {
		t.Errorf("set = %+v, want none", set.Dependencies())
	}
}

func TestDenyLicenceSetReadsTheLicencesOnlyExitAsAMeasurement(t *testing.T) {
	// cargo-deny's status is a bitmask of which checks failed, and a licences
	// run that rejected something exits 4. That status is not the verdict: what
	// gates is the delta against the merge-base, so the run has measured a set.
	r := licenceRun(t, "deny-licenses-rejected.ndjson")
	if r.Err == nil {
		t.Fatal("the capture no longer carries the non-zero status it exited with")
	}
	if _, err := denyLicenceSet(denyProbeDir(t), r); err != nil {
		t.Errorf("a run that rejected a crate answered no set: %v", err)
	}
}

func TestDenyLicenceSetFailingWithNothingToShowMeasuresNoSet(t *testing.T) {
	// cargo-deny sets its status for reasons no diagnostic states — a config it
	// would not read is the standing example — and a measured empty set there
	// would be a gate that could not run rendering as one that found nothing.
	_, err := denyLicenceSet(t.TempDir(), executil.Result{
		Stderr: "error: failed to read config\n",
		Err:    fmt.Errorf("exit status 4"),
	})
	if err == nil {
		t.Fatal("a run that measured nothing answered a set")
	}
	if !strings.Contains(err.Error(), "failed to read config") {
		t.Errorf("error = %q, want cargo-deny's own first line in it", err)
	}
}

func TestDenyLicenceSetKeepsAWarningOutOfTheSet(t *testing.T) {
	// One crate arrives as both — a warning that no expression could be read and
	// an error that it is unlicensed. cargo-deny's status is set by its errors,
	// so the warning is a claim the configuration it ran under does not fail on.
	set, err := denyLicenceSet(crateDir(t), stderrRun(t, "deny.ndjson"))
	if err != nil {
		t.Fatalf("reading the set: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("set = %+v, want the one error", set.Dependencies())
	}
	if got := set.Dependencies()[0]; got.Licence != licence.Unknown {
		t.Errorf("licence = %q, want %q — an unlicensed crate is a pair like any other", got.Licence, licence.Unknown)
	}
}

func TestDenyLicenceOfTakesTheFirstUnderlinedSpan(t *testing.T) {
	// The span is the expression cargo-deny refused, and a rejection carries the
	// same one twice. An empty span is no expression at all.
	got := denyLicenceOf(denyDiagnostic{Code: "rejected", Labels: []denyLabel{{Span: ""}, {Span: " MPL-2.0 "}}})
	if got != "MPL-2.0" {
		t.Errorf("licence = %q, want MPL-2.0", got)
	}
	if got := denyLicenceOf(denyDiagnostic{Code: "rejected"}); got != licence.Unknown {
		t.Errorf("licence = %q, want %q for a rejection underlining nothing", got, licence.Unknown)
	}
	if got := denyLicenceOf(denyDiagnostic{Code: denyUnlicensed, Labels: []denyLabel{{Span: "auditprobe"}}}); got != licence.Unknown {
		t.Errorf("licence = %q, want %q — an unlicensed crate's span is its name, not a licence", got, licence.Unknown)
	}
}

func TestLicenceDependenciesDropADiagnosticNamingNoCrate(t *testing.T) {
	// `license-not-encountered` names an allow-list entry rather than a
	// dependency. A pair with an empty package grandfathers nothing and locates
	// nowhere.
	got := licenceDependencies([]denyMessage{{Type: "diagnostic", Fields: denyDiagnostic{
		Code: "license-not-encountered", Severity: "error", Labels: []denyLabel{{Span: "ISC"}},
	}}})
	if len(got) != 0 {
		t.Errorf("pairs = %+v, want none", got)
	}
}

func TestPolicyForPrefersTheRepositorysOwnPolicy(t *testing.T) {
	// The repository's policy is the one document every language reads. A
	// component able to override it would make a Rust row answer a question no
	// Go row is asked.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DenyConfigFile), []byte("[bans]\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if got := PolicyFor(dir, licence.NewPolicy([]string{"MIT"})); got != PolicyFromLydite {
		t.Errorf("source = %q, want %q even beside a deny.toml", got, PolicyFromLydite)
	}
}

func TestPolicyForFallsBackToTheComponentsOwnConfig(t *testing.T) {
	// With no policy stated, the component's own deny.toml is what decides the
	// licence check — and the row has to name it, because that is the file an
	// edit goes in.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DenyConfigFile), []byte("[licenses]\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if got := PolicyFor(dir, licence.NewPolicy(nil)); got != PolicyFromConsumer {
		t.Errorf("source = %q, want %q", got, PolicyFromConsumer)
	}
}

func TestPolicyForReadsTheDottedConfigName(t *testing.T) {
	// cargo-deny reads `.deny.toml` where there is no `deny.toml`, and a
	// component using it has a policy this must not call absent.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "."+DenyConfigFile), []byte("[licenses]\n"), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if got := PolicyFor(dir, licence.NewPolicy(nil)); got != PolicyFromConsumer {
		t.Errorf("source = %q, want %q", got, PolicyFromConsumer)
	}
}

func TestPolicyForIsNoneWithNeither(t *testing.T) {
	// cargo-deny's own default rejects every licence, MIT included, so no run
	// happens at all: the row says what is missing instead of failing a
	// component for a reason nothing in the repository chose.
	if got := PolicyFor(t.TempDir(), licence.NewPolicy(nil)); got != PolicyFromNone {
		t.Errorf("source = %q, want %q", got, PolicyFromNone)
	}
}

// consumerSet is a captured cargo-deny licences run read as the set a
// component's own deny.toml decided.
func consumerSet(t *testing.T, name string) licence.Set {
	t.Helper()
	set, err := denyLicenceSet(denyProbeDir(t), licenceRun(t, name))
	if err != nil {
		t.Fatalf("reading the set: %v", err)
	}
	return set
}

func TestConsumerConfigPassesWhereItRejectedNothing(t *testing.T) {
	// cargo-deny evaluated the component's own file and refused no crate, which
	// is a gate that ran and found nothing — not a gate nothing configured.
	comparison, found := licenceVerdict(denyProbeDir(t), licence.NewPolicy(nil), PolicyFromConsumer,
		consumerSet(t, "deny-licenses-allowed.ndjson"), licence.NoDiffBase())
	if comparison.Verdict != licence.VerdictPass {
		t.Errorf("verdict = %q, want %q", comparison.Verdict, licence.VerdictPass)
	}
	if len(comparison.Pairs) != 0 || len(found) != 0 {
		t.Errorf("pairs = %+v, claims = %+v, want neither", comparison.Pairs, found)
	}
}

func TestConsumerConfigFailsOnEveryCrateItRejected(t *testing.T) {
	// The file is the component's own and cargo-deny evaluates it whole, so its
	// rejections fail the row and each one is a claim its author can act on.
	comparison, found := licenceVerdict(denyProbeDir(t), licence.NewPolicy(nil), PolicyFromConsumer,
		consumerSet(t, "deny-licenses-rejected.ndjson"), licence.NoDiffBase())
	if comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want %q", comparison.Verdict, licence.VerdictFail)
	}
	if len(comparison.Pairs) != 1 || comparison.Pairs[0].Package != "cbindgen" {
		t.Fatalf("pairs = %+v, want the rejected crate", comparison.Pairs)
	}
	if len(found) != 1 {
		t.Fatalf("got %d claims, want one per rejected crate", len(found))
	}
	if found[0].Gate != licence.Gate || found[0].Path != cargoLockFile || found[0].Line != 12 {
		t.Errorf("claim = %+v, want the licence gate at the probe's lockfile stanza", found[0])
	}
}

func TestConsumerConfigGatesWithoutAMergeBase(t *testing.T) {
	// Grandfathering exists to make this repository's own newly stated policy
	// adoptable. A component's deny.toml is its authors' own pre-existing
	// choice, so a crate it rejects at both ends is still a failing row.
	current := consumerSet(t, "deny-licenses-rejected.ndjson")
	comparison, found := licenceVerdict(denyProbeDir(t), licence.NewPolicy(nil), PolicyFromConsumer,
		current, licence.MeasuredBase(current))
	if comparison.Verdict != licence.VerdictFail || len(found) != 1 {
		t.Errorf("verdict = %q with %d claims, want %q with the rejected crate named",
			comparison.Verdict, len(found), licence.VerdictFail)
	}
}

func TestLyditePolicyGatesOnTheDeltaAgainstTheMergeBase(t *testing.T) {
	// The org-wide policy arrives on a repository that already ships whatever it
	// ships, so a crate the merge-base already carried introduces nothing.
	current := consumerSet(t, "deny-licenses-rejected.ndjson")
	comparison, found := licenceVerdict(denyProbeDir(t), licence.NewPolicy([]string{"MIT"}), PolicyFromLydite,
		current, licence.MeasuredBase(current))
	if comparison.Verdict != licence.VerdictPass || len(found) != 0 {
		t.Errorf("verdict = %q with %d claims, want %q and none",
			comparison.Verdict, len(found), licence.VerdictPass)
	}
}

func TestNoPolicySourceDecidesNothing(t *testing.T) {
	// No run happened, so the empty set measures nothing: a verdict over it
	// would be a gate that could not run rendering as one that found nothing.
	comparison, found := licenceVerdict(denyProbeDir(t), licence.NewPolicy(nil), PolicyFromNone,
		licence.Set{}, licence.NoDiffBase())
	if comparison.Verdict != licence.VerdictNotConfigured {
		t.Errorf("verdict = %q, want %q", comparison.Verdict, licence.VerdictNotConfigured)
	}
	if len(comparison.Pairs) != 0 || len(found) != 0 {
		t.Errorf("pairs = %+v, claims = %+v, want neither", comparison.Pairs, found)
	}
}

func TestLicenceFindingsLocateAtTheLockfileStanzaAndKeyOnThePair(t *testing.T) {
	// cbindgen is named on line 12 of the probe's lockfile. The site is the pair
	// rather than the text of that line, so a crate offering two non-conforming
	// licences stays two claims.
	got := LicenceFindings(denyProbeDir(t), []licence.Dependency{
		{Package: "cbindgen", Version: "0.27.0", Licence: "MPL-2.0"},
		{Package: "cbindgen", Version: "0.27.0", Licence: "GPL-3.0-only"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d claims, want both licences against the one crate", len(got))
	}
	if got[0].Path != cargoLockFile || got[0].Line != 12 {
		t.Errorf("located at %s:%d, want %s:12", got[0].Path, got[0].Line, cargoLockFile)
	}
	if got[0].Gate != licence.Gate || got[0].Rule != "MPL-2.0" {
		t.Errorf("claim = %+v, want the licence gate and the licence as its rule", got[0])
	}
	if got[0].Site != (licence.Pair{Package: "cbindgen", Licence: "MPL-2.0"}).Site() {
		t.Errorf("site = %q, want the pair's own", got[0].Site)
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("the two licences share a fingerprint, so the second is dropped as a duplicate")
	}
	if !strings.Contains(got[0].Message, "cbindgen 0.27.0") {
		t.Errorf("message = %q, want the crate and version named in it", got[0].Message)
	}
}

func TestLicenceFindingsReportZeroForACrateNoLockfileNames(t *testing.T) {
	// A guessed line is a review thread on code that has nothing to do with the
	// claim, on a pull request whose author cannot act on it.
	got := LicenceFindings(t.TempDir(), []licence.Dependency{{Package: "cbindgen", Version: "0.27.0", Licence: "MPL-2.0"}})
	if len(got) != 1 || got[0].Line != 0 {
		t.Errorf("claims = %+v, want one claim with no line", got)
	}
}

func TestDenyBansFindingIsIdentityAndLocationUnchanged(t *testing.T) {
	// A finding's fingerprint is what a standing review thread is keyed by, so
	// every field a bans claim is built from is pinned here field by field: a
	// site, a path or a line that moves re-fingerprints every open thread and
	// posts the same claim a second time under a new one.
	got := denyFindings(crateDir(t), decodeDeny(strings.NewReader(readFixture(t, "deny-transitive.ndjson"))))
	if len(got) != 1 {
		t.Fatalf("got %d claims, want the one banned crate", len(got))
	}
	f := got[0]
	if f.Gate != GateDeny {
		t.Errorf("gate = %q, want %q", f.Gate, GateDeny)
	}
	if f.Path != "Cargo.lock" {
		t.Errorf("path = %q, want Cargo.lock", f.Path)
	}
	if f.Rule != "banned" || f.Severity != "error" {
		t.Errorf("rule/severity = %q/%q, want banned/error", f.Rule, f.Severity)
	}
	if f.Site != "banned\x1flibc 0.2.189" {
		t.Errorf("site = %q, want the code and the crate it is against", f.Site)
	}
	if f.Message != "crate 'libc = 0.2.189' is explicitly banned" {
		t.Errorf("message = %q, want cargo-deny's own", f.Message)
	}
	if f.Ordinal != 0 {
		t.Errorf("ordinal = %d, want the first", f.Ordinal)
	}
	// The whole of what a thread is keyed by, as one literal: gate, component,
	// path, site and ordinal. Any of the five moving re-keys every open thread.
	if want := bansFingerprint; f.Fingerprint() != want {
		t.Errorf("fingerprint = %q, want %q", f.Fingerprint(), want)
	}
}

// bansFingerprint is the identity a bans claim against the probe's banned crate
// carries. It is a literal rather than a second computation, because a
// fingerprint recomputed from the same fields it is meant to pin agrees with
// itself however they move.
const bansFingerprint = "v1:28cf4c0066a801a5"

// readFixture is a captured stream, read for its text alone.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return string(data)
}
