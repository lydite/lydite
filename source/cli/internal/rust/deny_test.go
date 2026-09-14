package rust

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
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
	if got := strings.Join(argv, " "); got != "deny --format json check licenses bans" {
		t.Errorf("argv = %q", got)
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
