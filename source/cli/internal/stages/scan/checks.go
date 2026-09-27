package scanstages

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/shell"
	"lydite/lydite/internal/typescript"
)

// RunChecksIn is the plan whose Scan entries get their language checks, and
// what each check's claims and warnings need.
type RunChecksIn struct {
	Plan []Planned
	// Changed is, per path from the scan root, the lines the change touched,
	// which decides each claim's anchor. Empty for a scan with no diff base.
	Changed     map[string][]int
	Environment Environment
	// Diagnostics is where the declared-environment warning is written, at
	// the moment it arises — before that component's checks stream their own
	// output, never returned for a caller to print later.
	Diagnostics io.Writer
}

// Checked is what one component's language checks answered.
type Checked struct {
	// Results is each check's outcome, labelled for the component: its name
	// carries the component's (`gosec(cli)`), and each of its findings names
	// the component and a path from the scan root.
	Results []executil.Result
	// Findings is every located claim Results made, each naming the row that
	// made it and anchored against the lines the change touched.
	Findings []finding.Finding
	// Crashes is each check whose findings are not a complete answer, as the
	// bare gate and the component.
	Crashes []finding.Crash
}

// RunChecksOut is one entry per entry of the plan it was given, at the same
// index. An entry that is not Scan is the zero Checked, so one component's
// checks can never be read as another's.
type RunChecksOut struct {
	Checks []Checked
}

// RunChecks runs each Scan entry's language checks, in plan order, under the
// environment and toolchain key the plan resolved for it.
//
// One component at a time, every language in the one loop: scanners stream
// their output live, and running one language's components ahead of
// another's would reorder that stream whenever a declaration interleaves
// languages.
//
// A check's outcome is data in the Out, never this stage's error. The one
// error is a plan entry naming a language no check here handles, which is
// refused before any check runs.
func RunChecks(ctx context.Context, in RunChecksIn) (RunChecksOut, error) {
	for _, p := range in.Plan {
		if p.Disposition != Scan {
			continue
		}
		if _, err := checksFor(p.Lang); err != nil {
			return RunChecksOut{}, fmt.Errorf("component %s: %w", p.Component.Name, err)
		}
	}
	out := make([]Checked, len(in.Plan))
	for i, p := range in.Plan {
		if p.Disposition != Scan {
			continue
		}
		dirs, vars := in.Environment.Declared(p.Component)
		warnDeclaredEnv(in.Diagnostics, p.Component.Name, dirs, vars, p.Env.Check)

		check, _ := checksFor(p.Lang)
		results := check(ctx, p.Dir, p.Env, p.ToolchainKey)
		labelledResults := labelled(results, p.Component.Name, p.Component.Dir)
		out[i] = Checked{
			Results: labelledResults,
			// Read before labelled's suffix makes the gate prose: this is the
			// one place the bare gate and the component are both still
			// structured.
			Crashes:  crashesOf(results, p.Component.Name),
			Findings: anchoredFindings(labelledResults, in.Changed),
		}
	}
	return RunChecksOut{Checks: out}, nil
}

// languageChecks runs one language's checks in a component's directory.
type languageChecks func(ctx context.Context, dir string, env executil.Env, toolchainKey string) []executil.Result

// checksFor is the checks a language runs. Every language with checks is
// named, and any other is refused rather than handed another language's
// checks: those read none of this language's source, and would report
// finding nothing in it.
func checksFor(lang runner.Lang) (languageChecks, error) {
	switch lang {
	case runner.Go:
		return golang.Check, nil
	case runner.Rust:
		return func(ctx context.Context, dir string, env executil.Env, _ string) []executil.Result {
			return rust.Check(ctx, dir, env)
		}, nil
	case runner.TypeScript:
		return func(ctx context.Context, dir string, env executil.Env, _ string) []executil.Result {
			return typescript.Check(ctx, dir, env)
		}, nil
	case runner.Shell:
		return func(ctx context.Context, dir string, env executil.Env, _ string) []executil.Result {
			return shell.Check(ctx, dir, env)
		}, nil
	default:
		return nil, fmt.Errorf("%q is planned for scanning and has no language checks", lang)
	}
}

// warnDeclaredEnv names the environment a component's checks are composed
// with, on the writer reserved for what the scan ran under. A component that
// composed nothing says nothing.
//
// A warning and not a row: a declaration is the repository's own configuration
// of its own scan, which ADR 0020 records as legitimate influence, and no
// status fits it — a `pass` asserts something nobody measured, a `fail` turns a
// declaration the repository is entitled to make into a gate, and an
// `unmeasured` spends the tag that exists to be noticed on a component that
// scanned perfectly well. Naming it is the whole of what lydite owes here, and
// editing the file is already a referral disqualifier.
func warnDeclaredEnv(w io.Writer, name string, dirs, vars, composed []string) {
	names := declaredEnvNames(dirs, vars, composed)
	if len(names) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: %s's checks are composed with the environment %s declares: %s — names only, because a declared value can carry a credential\n",
		name, component.FileName, strings.Join(names, ", "))
}

