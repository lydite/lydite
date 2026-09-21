package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/coverage"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/typescript"
	"lydite/lydite/internal/ui"
)

func newScanCmd() *cobra.Command {
	var dir, diffBase, baseBranch string
	var asJSON, noColor bool
	cmd := &cobra.Command{
		Use: "scan",
		// A non-zero verdict is an answer, not a misuse of the command and
		// not a malfunction. Cobra prints usage and an "Error:" line for any
		// error a RunE returns, which would bury the report under the flag
		// list every time a gate failed. main owns error reporting.
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Run code-quality and security checks for every declared component",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			// Started here rather than in report(), so the duration on the
			// verdict line covers the scan and not just its rendering.
			streamDiagnostics(asJSON)
			rep := ui.NewReport("scan")

			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}

			file, err := component.Load(dir)
			if err != nil {
				return err
			}
			// An error rather than a row, because there is nothing to report
			// on: scan runs the checks each component's language implies, so
			// a repository that declares none would be scanned by nothing at
			// all while the job stayed green — a security scan that silently
			// stopped. Declaring a component is work the author can do, and
			// naming the file is what makes it one step.
			if len(file.Components) == 0 {
				return fmt.Errorf("no components declared in %s: scan runs the checks each component's language implies, so declare what this repository builds",
					filepath.Join(dir, filepath.FromSlash(component.FileName)))
			}

			// Before anything runs, not after every check has. Its failure is
			// an error rather than a row, so resolving it late would discard
			// every finding already collected — minutes of clippy and gosec
			// traded for one sentence about a shallow checkout, which is the
			// reason typescript.Check stopped returning an error too.
			baseSHA, err := resolveDiffBase(ctx, dir, diffBase, baseBranch)
			if err != nil {
				return err
			}
			// The lines the change touched, so a claim on one becomes a
			// review thread rather than a row in the standing comment. Asked
			// once and partitioned by path afterwards: every check measures
			// the same range, and asking git per component is the same answer
			// computed N times.
			//
			// Empty without --diff-base, which leaves every claim
			// unanchorable — the honest answer for a scan over a whole
			// repository, which reaches no change at all.
			var changed map[string][]int
			if baseSHA != "" {
				changed, err = coverage.ChangedLines(ctx, dir, baseSHA)
				if err != nil {
					return err
				}
			}

			// Before anything shells out to cargo/go/biome: make sure each
			// component's language toolchain is present at the version its
			// own directory declares. lydite pins every tool it runs, and
			// this is the toolchain it runs them with.
			envs, err := ensureToolchains(ctx, cmd, dir, cfg, scanUnits(file, cfg))
			if err != nil {
				return err
			}

			warnUnscanned(ctx, cmd.ErrOrStderr(), dir, file, cfg)

			// One merge-base worktree for the whole scan, checked out the
			// first time a component's licence gate asks for a set and removed
			// once every component has. A checkout per component would be the
			// same commit extracted once per declaration.
			licenceTree := newLicenceBaseTree(dir, baseSHA)
			defer licenceTree.close(ctx)

			// A component's checks are keyed by what they actually look at:
			// the directory they run in and the language they run for.
			// component.validate enforces unique names and not unique
			// directories, so two components over one root are legitimate —
			// `lydite test` runs both suites and its scheduler serialises
			// them. Their scanners would read the identical tree twice and
			// report every finding twice, under two labels, which is time
			// spent to make a report harder to read.
			scanned := map[string]string{}
			for _, c := range file.Components {
				lang := langOf(c)
				if lang == "" {
					// Said out loud rather than skipped. A component lydite
					// cannot derive a language for is one nothing scans, and
					// dropping it in silence reads exactly like a component
					// that was scanned and found clean.
					rep.Add(ui.Row{
						Status: ui.StatusUnmeasured,
						Label:  "scan(" + c.Name + ")",
						Value:  "not scanned — a component declaring its own command implies no language",
					})
					continue
				}
				// A language turned off in .lydite/config.yml is one whose
				// checks never run, so its components produce no rows at all
				// — a row per opted-out component trains readers to skip the
				// tag that exists to be noticed.
				if !langEnabled(lang, cfg) {
					continue
				}
				cdir := filepath.Join(dir, filepath.FromSlash(c.Dir))
				// The component's declared environment as well as its
				// toolchain, composed exactly as `lydite test` composes it: a
				// Rust component declaring SQLX_OFFLINE or a Go one declaring
				// CGO_ENABLED needs it to build at all. Install carries none
				// of it — see executil.Env. No invocation directories, since
				// scan runs lydite's own pinned tools by absolute path.
				tc := envs.For(c.Name)
				env := executil.Env{
					Check:   childEnv(tc, c, runner.Invocation{}),
					Install: tc.Environ(),
				}
				// Keyed on the environment as well as the directory and the
				// language, because that is the rest of what decides what a
				// check sees. Two components over one root declaring the same
				// environment are one scan, and the first in declaration order
				// carries the rows — either name is honest, and declaration
				// order does not vary between runs. Two declaring *different*
				// environments are two builds: dropping one would scan the
				// other's tree with an environment it never asked for, and a
				// component declaring the CGO_ENABLED or SQLX_OFFLINE its
				// language needs would fail on a build its declaration exists
				// to make work — under the other component's name.
				key := string(lang) + "\x00" + filepath.Clean(cdir) + "\x00" + strings.Join(env.Check, "\x00")
				if by, done := scanned[key]; done {
					// Said, not dropped. A consumer keying rows by component
					// name would otherwise lose this one with nothing to
					// separate "already covered" from "never declared" — the
					// same reason a raw-command component gets a row.
					rep.Add(ui.Row{
						Status: ui.StatusUnmeasured,
						Label:  "scan(" + c.Name + ")",
						Value:  "not scanned separately — same directory and environment as " + by,
					})
					continue
				}
				scanned[key] = c.Name
				warnDeclaredEnv(cmd.ErrOrStderr(), c, env.Check)

				var results []executil.Result
				switch lang {
				case runner.Rust:
					results = rust.Check(ctx, cdir, env)
				case runner.TypeScript:
					results = typescript.Check(ctx, cdir, env)
				case runner.Go:
					results = golang.Check(ctx, cdir, env, tc.Key())
				}
				record(rep, dir, changed, labelled(results, c.Name, c.Dir))
				switch lang {
				case runner.Go:
					recordGoLicence(ctx, rep, licenceTree, c, cdir, env.Check, cfg, changed)
				case runner.Rust:
					recordRustLicence(ctx, rep, licenceTree, c, cdir, env, cfg, changed)
				case runner.TypeScript:
					recordTypeScriptLicence(ctx, rep, licenceTree, c, cdir, cfg, changed)
				}
			}

			var results []executil.Result
			if cfg.Semgrep.Enabled {
				results = append(results, semgrep.Check(ctx, dir, cfg.Semgrep.Config, semgrepBase(baseSHA), cmd.ErrOrStderr()))
			}
			// Root-scoped, like Semgrep and unlike every language check: a
			// secret scanner reads bytes rather than a build graph, and the
			// files most likely to carry a credential — a workflow, a
			// compose file, an .env somebody added — belong to no component
			// at all. So it runs once over the scan root and its claims name
			// no component.
			//
			// Given no diff base. gitleaks has no --baseline-commit, so the
			// row fails on every secret in the tree as gosec's does, and the
			// anchor is what decides which claims become threads on the
			// change and which are the pre-existing debt.
			if cfg.Secrets.Enabled {
				results = append(results, secrets.Check(ctx, dir))
			}

			// A run in which no check ran says so, in a row of its own.
			//
			// Counting rows is not the test for that: a raw-command component
			// and a deduplicated one each add an `unmeasured` row, so a
			// repository whose every component declares a raw command, with
			// Semgrep off, produces a document of amber rows and has executed
			// nothing. Each opt-out is the repository's to make and none is
			// reported on its own; all of them together is a different fact.
			//
			// The row is `unmeasured`, so the verdict stays `pass` and the
			// exit code 0. That is the grammar's rule and not an oversight
			// here: refer, unmeasured and dropped all render amber and only
			// refer votes, which is what lets a check that did not run be
			// visibly distinct from one that passed without turning every
			// opt-out into a failing build. Every state reached here is one
			// the repository asked for in its own configuration — a language
			// switched off, a component declaring its own command — so failing
			// it would be lydite refusing a configuration it was handed. What
			// must not happen is silence, and the row and its `--json` status
			// are what remove it.
			if !ranAnyCheck(rep) && len(results) == 0 {
				rep.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "scan",
					Value:  "nothing ran — no declared component has a language lydite checks, or every one of them is disabled",
				})
			}

			return report(cmd, rep, dir, changed, results, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory to scan")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	cmd.Flags().StringVar(&diffBase, "diff-base", "", `only report findings introduced since this commit ("auto" resolves the merge-base with the base branch); empty scans everything`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	return cmd
}

