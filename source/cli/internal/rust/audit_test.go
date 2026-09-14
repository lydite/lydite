package rust

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/fixture"
)

// crateDir is the crate the captured reports name, materialised.
func crateDir(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, "testdata/auditprobe")
}

// auditFixture is cargo-audit's real `--json` report over a crate pinned to
// two vulnerable dependencies, captured from the pinned tool.
func auditFixture(t *testing.T) auditReport {
	t.Helper()
	data, err := os.ReadFile("testdata/audit.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var rep auditReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	return rep
}

func TestAuditLocatesAnAdvisoryAtTheLockfileLineNamingTheCrate(t *testing.T) {
	got := auditFindings(crateDir(t), auditFixture(t))
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	byRule := map[string]int{}
	for _, f := range got {
		byRule[f.Rule] = f.Line
		if f.Path != cargoLockFile {
			t.Errorf("%s located in %q, want %s — the one edit that clears an advisory is the bump", f.Rule, f.Path, cargoLockFile)
		}
	}
	// The lines the stanzas actually name those crates on, read out of the
	// captured lockfile rather than asserted from memory.
	lock := readCargoLock(crateDir(t))
	if want := lock.Line("smallvec", "1.6.0"); byRule["RUSTSEC-2021-0003"] != want || want == 0 {
		t.Errorf("RUSTSEC-2021-0003 on line %d, want %d", byRule["RUSTSEC-2021-0003"], want)
	}
	if want := lock.Line("time", "0.1.44"); byRule["RUSTSEC-2020-0071"] != want || want == 0 {
		t.Errorf("RUSTSEC-2020-0071 on line %d, want %d", byRule["RUSTSEC-2020-0071"], want)
	}
}

func TestAuditIdentifiesAnAdvisoryByItsIDAndNotByTheLockfileLine(t *testing.T) {
	// A lockfile line reads `name = "time"` and cannot tell two advisories
	// against one crate apart. Identifying them by the line's text would make
	// them one claim, reported once.
	rep := auditReport{}
	rep.Vulnerabilities.List = []auditVulnerability{
		advisory("RUSTSEC-2020-0071", "time", "0.1.44"),
		advisory("RUSTSEC-2099-0001", "time", "0.1.44"),
	}
	got := auditFindings(crateDir(t), rep)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	if got[0].Site == got[1].Site {
		t.Errorf("both advisories share the site %q, so they are one claim", got[0].Site)
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("two advisories against one crate share a fingerprint")
	}
}

func TestAuditReportsAnAdvisoryItCannotLocate(t *testing.T) {
	// A crate no lockfile names — a lockfile lydite could not read, or an
	// advisory against a crate resolved some other way. The claim survives
	// with no line rather than being guessed onto one: after a finding became
	// a review thread, a guessed line is a thread on a stranger's code.
	rep := auditReport{}
	rep.Vulnerabilities.List = []auditVulnerability{advisory("RUSTSEC-2099-0002", "absent", "9.9.9")}
	got := auditFindings(crateDir(t), rep)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1 — an advisory with no line is still an advisory", len(got))
	}
	if got[0].Line != 0 {
		t.Errorf("line = %d, want 0", got[0].Line)
	}
	if got[0].Anchor != "" {
		t.Errorf("anchor = %q, want the unanchorable zero value — it belongs in the standing comment", got[0].Anchor)
	}
}

func TestAuditReportsWarningsAsFindings(t *testing.T) {
	// cargo-audit sorts an advisory into `warnings` when its own policy says
	// not to fail the build on that kind. That is a statement about the
	// verdict, not about whether the claim exists, and the verdict stays the
	// tool's exit status either way.
	rep := auditReport{Warnings: map[string][]auditVulnerability{
		"unmaintained": {advisory("RUSTSEC-2020-0071", "time", "0.1.44")},
		"unsound":      {advisory("RUSTSEC-2021-0003", "smallvec", "1.6.0")},
	}}
	got := auditFindings(crateDir(t), rep)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2 — a warning lydite drops is an advisory the tool reported and lydite did not", len(got))
	}
	// Sorted by kind, so an ordinal is not counted in a different order on
	// every run: a map's iteration order is not stable, and a fingerprint
	// that moves on its own re-reports every claim as new.
	if got[0].Severity != "unmaintained" || got[1].Severity != "unsound" {
		t.Errorf("severities = %q, %q, want unmaintained then unsound", got[0].Severity, got[1].Severity)
	}
}

