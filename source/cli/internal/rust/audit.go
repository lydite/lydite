package rust

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// auditReport mirrors the subset of `cargo audit --json` lydite reads.
type auditReport struct {
	Vulnerabilities struct {
		List []auditVulnerability `json:"list"`
	} `json:"vulnerabilities"`
	// Warnings is keyed by kind — unmaintained, unsound, notice — each
	// holding its own list. They are advisories cargo-audit declines to fail
	// on by default, and lydite reports them as findings without failing on
	// them either: the row's verdict stays cargo-audit's own.
	Warnings map[string][]auditVulnerability `json:"warnings"`
}

type auditVulnerability struct {
	Advisory struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"advisory"`
	Package struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"package"`
	Versions struct {
		Patched []string `json:"patched"`
	} `json:"versions"`
}

// auditFindings is every advisory as a located claim on the lockfile line
// naming the crate it is against.
//
// Warnings are findings too. cargo-audit sorts an advisory into `warnings`
// when its own policy says not to fail the build on that kind — unmaintained,
// unsound, a notice — and that is a statement about the verdict, not about
// whether the claim exists. Dropping them would make lydite report fewer
// advisories than the tool it just ran, and the row's verdict is unaffected
// either way because it stays cargo-audit's own exit status.
func auditFindings(dir string, report auditReport) []finding.Finding {
	lock := readCargoLock(dir)
	out := make([]finding.Finding, 0, len(report.Vulnerabilities.List))
	for _, v := range report.Vulnerabilities.List {
		out = append(out, auditFinding(lock, v, "vulnerability"))
	}
	// Sorted, because a map's iteration order is not, and an ordinal counted
	// in a different order every run is a fingerprint that moves on its own.
	for _, kind := range sortedKeys(report.Warnings) {
		for _, v := range report.Warnings[kind] {
			out = append(out, auditFinding(lock, v, kind))
		}
	}
	finding.Number(out)
	return out
}

// auditFinding is one advisory, located and worded.
func auditFinding(lock *cargoLock, v auditVulnerability, severity string) finding.Finding {
	name, version := v.Package.Name, v.Package.Version
	message := v.Advisory.Title
	if message == "" {
		message = v.Advisory.ID
	}
	message += " in " + name + " " + version
	if patched := strings.Join(v.Versions.Patched, ", "); patched != "" {
		message += " (patched in " + patched + ")"
	}
	var detail []string
	if v.Advisory.URL != "" {
		detail = append(detail, v.Advisory.URL)
	}
	return finding.Finding{
		Gate:     GateAudit,
		Path:     cargoLockFile,
		Line:     lock.Line(name, version),
		Rule:     v.Advisory.ID,
		Severity: severity,
		Message:  message,
		Detail:   detail,
		// The advisory with the crate it is against, and not the text of the
		// lockfile line: that line reads `name = "time"` and cannot tell two
		// advisories against one crate apart. See ADR 0032.
		Site: v.Advisory.ID + "\x1f" + name + " " + version,
	}
}

// sortedKeys is a map's keys in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// parseAudit reads a report, saying whether it could.
//
// A report that will not parse leaves the run entirely to cargo-audit's own
// exit status and output, rather than letting a parse error decide the
// verdict.
func parseAudit(data []byte) (auditReport, bool) {
	var report auditReport
	if err := json.Unmarshal(data, &report); err != nil {
		return auditReport{}, false
	}
	return report, true
}

// auditArgv is the invocation, as argv. --json replaces cargo-audit's human
// report rather than copying it, and the report is what the row's claims and
// its Detail are both derived from.
func auditArgv() []string { return []string{"audit", "--json"} }

// runAudit runs cargo-audit once, as data.
//
// --json puts the report on stdout and leaves stderr for cargo-audit's own
// complaints, so the run that is parseable is the run that decides the row, and
// the advisories a developer reads are the Detail report() prints from the
// claims rather than a second invocation's stream.
// [lydite:exclude_from_coverage][the proving ground installs and runs the
// pinned cargo-audit on a bare checkout; a unit test here would run the
// machine's own, and what is lydite's to get right is the invocation, which
// auditArgv states and TestAuditArgvIsOneJSONRun asserts — everything done
// with the output is auditResult, which the captured reports test directly]
func runAudit(ctx context.Context, dir string, env []string, bin string) executil.Result {
	return auditResult(dir, named(GateAudit, executil.RunQuietEnv(ctx, dir, env, bin, auditArgv()...)))
}

// auditResult is one run read as claims, with the Detail a failing row needs
// rendered from them.
//
// cargo-audit's exit status stays the verdict and a finding count never becomes
// one: an advisory it sorts into `warnings` is a claim on a run it exits zero
// for, and a run that cannot read the lockfile fails before writing a report at
// all.
//
// Crashed is exactly that last case: no report on stdout that parses. A
// report that does parse is every advisory cargo-audit holds against the
// lockfile, whatever it exited with — its non-zero status is how it says one
// of them is a vulnerability.
func auditResult(dir string, r executil.Result) executil.Result {
	report, ok := parseAudit([]byte(r.Output))
	r.Crashed = !ok
	if ok {
		r.Findings = auditFindings(dir, report)
	}
	if r.Ok() {
		return r
	}
	if detail := findingsDetail(r.Findings); detail != "" {
		r.Detail = detail
		return r
	}
	// cargo-audit says why it would not run on stderr — an unreadable
	// Cargo.lock, an advisory database it could not fetch — and that text is
	// the only account of the failure anywhere, since stdout holds no report.
	r.Detail = unreadable(GateAudit, r.Err)
	if said := strings.TrimSpace(r.Stderr); said != "" {
		r.Detail += "\n" + said
	}
	return r
}