// warnDeclaredEnv names the environment a component's checks are composed
// with, on the writer the caller reserves for what the scan ran under. A
// component that composed nothing says nothing.
//
// A warning and not a row: a declaration is the repository's own configuration
// of its own scan, which ADR 0020 records as legitimate influence, and no
// status fits it — a `pass` asserts something nobody measured, a `fail` turns a
// declaration the repository is entitled to make into a gate, and an
// `unmeasured` spends the tag that exists to be noticed on a component that
// scanned perfectly well. Naming it is the whole of what lydite owes here, and
// editing the file is already a referral disqualifier.
func warnDeclaredEnv(w io.Writer, c component.Component, composed []string) {
	names := declaredEnvNames(c, composed)
	if len(names) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: %s's checks are composed with the environment %s declares: %s — names only, because a declared value can carry a credential\n",
		c.Name, component.FileName, strings.Join(names, ", "))
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
// to composed, in the order env sorts them, with a folded PATH last.
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
// It reads the composed environment and not the declaration, because the two
// differ in ways that matter. A declared PATH is not a variable of the child at
// all — childEnv folds it into the single composed PATH entry, behind the
// inherited one — so it is named as the path extension it is. A declared key
// the resolved toolchain also sets is cancelled, since the toolchain's
// variables compose last; naming it plainly would report a steering variable
// that never reached the check.
//
// Every declared name is reported unconditionally; a name also found in
// steeringEnv carries an additional mark, and the two annotations compose
// into one parenthetical rather than one clobbering the other.
func declaredEnvNames(c component.Component, composed []string) []string {
	dirs, vars := splitPath(env(c))
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

// scanUnits is what each declared component needs a toolchain for, in
// declaration order.
//
// Only components whose language is enabled: `enabled: false` says lydite
// runs no check over that language's code, so provisioning its toolchain
// would download a compiler nothing is going to invoke. A component that
// declares its own command implies no language and needs nothing.
func scanUnits(file component.File, cfg config.Config) []toolchain.Unit {
	var out []toolchain.Unit
	for _, c := range file.Components {
		lang := langOf(c)
		if lang == "" || !langEnabled(lang, cfg) {
			continue
		}
		out = append(out, toolchain.Unit{Name: c.Name, Lang: lang, Dir: c.Dir})
	}
	return out
}

// anyLanguageDeclared reports whether some component names a runner, and so
// implies source in a language lydite knows.
func anyLanguageDeclared(file component.File) bool {
	for _, c := range file.Components {
		if langOf(c) != "" {
			return true
		}
	}
	return false
}

// langEnabled reports whether .lydite/config.yml leaves one language's checks
// switched on.
func langEnabled(l runner.Lang, cfg config.Config) bool {
	switch l {
	case runner.Rust:
		return cfg.Rust.Enabled
	case runner.TypeScript:
		return cfg.TypeScript.Enabled
	case runner.Go:
		return cfg.Go.Enabled
	}
	return false
}

// scannerGates is the gates a language's checks report their findings under,
// which is what makes a count of nought distinguishable from a gate that never
// applied to a component at all.
//
// Each language package names its own, so the set cannot drift from the checks
// that package runs. Derived from the language rather than from the rows a scan
// wrote, for the reason internal/finding exists at all: a row's label is prose
// — `gosec(cli)` — and reading a gate and a component back out of it is the
// text-scraping the findings channel was built to remove.
func scannerGates(lang runner.Lang) []string {
	switch lang {
	case runner.Rust:
		return rust.FindingGates()
	case runner.TypeScript:
		return typescript.FindingGates()
	case runner.Go:
		return golang.FindingGates()
	}
	return nil
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

// warnUnscanned names the source no component's checks reach, so a narrowing
// scan is not a silent one.
//
// The orphan gate is what normally makes a declared list safe to rely on, and
// it cannot answer this: it asks whether any component *contains* a file, and
// a scanner is per language — a component rooted at `.` contains every path in
// the repository, so a Go component at the root leaves TypeScript beside it
// orphaning nothing while gosec never looks at it. The gate also belongs to
// `lydite test`, which a consumer can run scan without.
//
// A warning and not a row. What a repository should do about it is declare a
// component or write the exclude, which is `lydite test`'s gate to demand;
// scan's job is only to stop the narrowing being invisible. Stderr, because
// stdout carries the report and under --json a document a sentence would make
// unparseable.
//
// Outside a git repository there is no question to answer, which is the shape
// orphanRow already has for that case. Every other failure is said out loud,
// because this is the only thing standing between a scan that narrowed and a
// scan that narrowed silently — with one exception, stated at the branch that
// makes it: git listing no source at all is the ordinary state of a repository
// whose components all declare a raw command, and the notable one where a
// component implies a language.
// Any other failure is said out loud: this is the only thing standing between
// a scan that narrowed and a scan that narrowed silently, so a git that would
// not run must not switch it off without a word.
func warnUnscanned(ctx context.Context, w io.Writer, dir string, file component.File, cfg config.Config) []orphan.Gap {
	gaps, err := orphan.Unscanned(ctx, dir, file, func(l runner.Lang) bool { return langEnabled(l, cfg) })
	if err != nil {
		// git listing no source at all is worth saying only where some
		// component implies a language — `--dir` pointed at a gitignored tree
		// looks exactly like that. A repository whose every component declares
		// a raw command has no source in a language lydite knows by
		// construction, and telling it so on every run is a warning about its
		// ordinary state.
		if errors.Is(err, orphan.ErrNoRepository) || (errors.Is(err, orphan.ErrNoFiles) && !anyLanguageDeclared(file)) {
			return nil
		}
		_, _ = fmt.Fprintf(w, "warning: could not check what no component scans (%v)\n", err)
		return nil
	}
	for _, g := range gaps {
		// One example and a count, not the list: the reader needs to know
		// which declaration is missing, and a repository mid-migration would
		// otherwise print hundreds of paths ahead of its own report.
		_, _ = fmt.Fprintf(w, "warning: %d %s file(s) are under no component that checks them, so nothing scans them (e.g. %s) — declare a component for them, or exclude them in %s\n",
			len(g.Files), g.Lang, g.Files[0], component.FileName)
	}
	return gaps
}

// recordGoLicence is the licence gate for one Go component: the non-conforming
// set its build compiles, against the same set recomputed at the merge-base.
//
// The base is recomputed rather than read from a stored baseline. An entry
// written by a lydite that did not yet compute licences reads back as the empty
// set, so the delta on the day of the upgrade is the absolute set and fails
// every adopting repository over dependencies nobody in that change chose.
func recordGoLicence(ctx context.Context, rep *ui.Report, tree *licenceBaseTree, c component.Component, cdir string, env []string, cfg config.Config, changed map[string][]int) {
	policy := licence.NewPolicy(cfg.Licence.Policy.Allow)
	label := licence.Gate + "(" + c.Name + ")"
	base := licence.NoDiffBase()
	if policy.Configured() {
		// Asked for only under a stated policy, because it costs a worktree and
		// a module download and answers a question an unconfigured repository is
		// not asking.
		base = goLicenceBase(ctx, tree, c.Dir, env, policy)
	}
	comparison, found, err := golang.LicenceCheck(ctx, cdir, env, policy, base)
	if err != nil {
		// Unmeasured and never fail: a component whose own dependencies could
		// not be enumerated has had nothing decided about it, and a red row
		// here would ask its author to answer for a claim the gate never made.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value: "the component's dependencies could not be read", Detail: []string{err.Error()}})
		return
	}
	rep.Add(licenceRow(label, comparison))
	// Through labelled and findingsOf, so a licence claim's component, its
	// path from the scan root and its row label are derived exactly where
	// every other scanner's are.
	claims := findingsOf(labelled([]executil.Result{{Name: licence.Gate, Findings: found}}, c.Name, c.Dir))
	finding.Anchored(claims, changed)
	rep.AddFindings(claims...)
}

// recordRustLicence is the licence gate for one Rust component: the crates
// cargo-deny rejects under the stated policy, against the same set recomputed at
// the merge-base.
//
// The row names the document that decided the licences as well as the verdict.
// A Rust component can be governed by lydite's policy, by its own deny.toml or
// by neither, and those three are answered by edits to different files — or by
// no edit at all.
func recordRustLicence(ctx context.Context, rep *ui.Report, tree *licenceBaseTree, c component.Component, cdir string, env executil.Env, cfg config.Config, changed map[string][]int) {
	policy := licence.NewPolicy(cfg.Licence.Policy.Allow)
	label := licence.Gate + "(" + c.Name + ")"
	base := licence.NoDiffBase()
	if policy.Configured() {
		// Asked for only under a stated policy, because it costs a worktree and
		// a cargo-deny run and answers a question no other policy source gates
		// on.
		base = rustLicenceBase(ctx, tree, c.Dir, env, policy)
	}
	comparison, source, found, err := rust.LicenceCheck(ctx, cdir, env, policy, base)
	if err != nil {
		// Unmeasured and never fail: a component whose own dependencies could
		// not be enumerated has had nothing decided about it, and a red row here
		// would ask its author to answer for a claim the gate never made.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value: "the component's dependencies could not be read", Detail: []string{err.Error()}})
		return
	}
	row := licenceRow(label, comparison)
	if source == rust.PolicyFromConsumer && comparison.Verdict == licence.VerdictFail {
		// A component's own deny.toml is evaluated whole and absolutely, with no
		// base in it, so the count is every crate it rejected rather than what
		// this change introduced.
		row.Value = fmt.Sprintf("%d non-conforming licence(s)", len(comparison.Pairs))
	}
	row.Value += " — " + policySourceSays(source)
	rep.Add(row)
	claims := findingsOf(labelled([]executil.Result{{Name: licence.Gate, Findings: found}}, c.Name, c.Dir))
	finding.Anchored(claims, changed)
	rep.AddFindings(claims...)
}

// policySourceSays names the document that decided a Rust component's licences,
// on its row.
//
// Which one it was cannot be read off the verdict, and a reader told only that
// nothing was gated has no way to find the file an edit would go in.
func policySourceSays(s rust.PolicySource) string {
	switch s {
	case rust.PolicyFromLydite:
		return "policy from " + config.FileName
	case rust.PolicyFromConsumer:
		return "policy from the component's own " + rust.DenyConfigFile
	case rust.PolicyFromNone:
		return "no " + rust.DenyConfigFile + " either, so no licence check ran"
	}
	return string(s)
}

// recordTypeScriptLicence is the licence gate for one TypeScript component: the
// dependencies its lockfile resolved, against the same set recomputed at the
// merge-base.
//
// No install is run on either side, for any package manager. npm's lockfile
// states every dependency's licence outright; yarn's and pnpm's state none, and
// a tree no earlier step installed is the row saying so. See docs/adr/0042.
func recordTypeScriptLicence(ctx context.Context, rep *ui.Report, tree *licenceBaseTree, c component.Component, cdir string, cfg config.Config, changed map[string][]int) {
	policy := licence.NewPolicy(cfg.Licence.Policy.Allow)
	label := licence.Gate + "(" + c.Name + ")"
	if !policy.Configured() {
		// Nothing is read for a repository that stated no policy. A manager
		// that states no licence answers unmeasured, and reporting that where
		// the row's answer is already known asks its author for an install to
		// settle a question nobody put.
		rep.Add(licenceRow(label, licence.Comparison{Verdict: licence.VerdictNotConfigured}))
		return
	}
	base := typescriptLicenceBase(ctx, tree, c.Dir, policy)
	// The scan root bounds the walk to the workspace root whose lockfile
	// resolves this component: a member nested under one declares no lockfile
	// of its own, and lydite was never asked to look above what it was pointed
	// at.
	current, err := typescript.LicenceSet(ctx, cdir, tree.root, policy)
	if err != nil {
		// Unmeasured and never fail: a component whose own dependencies could
		// not be enumerated has had nothing decided about it, and a red row
		// here would ask its author to answer for a claim the gate never made.
		rep.Add(ui.Row{Status: ui.StatusUnmeasured, Label: label,
			Value: "the component's dependencies could not be read", Detail: []string{err.Error()}})
		return
	}
	comparison := licence.Compare(policy, current, base)
	rep.Add(licenceRow(label, comparison))
	if comparison.Verdict != licence.VerdictFail {
		// A claim per pair only where the gate failed on them. Every other
		// verdict gates nothing, and a located claim under one would reach the
		// review surface as a thread about a dependency nothing is blocking on.
		return
	}
	// Through labelled and findingsOf, so a licence claim's component, its path
	// from the scan root and its row label are derived exactly where every other
	// scanner's are.
	claims := findingsOf(labelled([]executil.Result{{Name: licence.Gate, Findings: typescript.LicenceFindings(cdir, comparison.Pairs)}}, c.Name, c.Dir))
	finding.Anchored(claims, changed)
	rep.AddFindings(claims...)
}

// licenceRow renders one component's comparison.
//
// Only a gating verdict renders green or red. A policy nobody stated, a run
// given no diff base and a base that could not be built each gate nothing, and
// rendering any of them as `pass` is a gate that never ran reported as one that
// ran and found nothing.
func licenceRow(label string, c licence.Comparison) ui.Row {
	row := ui.Row{Label: label, Detail: licenceDetail(c.Pairs)}
	switch c.Verdict {
	case licence.VerdictPass:
		row.Status, row.Value = ui.StatusPass, "passed"
	case licence.VerdictFail:
		row.Status, row.Value = ui.StatusFail,
			fmt.Sprintf("%d non-conforming licence(s) introduced against the merge-base", len(c.Pairs))
	case licence.VerdictUnmeasured:
		row.Status, row.Value = ui.StatusUnmeasured, c.Reason
	case licence.VerdictContext:
		row.Status, row.Value = ui.StatusContext,
			fmt.Sprintf("%d non-conforming dependencies, gating nothing — no diff base to compare against", len(c.Pairs))
	case licence.VerdictNotConfigured:
		row.Status, row.Value = ui.StatusContext,
			"not configured — state licence.policy.allow in "+config.FileName
	}
	return row
}

// licenceDetail names each pair the verdict is about, so a row a reader cannot
// act on names the dependency and the licence rather than only a count.
func licenceDetail(pairs []licence.Dependency) []string {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]string, 0, len(pairs))
	for _, d := range pairs {
		entry := d.Package
		if d.Version != "" {
			entry += " " + d.Version
		}
		out = append(out, entry+": "+d.Licence)
	}
	return out
}

