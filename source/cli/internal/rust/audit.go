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
		Gate:     "cargo-audit",
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

// auditArgv is one of the two passes, as argv. --json is what replaces the
// human report rather than copying it, which is the whole reason there are two.
func auditArgv(asJSON bool) []string {
	if asJSON {
		return []string{"audit", "--json"}
	}
	return []string{"audit"}
}

// runAudit runs cargo-audit twice: once for the terminal, once for the data.
//
// --json replaces cargo-audit's terminal output rather than copying it: with
// the flag, stdout is the report and stderr is empty, so a single JSON run
// would leave a developer with a failing row and no advisory named anywhere.
// The first pass is therefore the one that streams and decides the row, and
// the second only populates Findings.
//
// It is cheap because the work is reading a lockfile against an advisory
// database that the first pass has already fetched — measured at 1.8s against
// the first pass's 3.1s.
// [lydite:exclude_from_coverage][the proving ground installs and runs the
// pinned cargo-audit on a bare checkout; a unit test here would run the
// machine's own, and what is lydite's to get right is the invocation, which
// auditArgv states and TestAuditArgvKeepsTheHumanPass asserts]
func runAudit(ctx context.Context, dir string, env []string, bin string) executil.Result {
	r := named("cargo-audit", executil.RunEnv(ctx, dir, env, bin, auditArgv(false)...))

	data := executil.RunQuietEnv(ctx, dir, env, bin, auditArgv(true)...)
	report, ok := parseAudit([]byte(data.Output))
	if !ok {
		// No report to read: leave cargo-audit's own exit status and output
		// as-is rather than inventing a verdict.
		return r
	}
	r.Findings = auditFindings(dir, report)
	return r
}
