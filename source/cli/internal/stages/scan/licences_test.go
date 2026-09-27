package scanstages

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/cargotool"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
)

// licenceConfig is a configuration stating a policy the way a repository does:
// permissive enough to pass the probe's dually licensed module and to reject
// both of its copyleft ones.
func licenceConfig() config.Config {
	cfg := config.Default()
	cfg.Licence.Policy.Allow = []string{"Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MIT"}
	return cfg
}

// licenceEntry is a Scan entry of the plan for one component, run in its
// directory under root.
func licenceEntry(root, name, dir string, lang runner.Lang, env executil.Env) Planned {
	return Planned{
		Component:   component.Component{Name: name, Dir: dir},
		Lang:        lang,
		Disposition: Scan,
		Dir:         filepath.Join(root, filepath.FromSlash(dir)),
		Env:         env,
	}
}

// gateOne is the stage's verdict for a plan of one Scan entry.
func gateOne(t *testing.T, root, baseSHA string, cfg config.Config, changed map[string][]int, p Planned) LicenceVerdict {
	t.Helper()
	out, err := GateLicences(context.Background(), GateLicencesIn{
		Dir: root, BaseSHA: baseSHA, Plan: []Planned{p}, Config: cfg, Changed: changed,
	})
	if err != nil {
		t.Fatalf("GateLicences: %v", err)
	}
	if len(out.Licences) != 1 {
		t.Fatalf("licences = %d entries for a plan of one, want one", len(out.Licences))
	}
	return out.Licences[0]
}

// probeFiles is a captured probe tree, as the files one commit is made of,
// rooted at prefix.
func probeFiles(t *testing.T, probe, prefix string) map[string]string {
	t.Helper()
	tree := fixture.Tree(t, probe)
	entries, err := os.ReadDir(tree)
	if err != nil {
		t.Fatalf("reading the probe: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tree, e.Name()))
		if err != nil {
			t.Fatalf("reading the probe: %v", err)
		}
		out[path.Join(prefix, e.Name())] = string(data)
	}
	return out
}

// goProbeFiles is the captured Go probe module, rooted at prefix. Its
// dependency set covers strong copyleft, weak copyleft and a module carrying
// two licence files.
func goProbeFiles(t *testing.T, prefix string) map[string]string {
	t.Helper()
	return probeFiles(t, filepath.Join("..", "..", "licence", "testdata", "goprobe"), prefix)
}

// tsProbeFiles is a captured TypeScript probe tree, rooted at prefix.
func tsProbeFiles(t *testing.T, probe, prefix string) map[string]string {
	t.Helper()
	return probeFiles(t, filepath.Join("..", "..", "typescript", "testdata", probe), prefix)
}

// denyProbeLock is the captured Rust probe's lockfile, as the one file a
// component's licence gate locates its claims in.
func denyProbeLock(t *testing.T) string {
	t.Helper()
	tree := fixture.Tree(t, filepath.Join("..", "..", "licence", "testdata", "denyprobe"))
	data, err := os.ReadFile(filepath.Join(tree, "Cargo.lock"))
	if err != nil {
		t.Fatalf("reading the probe's lockfile: %v", err)
	}
	return string(data)
}

// cargoDenyStub puts a captured cargo-deny run in the version-keyed tool cache,
// so the gate runs a real invocation against a real stream with nothing
// installed and nothing fetched. capture names the stream and the file holding
// the status it exited with. It answers the file each invocation appends the
// directory it ran in to.
func cargoDenyStub(t *testing.T, capture string) string {
	t.Helper()
	home := t.TempDir()
	// Both, because os.UserCacheDir reads XDG_CACHE_HOME on Linux and
	// $HOME/Library/Caches on macOS.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	pin, err := os.ReadFile(filepath.Join("..", "..", "rust", "cargo-deny-pin", "Cargo.toml"))
	if err != nil {
		t.Fatalf("reading the pin: %v", err)
	}
	bin, err := (cargotool.Tool{Name: "cargo-deny", Version: cargotool.MustPinnedVersion(pin, "cargo-deny")}).Binary()
	if err != nil {
		t.Fatalf("locating the cached binary: %v", err)
	}
	dir := filepath.Dir(bin)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}
	stream, err := os.ReadFile(filepath.Join("..", "..", "licence", "testdata", capture))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream"), stream, 0o600); err != nil {
		t.Fatalf("writing the stream: %v", err)
	}
	status, err := os.ReadFile(filepath.Join("..", "..", "licence", "testdata",
		strings.TrimSuffix(capture, filepath.Ext(capture))+".exit"))
	if err != nil {
		t.Fatalf("reading the fixture's exit status: %v", err)
	}
	// cargo-deny writes its NDJSON to stderr, and exits non-zero on a check it
	// failed — both of which the gate reads.
	script := "#!/bin/sh\npwd >> \"$(dirname \"$0\")/invocations\"\ncat \"$(dirname \"$0\")/stream\" >&2\nexit " + strings.TrimSpace(string(status)) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- a stub the test is about to execute
		t.Fatalf("writing the stub: %v", err)
	}
	return filepath.Join(dir, "invocations")
}