// goLicenceBase is the Go component's non-conforming set recomputed at the
// merge-base.
//
// The base tree's environment is the branch's: the component's resolved
// toolchain and the environment its declaration asks for. A base tree declaring
// a Go newer than that toolchain is a `go list` that will not run, and it
// answers unmeasured naming what it said rather than a set read under an
// environment nobody chose.
func goLicenceBase(ctx context.Context, tree *licenceBaseTree, componentDir string, env []string, policy licence.Policy) licence.Base {
	return tree.set(ctx, componentDir, "go.mod", func(dir string) (licence.Set, error) {
		return golang.LicenceSet(ctx, dir, env, policy)
	})
}

// rustLicenceBase is the Rust component's non-conforming set recomputed at the
// merge-base.
//
// The lockfile is what has to be there: a component with no Cargo.lock at the
// base is one this change adds, which the generated policy decides nothing
// about at that end.
func rustLicenceBase(ctx context.Context, tree *licenceBaseTree, componentDir string, env executil.Env, policy licence.Policy) licence.Base {
	return tree.set(ctx, componentDir, "Cargo.lock", func(dir string) (licence.Set, error) {
		set, _, err := rust.LicenceSet(ctx, dir, env, policy)
		return set, err
	})
}

// typescriptLicenceBase is the TypeScript component's non-conforming set
// recomputed at the merge-base.
//
// The manifest is what has to be there, rather than a lockfile: a component
// declares one whichever package manager it uses, while the lockfile that
// answers its licences is npm's alone. A component with no package.json at the
// base is one this change adds, and a base missing only the lockfile is a set
// that could not be read — which is unmeasured, never the empty set that would
// make every dependency it already had read as introduced here.
func typescriptLicenceBase(ctx context.Context, tree *licenceBaseTree, componentDir string, policy licence.Policy) licence.Base {
	return tree.set(ctx, componentDir, "package.json", func(dir string) (licence.Set, error) {
		// The scan root inside the base worktree, which set has already
		// opened. The outer scan root bounds nothing here: dir sits in the
		// worktree, so a walk bounded by the branch's own root climbs out of
		// the tree being measured.
		return typescript.LicenceSet(ctx, dir, tree.dir, policy)
	})
}

