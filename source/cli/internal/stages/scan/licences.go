package scanstages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/gitdiff"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/typescript"
)

// GateLicencesIn is the plan whose Scan entries get a licence verdict, and
// the merge-base each verdict is compared against.
type GateLicencesIn struct {
	// Dir is the scan root: where git is run to check the merge-base out, and
	// the bound a TypeScript component's walk to its workspace lockfile stops
	// at.
	Dir string
	// BaseSHA is the resolved diff base, empty for a scan given none — which
	// gates nothing, and checks nothing out.
	BaseSHA string
	Plan    []Planned
	// Config states the licence policy, licence.policy.allow.
	Config config.Config
	// Changed is, per path from the scan root, the lines the change touched,
	// which decides each claim's anchor. Empty for a scan with no diff base.
	Changed map[string][]int
}

// LicenceVerdict is what one component's licence gate answered.
type LicenceVerdict struct {
	// Gated reports whether a licence gate exists for the component's
	// language. It is false for a Scan entry whose language declares no
	// dependency set to read licences from — shell — and for every entry that
	// is not Scan, so a verdict nothing computed never reads as one that ran
	// and found nothing. Every other field is empty where it is false.
	Gated bool
	// Err is why the component's own dependencies could not be enumerated,
	// nil where they were. It is the gate's outcome and never this stage's
	// error: a component whose dependencies could not be read has had nothing
	// decided about it. Where it is set, Comparison is the zero value and
	// carries no verdict.
	Err error
	// Comparison is the component's non-conforming set against the merge-base,
	// under the stated policy. Read only where Err is nil.
	Comparison licence.Comparison
	// PolicySource is the document that decided a Rust component's licences,
	// and empty for every other language.
	PolicySource rust.PolicySource
	// Findings is each pair a failing verdict is about as a located claim,
	// labelled for the component and anchored against the lines the change
	// touched. Empty under every verdict that gates nothing.
	Findings []finding.Finding
	// Crashes is the licence gate, for the component, where the gate made no
	// claim it could stand behind — see licenceCrashed.
	Crashes []finding.Crash
}

// GateLicencesOut is one entry per entry of the plan it was given, at the
// same index. An entry that is not Scan is the zero LicenceVerdict, so one
// component's licence verdict can never be read as another's.
type GateLicencesOut struct {
	Licences []LicenceVerdict
}

// GateLicences is the licence verdict for each Scan entry of the plan, in
// plan order, each comparing the component's non-conforming set against the
// same set recomputed at the merge-base.
//
// It owns the one merge-base worktree every component reads its base from. It
// is checked out the first time a component asks for a base, and removed by a
// deferred close in this function's own body — so a gate that panics still
// leaves no worktree registered behind it.
//
// Every verdict is data in the Out, never this stage's error. The one error
// is a plan entry naming a language no gate here handles, which is refused
// before anything is read or checked out.
func GateLicences(ctx context.Context, in GateLicencesIn) (GateLicencesOut, error) {
	return gateLicences(ctx, in, licenceGateFor)
}

// gateLicences is GateLicences with the language dispatch supplied, so what
// happens to the worktree when a gate fails part-way can be exercised with a
// gate of the caller's own.
func gateLicences(ctx context.Context, in GateLicencesIn, gateFor func(runner.Lang) (licenceGate, error)) (GateLicencesOut, error) {
	for _, p := range in.Plan {
		if p.Disposition != Scan {
			continue
		}
		if _, err := gateFor(p.Lang); err != nil {
			return GateLicencesOut{}, fmt.Errorf("component %s: %w", p.Component.Name, err)
		}
	}
	tree := newLicenceBaseTree(in.Dir, in.BaseSHA)
	defer tree.close(ctx)

	policy := licence.NewPolicy(in.Config.Licence.Policy.Allow)
	out := make([]LicenceVerdict, len(in.Plan))
	for i, p := range in.Plan {
		if p.Disposition != Scan {
			continue
		}
		gate, _ := gateFor(p.Lang)
		out[i] = gate(ctx, tree, p, policy, in.Changed)
	}
	return GateLicencesOut{Licences: out}, nil
}

