package rust

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/licence"
)

// DenyConfigFile is the name a component's own cargo-deny configuration
// carries. A row names it, because the edit that answers a licence claim
// decided by it lands there rather than in lydite's own configuration.
const DenyConfigFile = "deny.toml"

// denyConfigFiles are the names cargo-deny reads a crate's own configuration
// from, in the order it looks for them.
var denyConfigFiles = []string{DenyConfigFile, "." + DenyConfigFile}

// denySeverityError is the severity cargo-deny sets its exit status from. A
// warning is a claim the configuration it ran under does not fail on.
const denySeverityError = "error"

// denyUnlicensed is the code for a crate stating no licence expression at all.
const denyUnlicensed = "unlicensed"

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
	Labels   []denyLabel `json:"labels"`
}

// denyLabel is one of a diagnostic's underlined spans.
//
// The span text is the only place a licence rejection states the expression it
// refused — `MPL-2.0` against cbindgen — and that expression is half of the
// pair the licence gate keys on. The line and column beside it are not read,
// for the reason denyDiagnostic states: they point at no file in the tree.
type denyLabel struct {
	Span string `json:"span"`
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
//
// bans alone, under whatever configuration the component itself has. licenses
// is its own invocation under the policy this repository states, because a
// --config pointed at a generated file replaces cargo-deny's whole document —
// a component's [bans], [advisories] and [sources] tables with it — so a
// combined run would silently stop enforcing a ban list somebody curated.
func denyArgv() []string {
	return []string{"deny", "--format", "json", "check", "bans"}
}

// denyLicencesArgv is the licence check's own invocation, as argv.
//
// An empty config is no --config at all, which is the component's own deny.toml
// deciding — the source PolicyFromConsumer names.
func denyLicencesArgv(config string) []string {
	argv := []string{"deny", "--format", "json"}
	if config != "" {
		argv = append(argv, "--config", config)
	}
	return append(argv, "check", "licenses")
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

// PolicySource is the document that decided which licences a component's
// dependencies may carry.
//
// It reaches the row because the verdict cannot say it. A row gating nothing
// under a policy this repository never stated and a row gating nothing because
// the component brought its own deny.toml are answered by edits to different
// files, and a reader told only that nothing was gated can find neither.
type PolicySource string

const (
	// PolicyFromLydite is licence.policy.allow, generated into cargo-deny's
	// [licenses] table. It is the one source the delta gate runs under: the
	// generated document is identical at the branch and at the merge-base, so
	// what the comparison measures is the change rather than an edit to the
	// policy itself.
	PolicyFromLydite PolicySource = "lydite"
	// PolicyFromConsumer is the component's own deny.toml, which decides the
	// licence check where this repository states no policy. It gates
	// absolutely: the file is the component's own, cargo-deny has always
	// evaluated it whole, and every crate it rejects fails the row.
	PolicyFromConsumer PolicySource = "consumer"
	// PolicyFromNone is neither, and no licence check runs at all: cargo-deny's
	// own default rejects every licence, MIT included, so a run under it fails
	// every component for a reason nothing in the repository chose.
	PolicyFromNone PolicySource = "none"
)

// PolicyFor is where dir's licence policy comes from.
//
// The repository's own policy wins over a component's deny.toml. It is the one
// document every language reads, and a component able to override it would make
// a Rust row answer a question no Go row is asked.
func PolicyFor(dir string, policy licence.Policy) PolicySource {
	if policy.Configured() {
		return PolicyFromLydite
	}
	if denyConfig(dir) != "" {
		return PolicyFromConsumer
	}
	return PolicyFromNone
}

// denyConfig is the component's own cargo-deny configuration, or empty where it
// has none.
func denyConfig(dir string) string {
	for _, name := range denyConfigFiles {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// denyLicencePolicy is cargo-deny's [licenses] table generated from the
// repository's stated policy.
//
// unused-allowed-license is set to allow, and that is not cosmetic. At its
// default, every allowed licence the dependency graph does not happen to use
// becomes a `license-not-encountered` warning whose graphs array is empty — so
// it names no crate, locates at line 0, and shares one site with every other
// one. A repository-wide allow-list is by construction broader than any single
// component's graph, so the default is wrong for every generated config.
func denyLicencePolicy(policy licence.Policy) string {
	allow := policy.Allow()
	quoted := make([]string, 0, len(allow))
	for _, id := range allow {
		// Quoted rather than spliced. An identifier arrives out of the
		// repository's own configuration, and one carrying a quote would
		// otherwise write a document cargo-deny refuses to parse — a licence
		// check that cannot run, reported as one nothing was wrong with.
		quoted = append(quoted, strconv.Quote(id))
	}
	return "[licenses]\nallow = [" + strings.Join(quoted, ", ") + "]\nunused-allowed-license = \"allow\"\n"
}

// writeDenyLicencePolicy writes the generated table where cargo-deny can read
// it, answering the path and the removal the caller defers.
func writeDenyLicencePolicy(policy licence.Policy) (path string, remove func(), err error) {
	f, err := os.CreateTemp("", "lydite-deny-*.toml")
	if err != nil {
		return "", func() {}, fmt.Errorf("writing the generated licence policy: %w", err)
	}
	path = f.Name()
	remove = func() { _ = os.Remove(path) }
	if _, err := io.WriteString(f, denyLicencePolicy(policy)); err != nil {
		_ = f.Close()
		remove()
		return "", func() {}, fmt.Errorf("writing the generated licence policy: %w", err)
	}
	if err := f.Close(); err != nil {
		remove()
		return "", func() {}, fmt.Errorf("writing the generated licence policy: %w", err)
	}
	return path, remove, nil
}

// licenceDependencies is each crate cargo-deny rejected, as the pairs the gate
// compares.
//
// Severity decides which diagnostics are rejections, not the code. cargo-deny's
// exit status is set by its errors alone, and one crate arrives as both — a
// warning that no expression could be read and an error that it is unlicensed —
// so a rule reading codes reports the same crate twice and misses every
// rejection a later cargo-deny spells differently.
//
// A diagnostic naming no crate is no pair. `license-not-encountered` names an
// allow-list entry rather than a dependency, and a pair with an empty package
// grandfathers nothing and locates nowhere.
func licenceDependencies(messages []denyMessage) []licence.Dependency {
	var out []licence.Dependency
	for _, m := range messages {
		if m.Type != "diagnostic" || m.Fields.Severity != denySeverityError {
			continue
		}
		name, version := denySubject(m.Fields.Graphs)
		if name == "" {
			continue
		}
		out = append(out, licence.Dependency{
			Package: name,
			Version: version,
			Licence: denyLicenceOf(m.Fields),
		})
	}
	return out
}

// denyLicenceOf is the licence a rejection is about.
//
// The span cargo-deny underlined, which is the expression it refused. An
// unlicensed crate has no expression to underline and is Unknown — a pair like
// any other, so a dependency whose licence nobody could read grandfathers
// rather than dropping out of the set the gate compares.
func denyLicenceOf(d denyDiagnostic) string {
	if d.Code == denyUnlicensed {
		return licence.Unknown
	}
	for _, l := range d.Labels {
		if span := strings.TrimSpace(l.Span); span != "" {
			return span
		}
	}
	return licence.Unknown
}

// denyLicenceSet is one licences run read as the set it measured.
//
// A non-zero status with rejections in the stream is the measurement: the
// status says the licence check failed, and what the gate reports is the delta
// against the merge-base rather than the status. A non-zero status with nothing
// in the stream is a run that measured nothing at all — a config cargo-deny
// would not read, a lockfile it could not resolve — and it answers an error, so
// the row is unmeasured rather than a pass over an empty set.
func denyLicenceSet(dir string, r executil.Result) (licence.Set, error) {
	deps := licenceDependencies(decodeDeny(strings.NewReader(r.Stderr)))
	if !r.Ok() && len(deps) == 0 {
		return licence.Set{}, fmt.Errorf("cargo-deny check licenses in %s: %w%s", dir, r.Err, firstLine(r.Stderr))
	}
	return licence.NewSet(deps...), nil
}

// firstLine is the leading line of a tool's diagnostics, ready to append to an
// error. Empty stays empty, so an error over a silent failure does not end in a
// dangling separator.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return ": " + s
}

// LicenceSet is the component's non-conforming crates, with the document that
// decided them.
//
// The set is only ever the rejected crates and never a full inventory, which is
// what makes recomputing it at the merge-base affordable: it is what cargo-deny
// already hands back. policy.Reject plays no part — cargo-deny evaluated the
// generated table itself, and a second evaluation here over an expression
// already refused would only be a chance for the two to disagree.
//
// Nothing runs under PolicyFromNone. cargo-deny's default rejects every licence,
// so the run would report the whole dependency graph against a policy nothing in
// the repository chose.
func LicenceSet(ctx context.Context, dir string, env executil.Env, policy licence.Policy) (licence.Set, PolicySource, error) {
	source := PolicyFor(dir, policy)
	if source == PolicyFromNone {
		return licence.Set{}, source, nil
	}
	bin, err := ensure(ctx, env.Install, "cargo-deny", cargoDenyVersion)
	if err != nil {
		return licence.Set{}, source, err
	}
	var config string
	if source == PolicyFromLydite {
		path, remove, err := writeDenyLicencePolicy(policy)
		if err != nil {
			return licence.Set{}, source, err
		}
		defer remove()
		config = path
	}
	// RunQuiet keeps stdout and stderr apart, which this stream needs:
	// cargo-deny writes its NDJSON to stderr.
	set, err := denyLicenceSet(dir, executil.RunQuietEnv(ctx, dir, env.Check, bin, denyLicencesArgv(config)...))
	return set, source, err
}

// LicenceFindings is each introduced pair as a claim on the lockfile stanza
// naming its crate.
//
// The lockfile rather than Cargo.toml, for the reason cargoLockFile states: a
// crate reached transitively is named by no manifest line and is in the lockfile
// by construction.
//
// The site is the pair rather than the text of that line, the departure ADR 0032
// already makes for a dependency advisory and for the same reason: a crate
// offering two non-conforming licences is two claims against one stanza, and a
// site read from the line would make them one and drop the second.
func LicenceFindings(dir string, pairs []licence.Dependency) []finding.Finding {
	lock := readCargoLock(dir)
	out := make([]finding.Finding, 0, len(pairs))
	for _, d := range pairs {
		out = append(out, finding.Finding{
			Gate:     GateLicence,
			Path:     cargoLockFile,
			Line:     lock.Line(d.Package, d.Version),
			Rule:     d.Licence,
			Severity: licence.Gate,
			Message:  licenceMessage(d),
			Site:     d.Key().Site(),
		})
	}
	finding.Number(out)
	return out
}

// licenceMessage is the one-line claim: which crate, at which version, under
// which licence.
func licenceMessage(d licence.Dependency) string {
	msg := d.Package
	if d.Version != "" {
		msg += " " + d.Version
	}
	return msg + " is " + d.Licence + ", which the licence policy does not allow"
}

// LicenceCheck is the gate's answer for one Rust component: the crates
// cargo-deny rejects under the document that decided them, with the pairs the
// verdict is about as located claims.
func LicenceCheck(ctx context.Context, dir string, env executil.Env, policy licence.Policy, base licence.Base) (licence.Comparison, PolicySource, []finding.Finding, error) {
	current, source, err := LicenceSet(ctx, dir, env, policy)
	if err != nil {
		return licence.Comparison{}, source, nil, err
	}
	comparison, found := licenceVerdict(dir, policy, source, current, base)
	return comparison, source, found, nil
}

// licenceVerdict is what a measured set means, under the document that decided
// it, with the claims that accompany a failing row.
//
// The two gating sources gate differently because they were stated by different
// people. This repository's policy is org-wide and arrives on a component that
// already ships whatever it ships, so it gates on the delta against the
// merge-base: applied absolutely it would fail every adopting repository on its
// first run, over licences nobody in that change chose. A component's own
// deny.toml is configuration its authors wrote for themselves and cargo-deny
// evaluates whole every time it runs, so its verdict is absolute — there is no
// base in it, and every crate it rejected is a claim.
//
// PolicyFromNone ran nothing at all, and the empty set it carries measures
// nothing: a verdict over it would be a gate that could not run rendering as one
// that ran and found nothing. Any source this does not recognise falls the same
// way, for the same reason.
//
// Findings accompany a failing verdict alone. Every other verdict gates nothing,
// and a claim published under one would ask an author to answer for a crate the
// gate has decided nothing about.
func licenceVerdict(dir string, policy licence.Policy, source PolicySource, current licence.Set, base licence.Base) (licence.Comparison, []finding.Finding) {
	switch source {
	case PolicyFromLydite:
		comparison := licence.Compare(policy, current, base)
		if comparison.Verdict != licence.VerdictFail {
			return comparison, nil
		}
		return comparison, LicenceFindings(dir, comparison.Pairs)
	case PolicyFromConsumer:
		pairs := current.Dependencies()
		if len(pairs) == 0 {
			return licence.Comparison{Verdict: licence.VerdictPass}, nil
		}
		return licence.Comparison{Verdict: licence.VerdictFail, Pairs: pairs}, LicenceFindings(dir, pairs)
	case PolicyFromNone:
	}
	return licence.Comparison{Verdict: licence.VerdictNotConfigured}, nil
}