// licenceBaseTree is the merge-base checked out once for a whole scan, which
// every component's licence gate reads its base set from.
//
// measureBaseTree is the precedent, including its arithmetic: one commit, one
// checkout. The base set is per component but the tree holding it is not, so a
// repository with N Go and Rust components pays one checkout rather than N of
// the identical commit. What makes the recompute affordable at all is what
// measureBaseTree cannot do — a licence set needs the manifest, a warm
// dependency cache and one tool invocation, with no suite to run, no compose
// service to start and no instrumented build.
//
// The checkout happens on first use, so a scan no component asks a base of — no
// diff base, no stated policy, nothing but TypeScript — pays for no worktree.
// The failure is remembered as well as the success: a checkout that would not
// run answers every later component the same reason, decided once.
type licenceBaseTree struct {
	// root is the scan root, which is where git is run and which prefix
	// locates inside the repository.
	root string
	// baseSHA is the merge-base, empty on a run that was given no diff base.
	baseSHA string

	opened bool
	// tmp is the worktree's own root, empty until it has been checked out.
	tmp string
	// dir is the scan root inside that worktree — tmp joined with the prefix.
	dir string
	// reason is what stopped the checkout, empty while nothing has.
	reason string
}

// newLicenceBaseTree is the base every component of one scan compares against.
// Nothing is checked out here: a scan reaches this whether or not any component
// will ask it for a set.
func newLicenceBaseTree(root, baseSHA string) *licenceBaseTree {
	return &licenceBaseTree{root: root, baseSHA: baseSHA}
}