// licenceBaseRepo is a repository with two commits: the tree the merge-base
// holds, then the tree the change made of it. It answers the repository root
// and the merge-base SHA.
func licenceBaseRepo(t *testing.T, base, head map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(context.Background(), root, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
		return strings.TrimSpace(r.Output)
	}
	git("init", "-b", "main", ".")
	commit := func(files map[string]string) string {
		for name, body := range files {
			write(t, root, name, body)
		}
		git("add", "-A")
		// --allow-empty, because a change that alters no manifest is a tree the
		// gate has to answer for too: it is the case that must pass.
		git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "tree")
		return git("rev-parse", "HEAD")
	}
	baseSHA := commit(base)
	commit(head)
	return root, baseSHA
}

// readDeps stands in for a language's own reader: a manifest whose lines are
// `<package> <licence>`, so what a tree's non-conforming set is can be stated
// rather than measured by a toolchain. What is under test is which tree the
// reader was pointed at, which is the same question whatever reads it.
func readDeps(dir string) (licence.Set, error) {
	body, err := os.ReadFile(filepath.Join(dir, depsManifest))
	if err != nil {
		return licence.Set{}, err
	}
	var set licence.Set
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		set.Add(licence.Dependency{Package: fields[0], Licence: fields[1]})
	}
	return set, nil
}

// depsManifest is the file readDeps reads, and the file whose absence at the
// base means the component was not there.
const depsManifest = "deps"

// worktrees is how many working trees the repository has registered, which is
// one — its own — until a base is checked out.
func worktrees(t *testing.T, root string) int {
	t.Helper()
	r := executil.RunQuiet(context.Background(), root, "git", "worktree", "list", "--porcelain")
	if !r.Ok() {
		t.Fatalf("git worktree list: %v\n%s", r.Err, r.Stderr)
	}
	return strings.Count(r.Output, "worktree ")
}

// permissivePolicy is a stated policy, which is all Compare asks of one: the
// language reader has already rejected what the set carries.
func permissivePolicy() licence.Policy { return licence.NewPolicy([]string{"MIT"}) }

// packagesOf names what a verdict is about, in the order the comparison put
// them.
func packagesOf(pairs []licence.Dependency) []string {
	out := make([]string, 0, len(pairs))
	for _, d := range pairs {
		out = append(out, d.Package)
	}
	return out
}

// The base a Go component is compared against is the set its own build
// compiles at the merge-base, read in the checked-out tree rather than from a
// stored figure: an entry written by a lydite that computed no licences reads
// back as the empty set, and the delta on the day of the upgrade is then the
// absolute set.
func TestTheGoLicenceBaseReadsTheModuleAtTheMergeBase(t *testing.T) {
	files := goProbeFiles(t, "api")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := goLicenceBase(context.Background(), tree, "api", nil, licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	want := []string{"github.com/hashicorp/go-version", "github.com/juju/errors"}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, want) {
		t.Fatalf("base set = %v, want %v — the probe's copyleft modules, read under the stated policy", got, want)
	}
}

// The gate's whole shape for a Go component: a module the merge-base did not
// carry is a set every pair of which the change introduced, the verdict fails,
// and each claim is anchored to what the change touched — without which every
// claim reaches the review surface at no anchor at all. The worktree the base
// was read from is gone once the stage returns.
func TestTheGoLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/README.md": "the component this change adds a module to\n"},
		goProbeFiles(t, "api"))

	// Line 7 of the probe's manifest is the require naming github.com/juju/errors.
	changed := map[string][]int{"api/go.mod": {7}}
	v := gateOne(t, root, baseSHA, licenceConfig(), changed, licenceEntry(root, "api", "api", runner.Go, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %+v, want a failing licence verdict", v)
	}
	if len(v.Crashes) != 0 {
		t.Errorf("crashes = %+v, want none from a gate that decided", v.Crashes)
	}
	found := v.Findings
	if len(found) != 2 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	var anchors []finding.Anchor
	for _, f := range found {
		if f.Path != "api/go.mod" {
			t.Errorf("claim located at %q, want the component's manifest from the scan root", f.Path)
		}
		if f.Row != "licence(api)" || f.Component != "api" {
			t.Errorf("claim row %q, component %q, want the component's licence row", f.Row, f.Component)
		}
		anchors = append(anchors, f.Anchor)
	}
	if !slices.Contains(anchors, finding.AnchorLine) {
		t.Fatalf("anchors = %v, want the claim on the line the change touched anchored to it", anchors)
	}
	if slices.Contains(anchors, finding.AnchorNowhere) {
		t.Fatalf("anchors = %v, want every claim in a file the change touched anchored to it at least", anchors)
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d after the stage returned, want the base removed", got)
	}
}

