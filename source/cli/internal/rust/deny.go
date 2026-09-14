package rust

import (
	"context"
	"io"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
)

// denyMessage is one line of cargo-deny's NDJSON stream.
//
// The stream goes to *stderr*, not stdout, and mixes `log` lines in with the
// `diagnostic` ones. Both were confirmed against the pinned cargo-deny
// directly: stdout is empty under --format json, which a reader of the flag's
// name would not expect.
type denyMessage struct {
	Type   string         `json:"type"`
	Fields denyDiagnostic `json:"fields"`
}

// denyDiagnostic is one of cargo-deny's claims.
//
// It names a crate rather than a place. `labels` carries a line and a column
// but no filename, and points into a manifest cargo-deny synthesised for the
// crate rather than into any file in the tree — so it cannot locate anything
// and lydite does not read it. `graphs` is the dependency path that pulled the
// crate in, which is what a reader actually needs, and it becomes the
// finding's Detail.
type denyDiagnostic struct {
	Code     string      `json:"code"`
	Severity string      `json:"severity"`
	Message  string      `json:"message"`
	Graphs   []denyGraph `json:"graphs"`
}

// denyGraph is the root of one dependency path, nested through `parents`.
type denyGraph struct {
	Krate   denyKrate   `json:"Krate"`
	Parents []denyGraph `json:"parents"`
}

type denyKrate struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// denyFindings is every diagnostic as a claim on the lockfile line naming the
// crate it is about.
//
// Severity is carried unchanged and nothing is filtered on it. cargo-deny
// reports a `warning` for a crate with no licence field and an `error` for the
// same crate being unlicensed, and which of the two a policy makes fatal is
// the policy's business: the row's verdict stays cargo-deny's own exit status,
// and lydite reports every claim the tool made. A diagnostic whose severity
// lydite does not recognise is reported for the reason reportableBiome keeps
// an unknown category — a parser that silently drops what it does not
// recognise is how a gate stops gating.
func denyFindings(dir string, messages []denyMessage) []finding.Finding {
	lock := readCargoLock(dir)
	var out []finding.Finding
	for _, m := range messages {
		if m.Type != "diagnostic" {
			continue
		}
		d := m.Fields
		name, version := denySubject(d.Graphs)
		out = append(out, finding.Finding{
			Gate:     GateDeny,
			Path:     cargoLockFile,
			Line:     lock.Line(name, version),
			Rule:     d.Code,
			Severity: d.Severity,
			Message:  denyMessageText(d, name, version),
			Detail:   denyPaths(d.Graphs),
			// The check with the crate it is against. cargo-deny states no
			// advisory id, so its `code` — `unlicensed`, `banned` — is what
			// plays that part, and the crate is what separates one crate's
			// `unlicensed` from another's. See ADR 0032.
			Site: d.Code + "\x1f" + name + " " + version,
		})
	}
	finding.Number(out)
	return out
}

// denySubject is the crate a diagnostic is about.
//
// The first graph's root, because cargo-deny puts the offending crate there
// and its dependents in `parents`. A diagnostic carrying no graph names no
// crate, and answers empty — which locates it nowhere and is reported as such.
func denySubject(graphs []denyGraph) (name, version string) {
	if len(graphs) == 0 {
		return "", ""
	}
	return graphs[0].Krate.Name, graphs[0].Krate.Version
}

// denyMessageText is the claim with the crate it is about named in it.
//
// cargo-deny's own message sometimes names the crate and sometimes does not —
// "auditprobe = 0.0.0 is unlicensed" against "a valid license expression could
// not be retrieved for the crate" — and a claim that does not say what it is
// about cannot be read on its own in a comment listing twenty of them.
func denyMessageText(d denyDiagnostic, name, version string) string {
	if name == "" || strings.Contains(d.Message, name) {
		return d.Message
	}
	return d.Message + " — " + name + " " + version
}

// denyPaths is each dependency path that pulled the crate in, as the lines a
// reader sees.
//
// The path is what makes a ban or a licence claim actionable: the crate is
// rarely a direct dependency, and the edit that clears it is to whichever
// dependent pulled it in.
func denyPaths(graphs []denyGraph) []string {
	var out []string
	for _, g := range graphs {
		walkDenyGraph(g, nil, &out)
	}
	return out
}

// walkDenyGraph appends one line per root-to-leaf path through the parents.
func walkDenyGraph(g denyGraph, below []string, out *[]string) {
	path := append(below, strings.TrimSpace(g.Krate.Name+" "+g.Krate.Version))
	if len(g.Parents) == 0 {
		// Read outwards from the offending crate, which is the root here, so
		// a reader starts at what is wrong and walks to their own dependency.
		*out = append(*out, strings.Join(path, " ← "))
		return
	}
	for _, parent := range g.Parents {
		// Each branch gets the path so far by value. Siblings extend the same
		// prefix, and one branch must not be able to reach another's line
		// through a shared backing array.
		walkDenyGraph(parent, append([]string(nil), path...), out)
	}
}

// decodeDeny reads the NDJSON stream, skipping the lines that will not parse.
//
// A line that is not JSON is cargo-deny writing something the stream does not
// promise, and a malformed line costs only itself rather than every diagnostic
// after it.
func decodeDeny(r io.Reader) []denyMessage { return decodeNDJSON[denyMessage](r) }

// denyArgv is the invocation, as argv.
//
// --format comes before the subcommand: cargo-deny declares it on the
// top-level command, and `check --format json` is rejected outright. advisories
// is excluded — cargo-audit already covers RustSec CVEs, and running both would
// double-report them.
func denyArgv() []string {
	return []string{"deny", "--format", "json", "check", "licenses", "bans"}
}

// runDeny runs cargo-deny once, as data.
//
// --format json replaces cargo-deny's human output rather than copying it, so
// the JSON run is the only run: its exit status decides the row, and the crates
// a developer reads are the Detail report() prints from the claims.
// [lydite:exclude_from_coverage][the proving ground installs and runs the
// pinned cargo-deny on a bare checkout; a unit test here would run the
// machine's own, and what is lydite's to get right is the invocation, which
// denyArgv states and TestDenyArgvIsOneJSONRunWithFormatBeforeTheSubcommand
// asserts — everything done with the output is denyResult, which the captured
// reports test directly]
func runDeny(ctx context.Context, dir string, env []string, bin string) executil.Result {
	// RunQuiet is the one runner that keeps stdout and stderr apart, which this
	// stream needs: cargo-deny writes its NDJSON to stderr.
	return denyResult(dir, named(GateDeny, executil.RunQuietEnv(ctx, dir, env, bin, denyArgv()...)))
}

// denyResult is one run read as claims, with the Detail a failing row needs
// rendered from them.
//
// The NDJSON arrives on stderr and stdout stays empty under --format json, so
// Output is taken from Stderr: Output is what the run's log is written from, and
// the log is meant to hold the report.
//
// cargo-deny's exit status stays the verdict and a finding count never becomes
// one. The status is a bitmask of which checks failed — licenses 4, bans 2, 6
// for both — so what decides the row is Ok, never a comparison against 1, and
// cargo-deny can set it for reasons no diagnostic in the stream states.
func denyResult(dir string, r executil.Result) executil.Result {
	r.Output = r.Stderr
	r.Findings = denyFindings(dir, decodeDeny(strings.NewReader(r.Stderr)))
	if r.Ok() {
		return r
	}
	if detail := findingsDetail(r.Findings); detail != "" {
		r.Detail = detail
		return r
	}
	r.Detail = unreadable(GateDeny, r.Err)
	return r
}