func TestAuditMessageNamesTheCrateAndTheFix(t *testing.T) {
	got := auditFindings(crateDir(t), auditFixture(t))
	for _, f := range got {
		if !strings.Contains(f.Message, "patched in") {
			t.Errorf("%s message = %q, want the versions that clear it", f.Rule, f.Message)
		}
	}
}

func TestCargoLockTellsTwoVersionsOfOneCrateApart(t *testing.T) {
	dir := t.TempDir()
	lock := "# a lockfile\n[[package]]\nname = \"dup\"\nversion = \"1.0.0\"\n\n[[package]]\nname = \"dup\"\nversion = \"2.0.0\"\n"
	if err := os.WriteFile(dir+"/"+cargoLockFile, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	l := readCargoLock(dir)
	if first, second := l.Line("dup", "1.0.0"), l.Line("dup", "2.0.0"); first != 3 || second != 7 {
		t.Errorf("lines = %d, %d, want 3 and 7 — an advisory against one version must not land on the other's line", first, second)
	}
}

func TestCargoLockAnswersZeroWhenItCannotBeRead(t *testing.T) {
	// An unreadable lockfile costs every advisory its line and no advisory
	// its existence.
	if got := readCargoLock(t.TempDir()).Line("time", "0.1.44"); got != 0 {
		t.Errorf("line = %d, want 0", got)
	}
}

// advisory is one vulnerability record, for the cases the captured report
// does not contain.
func advisory(id, name, version string) auditVulnerability {
	var v auditVulnerability
	v.Advisory.ID = id
	v.Advisory.Title = "a claim"
	v.Package.Name = name
	v.Package.Version = version
	return v
}

func TestAuditFallsBackToTheExitStatusOnAnUnreadableReport(t *testing.T) {
	for _, data := range []string{"", "not json at all", `{"vulnerabilities": `} {
		if _, ok := parseAudit([]byte(data)); ok {
			t.Errorf("parseAudit(%q) reported success", data)
		}
	}
	if _, ok := parseAudit([]byte(`{"vulnerabilities":{"list":[]}}`)); !ok {
		t.Error("a report that does parse was rejected")
	}
}

func TestAuditArgvIsOneJSONRun(t *testing.T) {
	// One run decides the row and carries the data. --json is what makes the
	// report parseable; without it the row fails with no advisory named
	// anywhere, because nothing else reaches the developer.
	if got := strings.Join(auditArgv(), " "); got != "audit --json" {
		t.Errorf("argv = %q", got)
	}
}

func TestAuditPassesOnACleanReport(t *testing.T) {
	r := auditResult(crateDir(t), stdoutRun(t, "audit-clean.json"))
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

func TestAuditDetailIsEveryAdvisoryWithItsURLUnderIt(t *testing.T) {
	// The JSON run is the only run, so this Detail is the whole of what a
	// developer is told about a failing audit.
	r := auditResult(crateDir(t), stdoutRun(t, "audit.json"))
	if r.Ok() {
		t.Fatal("row passed a report cargo-audit exited 1 over")
	}
	if len(r.Findings) != 2 {
		t.Fatalf("got %d claims, want 2", len(r.Findings))
	}
	for _, f := range r.Findings {
		if len(f.Detail) != 1 {
			t.Fatalf("%s states %q, want the one advisory URL", f.Rule, f.Detail)
		}
		if !strings.Contains(r.Detail, f.Rule) || !strings.Contains(r.Detail, "\n  "+f.Detail[0]+"\n") {
			t.Errorf("detail =\n%s\nwant %s named with its advisory URL under it", r.Detail, f.Rule)
		}
	}
}

func TestAuditWarningsAreClaimsOnARunThatPasses(t *testing.T) {
	// cargo-audit exits zero over an advisory it sorts into `warnings`. The
	// verdict is its own exit status and never a count of claims, so the row
	// passes while still reporting what the tool found.
	r := auditResult(fixture.Tree(t, "testdata/warnprobe"), stdoutRun(t, "audit-warnings.json"))
	if !r.Ok() {
		t.Errorf("row failed a run cargo-audit exited zero on: %v", r.Err)
	}
	if len(r.Findings) == 0 {
		t.Error("no claims: a warning lydite drops is an advisory the tool reported and lydite did not")
	}
}

func TestAuditFailingWithAnUnreadableReportSaysWhatItSaid(t *testing.T) {
	// cargo-audit refuses before writing a report when it cannot read the
	// lockfile, and says so on stderr — which under --json is the only account
	// of the failure that exists.
	r := auditResult(t.TempDir(), executil.Result{
		Output: "not json at all\n",
		Stderr: "error: Couldn't find `Cargo.lock`\n",
		Err:    fmt.Errorf("exit status 1"),
	})
	if len(r.Findings) != 0 {
		t.Fatalf("got %d claims from a report that does not parse", len(r.Findings))
	}
	if !strings.Contains(r.Detail, "exit status 1") {
		t.Errorf("detail = %q, want the status cargo-audit exited with", r.Detail)
	}
	if !strings.Contains(r.Detail, "Couldn't find `Cargo.lock`") {
		t.Errorf("detail = %q, want what cargo-audit said on stderr", r.Detail)
	}
}

func TestAuditNumbersOneAdvisoryReportedTwice(t *testing.T) {
	// Severity takes no part in a site, so an advisory cargo-audit states
	// both in `vulnerabilities.list` and under a `warnings` kind reaches here
	// twice with one site. Without an ordinal the two hash alike and
	// finding.Set drops the second, which is a claim the tool made and lydite
	// did not report.
	same := advisory("RUSTSEC-2020-0071", "time", "0.1.44")
	rep := auditReport{Warnings: map[string][]auditVulnerability{"unmaintained": {same}}}
	rep.Vulnerabilities.List = []auditVulnerability{same}

	got := auditFindings(crateDir(t), rep)
	if len(got) != 2 {
		t.Fatalf("got %d claims, want 2", len(got))
	}
	if got[0].Site != got[1].Site {
		t.Fatalf("the two claims do not share a site (%q, %q), so this no longer guards the ordinal", got[0].Site, got[1].Site)
	}
	if got[0].Ordinal == got[1].Ordinal {
		t.Errorf("both claims carry ordinal %d, so they are one claim", got[0].Ordinal)
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Error("the two claims share a fingerprint, so the second is dropped as a duplicate")
	}
}

func TestAuditFallsBackToTheAdvisoryIDWithNoTitle(t *testing.T) {
	// An advisory with no title still has to read as a sentence naming what
	// is wrong and where, and the id is the only name it has.
	untitled := advisory("RUSTSEC-2099-0003", "time", "0.1.44")
	untitled.Advisory.Title = ""
	rep := auditReport{}
	rep.Vulnerabilities.List = []auditVulnerability{untitled}
	got := auditFindings(crateDir(t), rep)
	if len(got) != 1 {
		t.Fatalf("got %d claims, want 1", len(got))
	}
	if !strings.HasPrefix(got[0].Message, "RUSTSEC-2099-0003 in time 0.1.44") {
		t.Errorf("message = %q, want the id standing in for the title", got[0].Message)
	}
	// No URL is no detail rather than an empty line under the claim.
	if len(got[0].Detail) != 0 {
		t.Errorf("detail = %q, want none when the advisory states no URL", got[0].Detail)
	}
	withURL := advisory("RUSTSEC-2099-0004", "time", "0.1.44")
	withURL.Advisory.URL = "https://rustsec.org/advisories/RUSTSEC-2099-0004"
	rep.Vulnerabilities.List = []auditVulnerability{withURL}
	got = auditFindings(crateDir(t), rep)
	if len(got[0].Detail) != 1 {
		t.Errorf("detail = %q, want the advisory's URL", got[0].Detail)
	}
}