// A component whose own dependencies could not be enumerated has had nothing
// decided about it, and a red row would ask its author to answer for a claim
// the gate never made.
func TestTheGoLicenceVerdictIsUnreadWhereTheDependenciesCouldNotBeRead(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/README.md": "no module here\n"},
		map[string]string{"api/README.md": "no module here either\n"})

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "api", "api", runner.Go, executil.Env{}))

	if !v.Gated || v.Err == nil {
		t.Fatalf("verdict = %+v, want the reason the dependencies could not be read", v)
	}
	if len(v.Findings) != 0 {
		t.Fatalf("claims = %+v, want none from a gate that decided nothing", v.Findings)
	}
	if want := []finding.Crash{{Gate: licence.Gate, Component: "api"}}; !slices.Equal(v.Crashes, want) {
		t.Errorf("crashes = %+v, want %+v", v.Crashes, want)
	}
}

// A Rust component governed by neither document has had no licence check run at
// all, and the verdict names that neither document decided rather than the
// pass of a check that ran and found nothing.
func TestTheRustLicenceVerdictNamesTheDocumentThatDecidedNothing(t *testing.T) {
	dir := t.TempDir()
	v := gateOne(t, dir, "", config.Default(), nil, licenceEntry(dir, "svc", ".", runner.Rust, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictNotConfigured {
		t.Fatalf("verdict = %+v, want one not-configured licence verdict", v)
	}
	if v.PolicySource != rust.PolicyFromNone {
		t.Fatalf("policy source = %q, want %q — neither document decided", v.PolicySource, rust.PolicyFromNone)
	}
	if len(v.Crashes) != 0 {
		t.Errorf("crashes = %+v, want none: nothing was asked of the gate", v.Crashes)
	}
}

// cargo-deny not being reachable is a gate that could not run, which is amber
// and names what failed — never the pass of a component whose crates nothing
// read.
func TestTheRustLicenceVerdictIsUnreadWhereCargoDenyCouldNotRun(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"svc/Cargo.lock": "version = 4\n"},
		map[string]string{"svc/Cargo.lock": "version = 4\n"})
	// A cache directory that cannot even be named, so nothing is installed and
	// nothing on the machine is run.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "svc", "svc", runner.Rust, executil.Env{}))

	if !v.Gated || v.Err == nil || v.Err.Error() == "" {
		t.Fatalf("verdict = %+v, want the reason the component could not be read", v)
	}
	if len(v.Crashes) != 1 {
		t.Errorf("crashes = %+v, want the licence gate named crashed", v.Crashes)
	}
}

// The gate's whole shape for a Rust component: a lockfile the merge-base did
// not carry is a set every crate of which the change introduced, the verdict
// fails and names the document that decided it, and each claim is anchored to
// what the change touched — without which every claim reaches the review
// surface at no anchor at all.
func TestTheRustLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"svc/README.md": "the component this change adds a lockfile to\n"},
		map[string]string{"svc/Cargo.lock": denyProbeLock(t)})
	cargoDenyStub(t, "deny-licenses-rejected.ndjson")

	// Line 12 of the probe's lockfile is the stanza naming cbindgen, the crate
	// the captured run rejected.
	changed := map[string][]int{"svc/Cargo.lock": {12}}
	v := gateOne(t, root, baseSHA, licenceConfig(), changed, licenceEntry(root, "svc", "svc", runner.Rust, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %+v, want a failing licence verdict", v)
	}
	if v.PolicySource != rust.PolicyFromLydite {
		t.Errorf("policy source = %q, want %q — the document that decided the licences", v.PolicySource, rust.PolicyFromLydite)
	}
	found := v.Findings
	if len(found) != 1 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	if found[0].Path != "svc/Cargo.lock" {
		t.Errorf("claim located at %q, want the component's lockfile from the scan root", found[0].Path)
	}
	if found[0].Anchor != finding.AnchorLine {
		t.Errorf("anchor = %q, want %q — the claim is on the line the change touched", found[0].Anchor, finding.AnchorLine)
	}
}

// A component's own deny.toml is evaluated whole and absolutely, so a failing
// verdict is about every crate it rejected rather than what this change
// introduced — and a passing one is about none.
func TestARustComponentsOwnPolicyCountsEveryCrateItRejected(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		verdict licence.Verdict
		pairs   int
	}{
		{"rejected", "deny-licenses-rejected.ndjson", licence.VerdictFail, 1},
		{"allowed", "deny-licenses-allowed.ndjson", licence.VerdictPass, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := fixture.Tree(t, filepath.Join("..", "..", "licence", "testdata", "denyprobe"))
			// A configuration of the component's own, which is what makes the
			// component rather than lydite the document that decided.
			write(t, dir, rust.DenyConfigFile, "[licenses]\nallow = [\"MIT\"]\n")
			cargoDenyStub(t, c.capture)

			v := gateOne(t, dir, "", config.Default(), nil, licenceEntry(dir, "svc", ".", runner.Rust, executil.Env{}))

			if !v.Gated || v.Err != nil || v.Comparison.Verdict != c.verdict {
				t.Fatalf("verdict = %+v, want one %q licence verdict", v, c.verdict)
			}
			if len(v.Comparison.Pairs) != c.pairs {
				t.Errorf("pairs = %+v, want %d", v.Comparison.Pairs, c.pairs)
			}
			if v.PolicySource != rust.PolicyFromConsumer {
				t.Errorf("policy source = %q, want %q — the document that decided", v.PolicySource, rust.PolicyFromConsumer)
			}
		})
	}
}