// open checks the merge-base out, once, and answers the scan root inside it —
// or the reason no component can be measured against it.
func (t *licenceBaseTree) open(ctx context.Context) (string, string) {
	if t.opened {
		return t.dir, t.reason
	}
	t.opened = true
	tmp, err := os.MkdirTemp("", "lydite-licence-*")
	if err != nil {
		t.reason = "no temporary directory for the base worktree: " + err.Error()
		return "", t.reason
	}
	// Recorded before the checkout is attempted, so close removes a worktree
	// `git worktree add` registered and then failed part-way through.
	t.tmp = tmp
	if r := executil.RunQuiet(ctx, t.root, "git", "worktree", "add", "--detach", tmp, t.baseSHA); !r.Ok() {
		t.reason = "checking out " + shortSHA(t.baseSHA) + ": " + r.Err.Error()
		return "", t.reason
	}
	// A worktree holds the whole repository and the scan root may sit below
	// it, so a component is located through the prefix rather than from the
	// worktree root — the shape ChangedLines and measureBaseTree already
	// account for.
	prefix, err := gitdiff.Prefix(ctx, t.root)
	if err != nil {
		t.reason = "locating the scan root inside the repository: " + err.Error()
		return "", t.reason
	}
	t.dir = filepath.Join(tmp, filepath.FromSlash(prefix))
	return t.dir, ""
}