// steeringEnv is the variables lydite knows change what a check does — which
// files it compiles, which vulnerability database it consults, which registry
// or dependency it resolves against — rather than an ordinary variable a suite
// merely happens to read. declaredEnvNames marks a declared name found here so
// a reader scanning a long list finds the two or three worth a second look.
//
// This list is best-effort and is not a security boundary. It may rot as
// scanners gain new variables, and rotting is harmless: a name that falls off
// it is still reported, just without the mark. Nothing reads this set to
// decide whether to fail, refuse or gate anything — the mark exists only to
// help a reader's eye, never to filter.
var steeringEnv = map[string]bool{
	"GOFLAGS":            true,
	"GOVULNDB":           true,
	"GOPRIVATE":          true,
	"RUSTFLAGS":          true,
	"RUSTC_WRAPPER":      true,
	"CARGO_BUILD_TARGET": true,
	"NODE_OPTIONS":       true,
}

// declaredEnvNames is the names of what a component's declaration contributed
// to composed — dirs and vars being that declaration as Environment.Declared
// splits it — in the order Declared lists them, with a folded PATH last.
//
// Names, never values, and no future refinement of this prints a value. A
// declared value is arbitrary text the repository controls, and the places a
// repository puts a token are exactly the places that look like configuration:
// a registry URL with credentials in it, a `*_TOKEN` a suite needs, a DSN. This
// line reaches a CI log, which on a public repository is world-readable.
// Redacting rather than omitting is the same object as an allowlist — a pattern
// list that has to be complete to be safe, which publishes the secret it did
// not recognise while reading as though it had checked. What a reader needs is
// that the name was set for this component, and the value is in the file under
// review.
//
// It reads the composed environment and not the declaration alone, because the
// two differ in ways that matter. A declared PATH is not a variable of the
// child at all — composition folds it into the single composed PATH entry,
// behind the inherited one — so it is named as the path extension it is. A
// declared key the resolved toolchain also sets is cancelled, since the
// toolchain's variables compose last; naming it plainly would report a
// steering variable that never reached the check.
//
// Every declared name is reported unconditionally; a name also found in
// steeringEnv carries an additional mark, and the two annotations compose
// into one parenthetical rather than one clobbering the other.
func declaredEnvNames(dirs, vars, composed []string) []string {
	if len(dirs) == 0 && len(vars) == 0 {
		return nil
	}
	// The last occurrence of a key is the one the child reads, which is how
	// the toolchain's variables win.
	effective := map[string]string{}
	for _, kv := range composed {
		if k, v, ok := strings.Cut(kv, "="); ok {
			effective[k] = v
		}
	}
	var names []string
	for _, kv := range vars {
		k, v, _ := strings.Cut(kv, "=")
		mark := ""
		if steeringEnv[k] {
			mark = "steers a check"
		}
		if effective[k] != v {
			if mark != "" {
				mark += ", overridden by the resolved toolchain"
			} else {
				mark = "overridden by the resolved toolchain"
			}
		}
		if mark != "" {
			names = append(names, k+" ("+mark+")")
			continue
		}
		names = append(names, k)
	}
	if len(dirs) > 0 {
		names = append(names, "PATH (appended after lydite's own)")
	}
	return names
}

// crashesOf is each result whose findings are not a complete answer, as the
// bucket they would have been recorded in: the bare gate, and the component it
// scanned or none for a root-scoped gate.
//
// Read before labelled, whose suffix makes the gate prose; a crash is data a
// consumer buckets by, never a label it parses.
func crashesOf(results []executil.Result, component string) []finding.Crash {
	var out []finding.Crash
	for _, r := range results {
		if r.Crashed {
			out = append(out, finding.Crash{Gate: r.Name, Component: component})
		}
	}
	return out
}

// labelled attributes each of a component's results to the component that
// produced them — `gosec(cli)`, `cargo clippy(api)` — in the row's name and in
// every located claim beneath it.
//
// The name and never the directory: component.validate enforces unique names
// and not unique directories, so the name is the only one of the two unique
// by construction. It also matches how `lydite test` labels its own rows, so
// a scan row and a test row about one component carry the same token.
//
// A finding's path is rebased onto the scan root at the same time, because a
// check runs inside its component and reports paths relative to there, while
// every other producer names a file from the root. One file named from two
// roots is two claims, and only one of them can be anchored.
func labelled(results []executil.Result, component, dir string) []executil.Result {
	out := make([]executil.Result, 0, len(results))
	for _, r := range results {
		r.Name += "(" + component + ")"
		findings := make([]finding.Finding, len(r.Findings))
		for i, f := range r.Findings {
			f.Component = component
			f.Path = path.Join(dir, f.Path)
			findings[i] = f
		}
		r.Findings = findings
		out = append(out, r)
	}
	return out
}

// findingsOf is every check's located claims, each naming the row that made
// it.
//
// The label is the check's own name, taken from the same field a row's label
// is rendered from rather than rebuilt beside it. A finding whose row label
// does not match the row's is one the standing comment cannot partition: it
// would be rendered there as well as on the line a thread anchors it to, or
// dropped from both.
func findingsOf(results []executil.Result) []finding.Finding {
	var out []finding.Finding
	for _, r := range results {
		for _, f := range r.Findings {
			f.Row = r.Name
			out = append(out, f)
		}
	}
	return out
}

// anchoredFindings is findingsOf's claims, each anchored against the lines the
// change touched.
//
// Given results labelled already: changed is keyed by path from the scan root,
// and a claim still named from inside its component would match nothing in it.
func anchoredFindings(results []executil.Result, changed map[string][]int) []finding.Finding {
	found := findingsOf(results)
	finding.Anchored(found, changed)
	return found
}