// A component's own deny.toml gates absolutely, so its gate is never handed a
// base: given a merge-base that carries the same deny.toml, cargo-deny runs
// once, in the component's own directory, and never in a worktree of the base.
func TestARustComponentsOwnPolicyIsNeverComparedAgainstTheMergeBase(t *testing.T) {
	files := map[string]string{
		"svc/Cargo.lock":            denyProbeLock(t),
		"svc/" + rust.DenyConfigFile: "[licenses]\nallow = [\"MIT\"]\n",
	}
	root, baseSHA := licenceBaseRepo(t, files, files)
	invocations := cargoDenyStub(t, "deny-licenses-rejected.ndjson")

	v := gateOne(t, root, baseSHA, config.Default(), nil, licenceEntry(root, "svc", "svc", runner.Rust, executil.Env{}))

	if v.PolicySource != rust.PolicyFromConsumer || v.Comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %+v, want the component's own policy failing absolutely", v)
	}
	body, err := os.ReadFile(invocations)
	if err != nil {
		t.Fatalf("reading the stub's invocations: %v", err)
	}
	ran := strings.Fields(string(body))
	if len(ran) != 1 {
		t.Fatalf("cargo-deny ran in %v, want once: the component's own policy reads no base", ran)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "svc"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := filepath.EvalSymlinks(ran[0]); err != nil || got != want {
		t.Fatalf("cargo-deny ran in %q, want the component's own directory %q", ran[0], want)
	}
}

// The gate's whole shape for a TypeScript component: a lockfile the merge-base
// did not carry is a set every dependency of which the change introduced, the
// verdict fails, and each claim is anchored to what the change touched —
// without which every claim reaches the review surface at no anchor at all.
func TestTheTypeScriptLicenceGateFailsAndAnchorsTheClaimsTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/README.md": "the component this change adds a manifest to\n"},
		tsProbeFiles(t, "npmprobe", "web"))

	// Line 10 of the probe's manifest is the dependency naming lightningcss,
	// which the stated policy rejects under MPL-2.0.
	changed := map[string][]int{"web/package.json": {10}}
	v := gateOne(t, root, baseSHA, licenceConfig(), changed, licenceEntry(root, "web", "web", runner.TypeScript, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %+v, want a failing licence verdict", v)
	}
	found := v.Findings
	// lightningcss, the two sharp-libvips builds and the entry stating no
	// licence at all: every dependency of the probe the allow-list rejects.
	if len(found) != 4 {
		t.Fatalf("claims = %+v, want one per introduced pair", found)
	}
	var direct, transitive int
	for _, f := range found {
		if f.Path != "web/package.json" {
			t.Errorf("claim located at %q, want the component's manifest from the scan root", f.Path)
		}
		if f.Line > 0 {
			direct++
			if f.Anchor != finding.AnchorLine {
				t.Errorf("%q anchored %q, want %q — it is on the manifest line the change touched", f.Message, f.Anchor, finding.AnchorLine)
			}
			continue
		}
		transitive++
		// A package the manifest names on no line reaches the change nowhere,
		// however much of that manifest the change edited: an anchor here would
		// put a transitive dependency's claim on a line whose edit does nothing
		// about it.
		if f.Anchor != finding.AnchorNowhere {
			t.Errorf("%q anchored %q, want %q — it is reached only transitively", f.Message, f.Anchor, finding.AnchorNowhere)
		}
	}
	if direct != 1 || transitive != 3 {
		t.Fatalf("claims = %d direct and %d transitive, want lightningcss located and the other three not", direct, transitive)
	}
}

// A dependency the merge-base already carried is not this change's to answer
// for, however many of them the allow-list rejects: the verdict passes and
// makes no claim at all.
func TestTheTypeScriptLicenceGatePassesWhereTheBaseCarriedTheSameLockfile(t *testing.T) {
	files := tsProbeFiles(t, "npmprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "web", "web", runner.TypeScript, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictPass {
		t.Fatalf("verdict = %+v, want one passing licence verdict", v)
	}
	if len(v.Findings) != 0 {
		t.Fatalf("claims = %+v, want none: the base carried every pair", v.Findings)
	}
}