// licenceGate is one language's licence gate over one Scan entry.
type licenceGate func(ctx context.Context, tree *licenceBaseTree, p Planned, policy licence.Policy, changed map[string][]int) LicenceVerdict

// licenceGateFor is the licence gate a language runs. Every language a scan
// runs checks for is named — shell among them, as the language with no gate —
// and any other is refused rather than handed another language's gate: that
// reads none of this language's manifests, and would report a dependency set
// it never looked at.
func licenceGateFor(lang runner.Lang) (licenceGate, error) {
	switch lang {
	case runner.Go:
		return goLicence, nil
	case runner.Rust:
		return rustLicence, nil
	case runner.TypeScript:
		return typescriptLicence, nil
	case runner.Shell:
		return noLicenceGate, nil
	default:
		return nil, fmt.Errorf("%q is planned for scanning and has no licence gate", lang)
	}
}

// noLicenceGate is the gate of a language that declares no dependency set to
// read licences from: nothing, marked as not gated.
func noLicenceGate(context.Context, *licenceBaseTree, Planned, licence.Policy, map[string][]int) LicenceVerdict {
	return LicenceVerdict{}
}

// goLicence is the licence gate for one Go component: the non-conforming set
// its build compiles, against the same set recomputed at the merge-base.
//
// The base is recomputed rather than read from a stored baseline. An entry
// written by a lydite that did not compute licences reads back as the empty
// set, so the delta on the day of an upgrade would be the absolute set and fail
// every adopting repository over dependencies nobody in that change chose.
func goLicence(ctx context.Context, tree *licenceBaseTree, p Planned, policy licence.Policy, changed map[string][]int) LicenceVerdict {
	base := licence.NoDiffBase()
	if policy.Configured() {
		// Asked for only under a stated policy, because it costs a worktree and
		// a module download and answers a question an unconfigured repository is
		// not asking.
		base = goLicenceBase(ctx, tree, p.Component.Dir, p.Env.Check, policy)
	}
	comparison, found, err := golang.LicenceCheck(ctx, p.Dir, p.Env.Check, policy, base)
	v := LicenceVerdict{Gated: true, Crashes: licenceCrashed(p.Component.Name, comparison, err)}
	if err != nil {
		v.Err = err
		return v
	}
	v.Comparison = comparison
	v.Findings = licenceFindings(found, p.Component, changed)
	return v
}

// rustLicence is the licence gate for one Rust component: the crates
// cargo-deny rejects under the stated policy, against the same set recomputed
// at the merge-base.
//
// The verdict carries the document that decided the licences. A Rust
// component can be governed by lydite's policy, by its own deny.toml or by
// neither, and those three are answered by edits to different files — or by
// no edit at all.
func rustLicence(ctx context.Context, tree *licenceBaseTree, p Planned, policy licence.Policy, changed map[string][]int) LicenceVerdict {
	base := licence.NoDiffBase()
	if rust.PolicyFor(p.Dir, policy) == rust.PolicyFromLydite {
		// Asked for under lydite's own policy alone. A component's own
		// deny.toml gates absolutely, every run, and a base read for it would
		// be a worktree and a cargo-deny run spent on a comparison its verdict
		// never makes.
		base = rustLicenceBase(ctx, tree, p.Component.Dir, p.Env, policy)
	}
	comparison, source, found, err := rust.LicenceCheck(ctx, p.Dir, p.Env, policy, base)
	v := LicenceVerdict{Gated: true, PolicySource: source, Crashes: licenceCrashed(p.Component.Name, comparison, err)}
	if err != nil {
		v.Err = err
		return v
	}
	v.Comparison = comparison
	v.Findings = licenceFindings(found, p.Component, changed)
	return v
}