// set is one component's non-conforming set at the merge-base, with read the
// language's own way of measuring one.
//
// manifest is the file whose absence at the base means the component was not
// there. Every failure answers UnmeasuredBase naming the step, never a measured
// empty set: a base that could not be built gates nothing and says so on the
// row.
func (t *licenceBaseTree) set(ctx context.Context, componentDir, manifest string, read func(dir string) (licence.Set, error)) licence.Base {
	if t.baseSHA == "" {
		return licence.NoDiffBase()
	}
	root, reason := t.open(ctx)
	if reason != "" {
		return licence.UnmeasuredBase(reason)
	}
	dir := filepath.Join(root, filepath.FromSlash(componentDir))
	if _, err := os.Stat(filepath.Join(dir, manifest)); err != nil {
		// A component with no manifest at the base is one this change adds, and
		// every pair it carries is one the change introduces. That is a
		// measured empty set rather than an unmeasured base: nothing failed,
		// there was nothing there. It is asked per component, because the
		// shared worktree answers it for each of them separately.
		return licence.MeasuredBase(licence.Set{})
	}
	set, err := read(dir)
	if err != nil {
		return licence.UnmeasuredBase("reading the base tree's dependencies: " + err.Error())
	}
	return licence.MeasuredBase(set)
}

// close removes the worktree, once, after every component has read from it. It
// is a no-op for a scan that never opened one.
func (t *licenceBaseTree) close(ctx context.Context) {
	if t.tmp == "" {
		return
	}
	tmp := t.tmp
	t.tmp = ""
	// A context of its own, for the reason measureBaseTree's removal has one:
	// the run's may already be cancelled, and an interrupt would kill the
	// removal and then delete the directory anyway — leaving a registered
	// worktree pointing at nothing, which every later `git worktree add` and
	// `git worktree list` in that repository trips over until someone prunes.
	_ = executil.RunQuiet(context.WithoutCancel(ctx), t.root, "git", "worktree", "remove", "--force", tmp)
	_ = os.RemoveAll(tmp)
}