// yarn states no dependency licence in its lockfile and scan runs no install to
// produce one, so a component with no installed tree beside it has had nothing
// decided about it. Amber naming what was missing, never the green of a gate
// whose dependencies nothing read.
func TestTheTypeScriptLicenceVerdictIsUnreadWhereNoLicenceSourceExists(t *testing.T) {
	files := tsProbeFiles(t, "yarnprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "web", "web", runner.TypeScript, executil.Env{}))

	if !v.Gated || v.Err == nil || v.Err.Error() == "" {
		t.Fatalf("verdict = %+v, want the reason the component could not be read", v)
	}
	if len(v.Findings) != 0 {
		t.Fatalf("claims = %+v, want none from a gate that decided nothing", v.Findings)
	}
	if want := []finding.Crash{{Gate: licence.Gate, Component: "web"}}; !slices.Equal(v.Crashes, want) {
		t.Errorf("crashes = %+v, want %+v", v.Crashes, want)
	}
}

// A repository that stated no policy gets the not-configured verdict — not the
// unread dependencies of a manager whose licences could not be read, which is
// an answer to a question nobody asked here.
func TestTheTypeScriptLicenceVerdictIsNotConfiguredWithoutAPolicy(t *testing.T) {
	dir := fixture.Tree(t, filepath.Join("..", "..", "typescript", "testdata", "yarnprobe"))

	v := gateOne(t, dir, "", config.Default(), nil, licenceEntry(dir, "web", ".", runner.TypeScript, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictNotConfigured {
		t.Fatalf("verdict = %+v, want one not-configured licence verdict", v)
	}
	if len(v.Crashes) != 0 {
		t.Errorf("crashes = %+v, want none: nothing was asked of the gate", v.Crashes)
	}
}

// The base a TypeScript component is compared against is the set its own
// lockfile resolved at the merge-base, read in the checked-out tree rather than
// from a stored figure: an entry written by a lydite that computed no licences
// reads back as the empty set, and the delta on the day of the upgrade is then
// the absolute set.
func TestTheTypeScriptLicenceBaseReadsTheLockfileAtTheMergeBase(t *testing.T) {
	files := tsProbeFiles(t, "npmprobe", "web")
	root, baseSHA := licenceBaseRepo(t, files, files)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := typescriptLicenceBase(context.Background(), tree, "web", licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	want := []string{"@img/sharp-libvips-darwin-arm64", "@img/sharp-libvips-linux-x64", "lightningcss", "unlicensed-probe"}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, want) {
		t.Fatalf("base set = %v, want %v — the probe's rejected dependencies, read under the stated policy", got, want)
	}
}

// What has to be at the base for the component to have been there is its
// manifest, and not the one lockfile that states licences.
//
// A yarn component declares no package-lock.json in any tree, so gating on one
// would make every base read as a component the change adds — a measured empty
// set, against which every dependency the repository already shipped is
// introduced, and a failing verdict on a change that touched none of them.
func TestTheTypeScriptLicenceBaseIsTheManifestAndNotTheNpmLockfile(t *testing.T) {
	files := tsProbeFiles(t, "yarnprobe", "web")
	// An installed tree, the only licence source a yarn component has, and one
	// both sides of the comparison carry.
	files["web/node_modules/lightningcss/package.json"] = `{"name":"lightningcss","version":"1.33.0","license":"MPL-2.0"}`
	root, baseSHA := licenceBaseRepo(t, files, files)

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "web", "web", runner.TypeScript, executil.Env{}))

	if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictPass {
		t.Fatalf("verdict = %+v, want one passing licence verdict: the base carried the same installed tree", v)
	}
}

// A component with no manifest at the merge-base is one this change adds, which
// is a measured empty set and never an unmeasured base: nothing failed, there
// was nothing there.
func TestTheTypeScriptLicenceBaseIsEmptyWhereTheComponentWasNotThere(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/README.md": "the component this change adds a manifest to\n"},
		tsProbeFiles(t, "npmprobe", "web"))
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := typescriptLicenceBase(context.Background(), tree, "web", licence.NewPolicy(licenceConfig().Licence.Policy.Allow))
	if base.State() != licence.Measured || base.Set().Len() != 0 {
		t.Fatalf("base = %q holding %d, want a measured empty set for a component the base did not carry", base.State(), base.Set().Len())
	}
}

// The reason a base could not be built reaches the caller, and the tree inside
// it never does: a directory answered beside a reason would be read as the
// checkout that did not happen, and every component measured against it.
func TestABaseWorktreeThatWouldNotCheckOutAnswersNoTree(t *testing.T) {
	root, _ := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, strings.Repeat("0123456789", 4))
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason == "" {
		t.Fatal("a merge-base that does not exist checked out")
	}
	if dir != "" {
		t.Fatalf("dir = %q, want none beside a reason", dir)
	}
	// Decided once: the second component asks the same question and is answered
	// from what the first attempt recorded.
	if again, sameReason := tree.open(context.Background()); again != "" || sameReason != reason {
		t.Fatalf("second open = %q/%q, want the first attempt's answer", again, sameReason)
	}
}