// typescriptLicence is the licence gate for one TypeScript component: the
// dependencies its lockfile resolved, against the same set recomputed at the
// merge-base.
//
// No install is run on either side, for any package manager. npm's lockfile
// states every dependency's licence outright; yarn's and pnpm's state none, and
// a tree no earlier step installed is the verdict saying so. See docs/adr/0042.
func typescriptLicence(ctx context.Context, tree *licenceBaseTree, p Planned, policy licence.Policy, changed map[string][]int) LicenceVerdict {
	if !policy.Configured() {
		// Nothing is read for a repository that stated no policy. A manager
		// that states no licence answers unmeasured, and reporting that where
		// the answer is already known asks its author for an install to settle
		// a question nobody put.
		return LicenceVerdict{Gated: true, Comparison: licence.Comparison{Verdict: licence.VerdictNotConfigured}}
	}
	base := typescriptLicenceBase(ctx, tree, p.Component.Dir, policy)
	// The scan root bounds the walk to the workspace root whose lockfile
	// resolves this component: a member nested under one declares no lockfile
	// of its own, and lydite was never asked to look above what it was pointed
	// at.
	current, err := typescript.LicenceSet(ctx, p.Dir, tree.root, policy)
	if err != nil {
		return LicenceVerdict{Gated: true, Err: err, Crashes: licenceCrashed(p.Component.Name, licence.Comparison{}, err)}
	}
	comparison := licence.Compare(policy, current, base)
	v := LicenceVerdict{Gated: true, Comparison: comparison, Crashes: licenceCrashed(p.Component.Name, comparison, nil)}
	if comparison.Verdict != licence.VerdictFail {
		// A claim per pair only where the gate failed on them. Every other
		// verdict gates nothing, and a located claim under one would reach the
		// review surface as a thread about a dependency nothing is blocking on.
		return v
	}
	v.Findings = licenceFindings(typescript.LicenceFindings(p.Dir, comparison.Pairs), p.Component, changed)
	return v
}

// licenceFindings is a licence gate's located claims, labelled, named for
// their row and anchored exactly where every other scanner's are: through
// labelled and anchoredFindings, so a claim's component, its path from the
// scan root and its row label cannot be derived two ways.
func licenceFindings(found []finding.Finding, c component.Component, changed map[string][]int) []finding.Finding {
	return anchoredFindings(labelled([]executil.Result{{Name: licence.Gate, Findings: found}}, c.Name, c.Dir), changed)
}

// licenceCrashed names the licence gate crashed for a component whose gate
// made no claim it could stand behind, and nothing otherwise.
//
// The gate only ever claims the pairs a failing verdict is about. A component
// whose dependencies could not be read, a base that could not be built and a
// run with no base to compare against all report no pair at all, and that
// absence says nothing about which dependencies conform — so the bucket is
// named as crashed rather than read as clean.
func licenceCrashed(component string, c licence.Comparison, err error) []finding.Crash {
	if err != nil || c.Verdict == licence.VerdictUnmeasured || c.Verdict == licence.VerdictContext {
		return []finding.Crash{{Gate: licence.Gate, Component: component}}
	}
	return nil
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
// `lydite test`'s base measurement is the precedent, including its arithmetic:
// one commit, one checkout. The base set is per component but the tree holding
// it is not, so a repository with N Go and Rust components pays one checkout
// rather than N of the identical commit. What makes the recompute affordable at
// all is what a coverage base cannot do — a licence set needs the manifest, a
// warm dependency cache and one tool invocation, with no suite to run, no
// compose service to start and no instrumented build.
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
	// worktree root — the shape ChangedLines and the coverage base already
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
// empty set: a base that could not be built gates nothing and says so.
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
	// A context of its own: the run's may already be cancelled, and an
	// interrupt would kill the removal and then delete the directory anyway —
	// leaving a registered worktree pointing at nothing, which every later
	// `git worktree add` and `git worktree list` in that repository trips over
	// until someone prunes.
	_ = executil.RunQuiet(context.WithoutCancel(ctx), t.root, "git", "worktree", "remove", "--force", tmp)
	_ = os.RemoveAll(tmp)
}

// shortSHA is a commit's abbreviated form, as a reason names it.
func shortSHA(sha string) string {
	return sha[:min(len(sha), 12)]
}