// semgrepBase is the diff base Semgrep is given, which is none when a token is
// set.
//
// `semgrep ci` scopes itself to the diff already, and passing --baseline-commit
// on top of that is redundant. The rule lives here rather than in the resolver
// because the resolved base has a second reader now: a finding's anchor, which
// a Semgrep token says nothing about.
func semgrepBase(baseSHA string) string {
	if os.Getenv(semgrep.AppTokenEnv) != "" {
		return ""
	}
	return baseSHA
}

// resolveDiffBase turns the --diff-base flag into a commit SHA for Semgrep's
// scan-mode --baseline-commit. "auto" resolves the same merge-base
// `lydite coverage` already gates against, so a PR's scan and coverage agree
// on what "this change" means; any other non-empty value is passed through as
// a literal ref.
//
// It is resolved whenever --diff-base is given, including for a run carrying a
// SEMGREP_APP_TOKEN and a run with Semgrep switched off. The base has a second
// reader besides Semgrep: it is what decides whether a finding reaches a line
// of the change, and so whether it becomes a review thread or a row in the
// standing comment. A token says `semgrep ci` scopes itself; it says nothing
// about where gosec's claims belong.
//
// The cost is real and is the point: every consumer passing `--diff-base
// auto` needs a full-history checkout to resolve the merge-base, a token in
// the environment notwithstanding. Asking for a diff-scoped scan and getting
// claims that reach no line is what the alternative buys.
func resolveDiffBase(ctx context.Context, dir, diffBase, baseBranch string) (string, error) {
	if diffBase == "" {
		return "", nil
	}
	if diffBase == "auto" {
		// Deliberately an error, not a silent full-repo scan: falling back
		// would reintroduce exactly the surprise this flag exists to remove —
		// a scan that quietly changes scope, and starts blocking on findings
		// the PR never touched. A shallow checkout is a fixable CI
		// misconfiguration (fetch-depth: 0), so say so.
		baseSHA, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
		if err != nil {
			return "", fmt.Errorf("--diff-base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
		}
		diffBase = baseSHA
	}
	// Resolved to a commit before it reaches anything, so what a tool is given
	// is a SHA and never the caller's string. The anchor reads this base by
	// handing it to `git diff <base>..HEAD`, where a value beginning with `-`
	// is a position git parses as an option — `--diff-base --output=/tmp/x`
	// would make git write the diff to a path of the caller's choosing.
	// --end-of-options is what stops that here, and resolving once is what
	// makes verifying it here sufficient for every later invocation.
	// resolveReviewBase does the same for --base, and executil.RunQuiet's own
	// doc names this as the caller's duty.
	rev := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "--end-of-options", diffBase+"^{commit}")
	resolved := strings.TrimSpace(rev.Output)
	if !rev.Ok() || resolved == "" {
		return "", fmt.Errorf("--diff-base %q does not name a commit", diffBase)
	}
	return resolved, nil
}