// The checkout succeeding answers the scan root inside the worktree, which is
// what every component's base is located under.
func TestABaseWorktreeAnswersTheScanRootInsideIt(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason != "" {
		t.Fatalf("checking out the merge-base: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(dir, "api", depsManifest)); err != nil {
		t.Fatalf("the base tree holds no manifest at %s: %v", dir, err)
	}
}

// A worktree with nowhere to be created answers no tree either, and names the
// step that failed rather than the checkout that was never reached: a directory
// answered beside a reason is read as the checkout that did not happen, and
// every component is measured against it.
func TestABaseWorktreeWithNoTemporaryDirectoryAnswersNoTree(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	// A file where the temporary directory belongs, so nothing can be created
	// under it.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)

	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	dir, reason := tree.open(context.Background())
	if reason == "" {
		t.Fatal("a worktree with nowhere to live was created")
	}
	if dir != "" {
		t.Fatalf("dir = %q, want none beside a reason", dir)
	}
	if !strings.Contains(reason, "no temporary directory") {
		t.Errorf("reason = %q, want the step that failed", reason)
	}
	if strings.Contains(reason, shortSHA(baseSHA)) {
		t.Errorf("reason = %q, want it distinct from a merge-base that would not check out", reason)
	}
}

// A worktree that cannot be created is a base nothing can be measured against,
// and it says so rather than falling through to a measured empty set that fails
// every component over dependencies it already shipped.
func TestABaseWorktreeThatCannotBeCreatedIsUnmeasured(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	// A file where the temporary directory belongs, so nothing can be created
	// under it.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked)

	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())
	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Unmeasured {
		t.Fatalf("base state = %q, want unmeasured", base.State())
	}
	if !strings.Contains(base.Reason(), "temporary directory") {
		t.Errorf("reason = %q, want the step that failed", base.Reason())
	}
}

// The whole of what the gate is: the set at the merge-base, not the set at the
// head. A base pointed at the wrong tree reads as no manifest at all, which is
// a measured empty set — and every dependency the repository already shipped
// then reads as one this change introduced.
func TestTheLicenceGateFailsOnThePairTheChangeIntroduced(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured at the merge-base", base.State(), base.Reason())
	}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, []string{"golden"}) {
		t.Fatalf("base set = %v, want the one pair the merge-base carried", got)
	}

	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	got := licence.Compare(permissivePolicy(), current, base)
	if got.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want fail — the change added a pair the merge-base did not carry", got.Verdict)
	}
	if names := packagesOf(got.Pairs); !slices.Equal(names, []string{"copyleft"}) {
		t.Fatalf("introduced = %v, want copyleft alone — golden was grandfathered", names)
	}
}

// The other half of the same gate. A pair already at the merge-base is one the
// change did not introduce, however non-conforming it is.
func TestTheLicenceGatePassesAChangeThatIntroducesNoPair(t *testing.T) {
	deps := map[string]string{"api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"}
	root, baseSHA := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	got := licence.Compare(permissivePolicy(), current, base)
	if got.Verdict != licence.VerdictPass {
		t.Fatalf("verdict = %q (%s), want pass — both pairs were already at the merge-base", got.Verdict, got.Reason)
	}
}

// A component this change adds has no manifest at the base, which is a measured
// empty set and not an unmeasured base: nothing failed, there was nothing
// there. Reporting `unmeasured` would take the gate off the one change that
// brings a whole dependency set with it.
func TestAComponentAbsentFromTheMergeBaseMeasuresAnEmptySet(t *testing.T) {
	root, baseSHA := licenceBaseRepo(t,
		map[string]string{"web/app.ts": "export const x = 1;\n"},
		map[string]string{"api/" + depsManifest: "copyleft GPL-3.0-only\n"})
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured: the component was not there", base.State(), base.Reason())
	}
	if got := base.Set().Len(); got != 0 {
		t.Fatalf("base holds %d pair(s), want none", got)
	}
	current, err := readDeps(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	if got := licence.Compare(permissivePolicy(), current, base); got.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want fail — every pair a new component carries is one the change introduces", got.Verdict)
	}
}

// A worktree holds the whole repository and the scan root may sit below it. A
// base located from the worktree root instead finds no manifest, calls that an
// empty set, and every dependency the component already had reads as new.
func TestTheLicenceBaseLocatesAComponentThroughTheScanRootPrefix(t *testing.T) {
	repo, baseSHA := licenceBaseRepo(t,
		map[string]string{"source/api/" + depsManifest: "golden MIT\n"},
		map[string]string{"source/api/" + depsManifest: "golden MIT\ncopyleft GPL-3.0-only\n"})
	root := filepath.Join(repo, "source")
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.Measured {
		t.Fatalf("base state = %q (%s), want it measured under the scan root's prefix", base.State(), base.Reason())
	}
	if got := packagesOf(base.Set().Dependencies()); !slices.Equal(got, []string{"golden"}) {
		t.Fatalf("base set = %v, want the pair the merge-base carried below source/", got)
	}
}

// One commit, one checkout. A worktree per component extracts the identical
// merge-base once per declaration, and nothing in a passing report says it
// happened.
func TestOneWorktreeServesEveryComponentsLicenceBase(t *testing.T) {
	deps := map[string]string{
		"api/" + depsManifest: "golden MIT\n",
		"web/" + depsManifest: "silver MIT\n",
	}
	root, baseSHA := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, baseSHA)
	defer tree.close(context.Background())

	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d before any component asked for a base, want the repository's own alone", got)
	}
	for _, dir := range []string{"api", "web"} {
		if base := tree.set(context.Background(), dir, depsManifest, readDeps); base.State() != licence.Measured {
			t.Fatalf("%s base state = %q (%s), want it measured", dir, base.State(), base.Reason())
		}
	}
	if got := worktrees(t, root); got != 2 {
		t.Fatalf("worktrees = %d, want one shared base beside the repository's own", got)
	}

	tree.close(context.Background())
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d after the scan, want the base removed — a registered worktree pointing at nothing trips every later `git worktree add`", got)
	}
}

// A scan whose components ask for no base pays for no checkout: no diff base at
// all is the shape `lydite scan` on `main` has, and it gates nothing.
func TestARunWithNoDiffBaseChecksOutNoWorktree(t *testing.T) {
	root, _ := licenceBaseRepo(t,
		map[string]string{"api/" + depsManifest: "golden MIT\n"},
		map[string]string{"api/" + depsManifest: "golden MIT\n"})
	tree := newLicenceBaseTree(root, "")
	defer tree.close(context.Background())

	base := tree.set(context.Background(), "api", depsManifest, readDeps)
	if base.State() != licence.NoBase {
		t.Fatalf("base state = %q, want no base: the run was given no diff base", base.State())
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d, want no base checked out for a run that compares against nothing", got)
	}
}

// A base that could not be built gates nothing and says so — for every
// component, with the same reason, because the checkout it names was attempted
// once. Falling through to a measured empty set would fail each of them over
// dependencies nobody in the change chose.
func TestABaseThatWillNotCheckOutIsUnmeasuredForEveryComponent(t *testing.T) {
	deps := map[string]string{
		"api/" + depsManifest: "golden MIT\n",
		"web/" + depsManifest: "silver MIT\n",
	}
	root, _ := licenceBaseRepo(t, deps, deps)
	tree := newLicenceBaseTree(root, strings.Repeat("0123456789", 4))
	defer tree.close(context.Background())

	var reasons []string
	for _, dir := range []string{"api", "web"} {
		base := tree.set(context.Background(), dir, depsManifest, readDeps)
		if base.State() != licence.Unmeasured {
			t.Fatalf("%s base state = %q, want unmeasured: the merge-base would not check out", dir, base.State())
		}
		if base.Reason() == "" {
			t.Fatalf("%s base names no reason, want the step that failed", dir)
		}
		reasons = append(reasons, base.Reason())
	}
	if reasons[0] != reasons[1] {
		t.Fatalf("reasons = %q, want one answer decided once for the whole scan", reasons)
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d, want no registered base after a checkout that failed", got)
	}
}

// A gate that panics part-way through reading its base still leaves no
// worktree behind: the removal is deferred in the stage's own body, so it runs
// on the way out of a panic as it does on a return. A registered worktree
// pointing at nothing trips every later `git worktree add` in the repository.
func TestAGateThatPanicsReadingItsBaseStillRemovesTheWorktree(t *testing.T) {
	deps := map[string]string{"api/" + depsManifest: "golden MIT\n"}
	root, baseSHA := licenceBaseRepo(t, deps, deps)

	var tmp string
	var during int
	panicking := func(runner.Lang) (licenceGate, error) {
		return func(ctx context.Context, tree *licenceBaseTree, p Planned, _ licence.Policy, _ map[string][]int) LicenceVerdict {
			tree.set(ctx, p.Component.Dir, depsManifest, func(string) (licence.Set, error) {
				tmp = tree.tmp
				during = worktrees(t, root)
				panic("a base read that panics")
			})
			return LicenceVerdict{}
		}, nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the gate's panic did not reach the stage's caller")
			}
		}()
		_, _ = gateLicences(context.Background(), GateLicencesIn{
			Dir: root, BaseSHA: baseSHA, Config: licenceConfig(),
			Plan: []Planned{licenceEntry(root, "api", "api", runner.Go, executil.Env{})},
		}, panicking)
	}()

	if tmp == "" || during != 2 {
		t.Fatalf("worktree %q with %d registered while the base was read, want it checked out — its removal proves nothing otherwise", tmp, during)
	}
	if got := worktrees(t, root); got != 1 {
		t.Fatalf("worktrees = %d after the panic, want the base removed", got)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat %s = %v, want the worktree's directory gone", tmp, err)
	}
}