// ranAnyCheck reports whether any row records a check that executed. An
// unmeasured row is a component saying why it has no check, which is the
// opposite.
func ranAnyCheck(rep *ui.Report) bool {
	for _, row := range rep.Rows() {
		if row.Status != ui.StatusUnmeasured {
			return true
		}
	}
	return false
}

// resultRows turns each check's result into its row. It is the one place a
// Result becomes a Row, so rows a command adds as it goes and rows added at
// the end cannot render differently.
//
// Every row names the log holding the whole of what its check printed, which
// is what lets a pull-request comment reach past the tail in Detail. A passing
// check's output is kept too: it is what a reader consults to find out what a
// clean run actually looked at.
func resultRows(root string, results []executil.Result) []ui.Row {
	rows := make([]ui.Row, 0, len(results))
	for _, r := range results {
		status := ui.StatusPass
		value := "passed"
		var detail []string
		if !r.Ok() {
			status, value = ui.StatusFail, "failed"
			detail = strings.Split(strings.TrimRight(r.Detail, "\n"), "\n")
			if len(detail) == 1 && strings.TrimSpace(detail[0]) == "" {
				detail = nil
			}
		}
		rows = append(rows, ui.Row{
			Status: status, Label: r.Name, Value: value, Detail: detail,
			Log: checkLog(root, r.Name, r.Output),
		})
	}
	return rows
}

// record puts one batch of check results into the report: a row each, and the
// located claims they made.
//
// One call and not a pair, because the rows and the findings are the same
// results read twice — a caller that adds one and forgets the other publishes
// a comment saying a check failed and a review with nothing on the line it
// failed at.
func record(rep *ui.Report, root string, changed map[string][]int, results []executil.Result) {
	for _, row := range resultRows(root, results) {
		rep.Add(row)
	}
	found := findingsOf(results)
	// Anchored here, after labelled has rebased each path onto the scan root:
	// the map is keyed from that root, and a claim still named from inside its
	// component would match nothing in it.
	finding.Anchored(found, changed)
	rep.AddFindings(found...)
}

// findingsOf is every check's located claims, each naming the row that made
// it.
//
// The label is the check's own name, taken from the same field resultRows
// renders a row's label from rather than rebuilt beside it. A finding whose
// row label does not match the row's is one the standing comment cannot
// partition: it would be rendered there as well as on the line a thread
// anchors it to, or dropped from both.
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

// report renders one row per check in the grammar docs/design/tokens.md
// specifies, and returns the run's exit code as an error so the process
// reflects the verdict.
//
// A failing check also prints its Detail, which is the only place some
// findings' full text exists. Most tools stream their own output live
// through executil.Run, so it is already on the terminal and in the log the
// action captures; Biome's report never reaches the terminal at all, because
// lydite sends it to a file so the JSON cannot be corrupted by Biome's own
// chatter. clippy, cargo-audit and cargo-deny run once in JSON mode, so their
// stream is that same JSON rather than a second, richer rendering worth
// reprinting, and Detail carries the claim instead. Printing only a status
// line left the developer to re-run the pinned toolchain by hand to find out
// what was wrong, and put nothing in the PR comment either.
func report(cmd *cobra.Command, rep *ui.Report, root string, changed map[string][]int, results []executil.Result, asJSON, noColor bool) error {
	record(rep, root, changed, results)
	saveDocument(root, rep)
	out := cmd.OutOrStdout()
	if err := rep.Write(out, asJSON, ui.ColorEnabled(out, noColor)); err != nil {
		return err
	}
	return rep.Err()
}