// Each entry's verdict sits at its own index, and an entry that is not Scan —
// or a Scan entry whose language declares no dependency set — answers the zero
// value, marked not gated, so a caller walking the plan never pairs one
// component's verdict with another's.
func TestGateLicencesAnswersEachEntryAtItsOwnIndex(t *testing.T) {
	root := t.TempDir()
	plan := []Planned{
		licenceEntry(root, "api", "api", runner.Go, executil.Env{}),
		{Component: component.Component{Name: "legacy"}, Disposition: Unscanned},
		licenceEntry(root, "scripts", "scripts", runner.Shell, executil.Env{}),
		{Component: component.Component{Name: "tools"}, Lang: runner.Shell, Disposition: Disabled},
		licenceEntry(root, "svc", "svc", runner.Rust, executil.Env{}),
		licenceEntry(root, "web", "web", runner.TypeScript, executil.Env{}),
		{Component: component.Component{Name: "api-integration"}, Lang: runner.Go, Disposition: Duplicate, DuplicateOf: "api"},
	}
	// No policy stated, so every gate answers without running a tool.
	out, err := GateLicences(context.Background(), GateLicencesIn{Dir: root, Plan: plan, Config: config.Default()})
	if err != nil {
		t.Fatalf("GateLicences: %v", err)
	}
	if len(out.Licences) != len(plan) {
		t.Fatalf("licences = %d entries for a plan of %d, want one per entry", len(out.Licences), len(plan))
	}
	for _, i := range []int{1, 2, 3, 6} {
		if !reflect.DeepEqual(out.Licences[i], LicenceVerdict{}) {
			t.Errorf("entry %d (%s) = %+v, want the zero verdict", i, plan[i].Component.Name, out.Licences[i])
		}
	}
	for _, i := range []int{0, 4, 5} {
		v := out.Licences[i]
		if !v.Gated || v.Err != nil || v.Comparison.Verdict != licence.VerdictNotConfigured {
			t.Errorf("entry %d (%s) = %+v, want a gated, not-configured verdict", i, plan[i].Component.Name, v)
		}
	}
	if got := out.Licences[4].PolicySource; got != rust.PolicyFromNone {
		t.Errorf("svc's policy source = %q, want %q", got, rust.PolicyFromNone)
	}
	if got := out.Licences[0].PolicySource; got != "" {
		t.Errorf("api's policy source = %q, want none: only Rust names a policy source", got)
	}
}

// Shell declares no dependency set to read licences from, so its entry is
// answered as not gated whatever the policy and the base — the caller's cue
// to say so on a row of its own, rather than leave a gap that reads as a gate
// that ran and found nothing.
func TestAShellComponentIsAnsweredAsHavingNoLicenceGate(t *testing.T) {
	deps := map[string]string{"scripts/run.sh": "#!/bin/sh\n"}
	root, baseSHA := licenceBaseRepo(t, deps, deps)

	v := gateOne(t, root, baseSHA, licenceConfig(), nil, licenceEntry(root, "scripts", "scripts", runner.Shell, executil.Env{}))

	if !reflect.DeepEqual(v, LicenceVerdict{}) {
		t.Fatalf("verdict = %+v, want the zero verdict, not gated", v)
	}
}

// A language the switch does not name is refused before any gate runs or any
// base is checked out, never handed another language's gate.
func TestGateLicencesRefusesALanguageItHasNoGateFor(t *testing.T) {
	root := t.TempDir()
	plan := []Planned{
		licenceEntry(root, "api", "api", runner.Go, executil.Env{}),
		licenceEntry(root, "tools", "tools", runner.Python, executil.Env{}),
	}
	_, err := GateLicences(context.Background(), GateLicencesIn{Dir: root, Plan: plan, Config: licenceConfig()})
	if err == nil {
		t.Fatal("GateLicences accepted a planned language it has no licence gate for")
	}
	if !strings.Contains(err.Error(), "tools") || !strings.Contains(err.Error(), string(runner.Python)) {
		t.Errorf("error = %v, want the component and its language named", err)
	}

	ran := false
	gateFor := func(l runner.Lang) (licenceGate, error) {
		if l != runner.Go {
			return licenceGateFor(l)
		}
		return func(context.Context, *licenceBaseTree, Planned, licence.Policy, map[string][]int) LicenceVerdict {
			ran = true
			return LicenceVerdict{}
		}, nil
	}
	if _, err := gateLicences(context.Background(), GateLicencesIn{Dir: root, Plan: plan, Config: licenceConfig()}, gateFor); err == nil {
		t.Fatal("gateLicences accepted a planned language it has no licence gate for")
	}
	if ran {
		t.Error("a gate ran ahead of the refusal, want the plan refused before anything is read")
	}

	for _, lang := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript, runner.Shell} {
		if _, err := licenceGateFor(lang); err != nil {
			t.Errorf("licenceGateFor(%s): %v, want every language with a scanner handled", lang, err)
		}
	}
}

// The licence gate claims only the pairs a failing verdict is about, so every
// verdict that gates nothing — and a set that could not be read — reports no
// pair whatever conforms, and is named crashed rather than read as clean.
func TestLicenceCrashedOnlyWhereTheGateClaimedNothingItCouldStandBehind(t *testing.T) {
	cases := []struct {
		c    licence.Comparison
		err  error
		want bool
	}{
		{licence.Comparison{Verdict: licence.VerdictPass}, nil, false},
		{licence.Comparison{Verdict: licence.VerdictFail}, nil, false},
		{licence.Comparison{Verdict: licence.VerdictUnmeasured}, nil, true},
		{licence.Comparison{Verdict: licence.VerdictContext}, nil, true},
		{licence.Comparison{}, errors.New("go list failed"), true},
	}
	for _, tc := range cases {
		got := licenceCrashed("api", tc.c, tc.err)
		if (len(got) == 1) != tc.want {
			t.Errorf("verdict %q, err %v: crashed %v, want %v", tc.c.Verdict, tc.err, len(got) == 1, tc.want)
		}
		if tc.want && got[0] != (finding.Crash{Gate: licence.Gate, Component: "api"}) {
			t.Errorf("crash = %+v, want the licence gate naming the component", got[0])
		}
	}
}
