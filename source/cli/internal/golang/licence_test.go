package golang

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/fixture"
	"lydite/lydite/internal/licence"
)

// permissive is the allow-list ADR 0038 states as the shape of a real one: it
// holds the two halves of `gopkg.in/yaml.v2`'s expression and neither of the
// probe's copyleft licences.
var permissive = []string{"Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MIT"}

// probe materialises the captured probe module, whose dependency set covers
// strong copyleft, weak copyleft and a module carrying two licence files.
func probe(t *testing.T) string {
	t.Helper()
	return fixture.Tree(t, filepath.Join("..", "licence", "testdata", "goprobe"))
}

func set(t *testing.T, dir string, allow []string) licence.Set {
	t.Helper()
	s, err := LicenceSet(context.Background(), dir, nil, licence.NewPolicy(allow))
	if err != nil {
		t.Fatalf("reading the probe's dependencies: %v", err)
	}
	return s
}

// The classification is the whole gate's input, and the three cases it has to
// get right are the three the probe carries: strong copyleft rejected, weak
// copyleft rejected, and a module whose two licence files are read as one
// expression either half of which satisfies the policy.
func TestAPermissivePolicyRejectsTheCopyleftModulesAndPassesTheDuallyLicensedOne(t *testing.T) {
	got := packages(set(t, probe(t), permissive))
	want := []string{"github.com/hashicorp/go-version", "github.com/juju/errors"}
	if !slices.Equal(got, want) {
		t.Fatalf("rejected = %v, want %v — gopkg.in/yaml.v2 conforms through either half of its expression", got, want)
	}
}

// Both licence files at a module root, read as an SPDX OR. A classifier that
// stopped at the first file reports Apache-2.0 alone, and the MIT half of
// gopkg.in/yaml.v2 is gone — which is the drop that decided against
// go-licenses.
func TestEveryLicenceFileAtAModuleRootReachesTheExpression(t *testing.T) {
	// An allow-list satisfying nothing in the probe, so every module reaches
	// the set with the expression its files classified as.
	s := set(t, probe(t), []string{"0BSD"})
	for _, d := range s.Dependencies() {
		if d.Package != "gopkg.in/yaml.v2" {
			continue
		}
		if d.Licence != "Apache-2.0 OR MIT" {
			t.Fatalf("gopkg.in/yaml.v2 = %q, want both of its licence files", d.Licence)
		}
		return
	}
	t.Fatal("gopkg.in/yaml.v2 reached no set at all")
}

// The version is read and reported, because it is what a reader needs in order
// to find the dependency the claim is about.
func TestEachRejectedDependencyCarriesItsVersion(t *testing.T) {
	for _, d := range set(t, probe(t), permissive).Dependencies() {
		if d.Version == "" {
			t.Errorf("%s reached the set with no version", d.Package)
		}
	}
}

// A copyleft dependency the base did not carry fails the row, with the claim
// on the require line naming it.
func TestACopyleftModuleAbsentFromTheBaseFailsOnItsRequireLine(t *testing.T) {
	dir := probe(t)
	// The base carried the MPL module and not the LGPL one, so exactly one
	// pair is introduced.
	base := licence.MeasuredBase(licence.NewSet(licence.Dependency{
		Package: "github.com/hashicorp/go-version", Version: "v1.9.0", Licence: "MPL-2.0",
	}))
	comparison, found, err := LicenceCheck(context.Background(), dir, nil, licence.NewPolicy(permissive), base)
	if err != nil {
		t.Fatalf("checking the probe: %v", err)
	}
	if comparison.Verdict != licence.VerdictFail {
		t.Fatalf("verdict = %q, want fail — the change introduced an LGPL dependency", comparison.Verdict)
	}
	if len(found) != 1 {
		t.Fatalf("findings = %d, want the one introduced pair: %v", len(found), found)
	}
	f := found[0]
	if f.Gate != licence.Gate || f.Path != goModFile {
		t.Errorf("finding located at %s in %q, want the manifest under the licence gate", f.Path, f.Gate)
	}
	// Line 7 of the probe's manifest is `github.com/juju/errors v1.0.0`.
	if f.Line != 7 {
		t.Errorf("line = %d, want 7 — the require naming github.com/juju/errors", f.Line)
	}
	if f.Site != "licence\x1fgithub.com/juju/errors LGPL-3.0" {
		t.Errorf("site = %q, want the package and its licence", f.Site)
	}
	if !strings.Contains(f.Message, "github.com/juju/errors") || !strings.Contains(f.Message, "LGPL-3.0") {
		t.Errorf("message = %q, want the package and the licence named", f.Message)
	}
}

// A pair the merge-base already held is grandfathered: the change did not
// introduce it, and a gate that fired on it would fail every adopting
// repository over the licences it already ships.
func TestACopyleftModuleTheBaseAlreadyHeldIsGrandfathered(t *testing.T) {
	dir := probe(t)
	base := licence.MeasuredBase(set(t, dir, permissive))
	comparison, found, err := LicenceCheck(context.Background(), dir, nil, licence.NewPolicy(permissive), base)
	if err != nil {
		t.Fatalf("checking the probe: %v", err)
	}
	if comparison.Verdict != licence.VerdictPass {
		t.Fatalf("verdict = %q, want pass — every non-conforming pair was already there", comparison.Verdict)
	}
	if len(found) != 0 {
		t.Fatalf("findings = %v, want none: a grandfathered pair is nobody's to answer for", found)
	}
}

// A bump that leaves the licence where it was is the pair that was already
// there. Keyed on the version it would read as new and fail the row on the
// ordinary maintenance the gate has no claim about.
func TestABumpThatLeavesTheLicenceUnchangedIntroducesNothing(t *testing.T) {
	dir := probe(t)
	var base licence.Set
	for _, d := range set(t, dir, permissive).Dependencies() {
		d.Version = "v0.0.1-before-the-bump"
		base.Add(d)
	}
	comparison, found, err := LicenceCheck(context.Background(), dir, nil, licence.NewPolicy(permissive), licence.MeasuredBase(base))
	if err != nil {
		t.Fatalf("checking the probe: %v", err)
	}
	if comparison.Verdict != licence.VerdictPass || len(found) != 0 {
		t.Fatalf("verdict = %q with %d findings, want a pass: only the versions moved", comparison.Verdict, len(found))
	}
}

// No policy, no enumeration and no verdict. The row is `not configured`, and
// the dependencies are never listed: `go list -deps` downloads every module the
// build compiles, and a repository whose answer is already known must not pay
// for it.
func TestAnUnstatedPolicyGatesNothingAndEnumeratesNothing(t *testing.T) {
	// A directory holding no module at all. A run that reached `go list` here
	// would answer an error rather than a verdict.
	comparison, found, err := LicenceCheck(context.Background(), t.TempDir(), nil, licence.NewPolicy(nil), licence.NoDiffBase())
	if err != nil {
		t.Fatalf("an unstated policy read a module: %v", err)
	}
	if comparison.Verdict != licence.VerdictNotConfigured {
		t.Fatalf("verdict = %q, want not-configured", comparison.Verdict)
	}
	if comparison.Verdict.Gating() {
		t.Error("an unstated policy reached a gating verdict")
	}
	if len(found) != 0 {
		t.Fatalf("findings = %v, want none from a gate nothing configured", found)
	}
}

// A base that could not be built gates nothing, says why, and is never folded
// into a pass.
func TestABaseThatCouldNotBeBuiltIsUnmeasuredAndNamesWhatFailed(t *testing.T) {
	comparison, found, err := LicenceCheck(context.Background(), probe(t), nil, licence.NewPolicy(permissive),
		licence.UnmeasuredBase("checking out deadbee: no such commit"))
	if err != nil {
		t.Fatalf("checking the probe: %v", err)
	}
	if comparison.Verdict != licence.VerdictUnmeasured {
		t.Fatalf("verdict = %q, want unmeasured", comparison.Verdict)
	}
	if comparison.Verdict.Gating() {
		t.Error("a base that could not be built reached a gating verdict")
	}
	if !strings.Contains(comparison.Reason, "no such commit") {
		t.Errorf("reason = %q, want the step that failed", comparison.Reason)
	}
	// The current set as context, so a reader sees what the gate would have
	// compared — and no claim, because nothing was decided.
	if len(comparison.Pairs) == 0 {
		t.Error("an unmeasured base reported no context at all")
	}
	if len(found) != 0 {
		t.Fatalf("findings = %v, want none from a gate that could not compare", found)
	}
}

// A run given no diff base reports the whole non-conforming set and gates
// nothing, which is the shape a scan with no --diff-base already has.
func TestNoDiffBaseReportsTheSetAsContext(t *testing.T) {
	comparison, found, err := LicenceCheck(context.Background(), probe(t), nil, licence.NewPolicy(permissive), licence.NoDiffBase())
	if err != nil {
		t.Fatalf("checking the probe: %v", err)
	}
	if comparison.Verdict != licence.VerdictContext {
		t.Fatalf("verdict = %q, want context", comparison.Verdict)
	}
	if len(comparison.Pairs) != 2 {
		t.Fatalf("pairs = %v, want the whole non-conforming set", comparison.Pairs)
	}
	if len(found) != 0 {
		t.Fatalf("findings = %v, want none from a run gating nothing", found)
	}
}

// A component whose dependencies could not be enumerated has had nothing
// decided about it, and the error reaches the caller rather than a set that
// looks clean.
func TestAModuleThatWillNotEnumerateAnswersAnErrorRatherThanAnEmptySet(t *testing.T) {
	_, _, err := LicenceCheck(context.Background(), t.TempDir(), nil, licence.NewPolicy(permissive), licence.NoDiffBase())
	if err == nil {
		t.Fatal("a directory holding no module answered a set")
	}
	if !strings.Contains(err.Error(), "go list") {
		t.Errorf("error = %v, want the step that failed named", err)
	}
}

// The gate seeds the ledger's findings map, so a Go component nothing was
// found in records a nought rather than a gap. `findingCounts` reads the set
// from `scannerGates`, which reads it from here.
func TestTheLicenceGateIsOneOfThisPackagesFindingGates(t *testing.T) {
	if !slices.Contains(FindingGates(), GateLicence) {
		t.Fatalf("FindingGates() = %v, want the licence gate among them", FindingGates())
	}
	if GateLicence != licence.Gate {
		t.Errorf("GateLicence = %q, want the one name every language's row carries", GateLicence)
	}
}

// A vendored module answers no Module.Dir, and its licence files sit at
// `vendor/<module path>` — above the subpackage directory `go list` reports.
// Reading Module.Dir alone classifies every dependency of every vendoring
// repository as unknown.
func TestAVendoredSubpackageResolvesToItsModuleRoot(t *testing.T) {
	root := filepath.Join("vendor", "gopkg.in", "yaml.v2")
	cases := []struct {
		name string
		pkg  goListPackage
		want string
	}{
		{"module directory when it is answered", goListPackage{
			ImportPath: "gopkg.in/yaml.v2",
			Dir:        filepath.Join("cache", "yaml.v2@v2.4.0"),
			Module:     &goListModule{Path: "gopkg.in/yaml.v2", Dir: filepath.Join("cache", "yaml.v2@v2.4.0")},
		}, filepath.Join("cache", "yaml.v2@v2.4.0")},
		{"vendored module root", goListPackage{
			ImportPath: "gopkg.in/yaml.v2",
			Dir:        root,
			Module:     &goListModule{Path: "gopkg.in/yaml.v2"},
		}, root},
		{"vendored subpackage walks back to the root", goListPackage{
			ImportPath: "gopkg.in/yaml.v2/internal/scanner",
			Dir:        filepath.Join(root, "internal", "scanner"),
			Module:     &goListModule{Path: "gopkg.in/yaml.v2"},
		}, root},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := moduleDir(c.pkg); got != c.want {
				t.Errorf("moduleDir = %q, want %q", got, c.want)
			}
		})
	}
}

// The names a licence file carries, matched case-insensitively by prefix. An
// exact-name match reads one of the probe's four files and calls the rest
// absent.
func TestLicenceFilesAreMatchedCaseInsensitivelyByPrefix(t *testing.T) {
	for _, name := range []string{"LICENSE", "License", "license", "LICENSE.txt", "LICENSE.libyaml", "LICENCE", "COPYING", "copying.md"} {
		if !isLicenceFile(name) {
			t.Errorf("%q is not read as a licence file", name)
		}
	}
	// A prefix match reads a file whose name merely starts the same way, and
	// that is the accepted side of the trade: a file licensecheck recognises
	// nothing in contributes no identifier, while an exact list of filenames
	// loses four of the probe's own.
	for _, name := range []string{"go.mod", "README.md", "NOTICE", "AUTHORS"} {
		if isLicenceFile(name) {
			t.Errorf("%q is read as a licence file", name)
		}
	}
}

// The main module and the standard library are not dependencies. A repository's
// own licence is not this gate's to judge, and a standard-library package
// carries no module at all.
func TestTheMainModuleAndTheStandardLibraryAreNotDependencies(t *testing.T) {
	deps := licenceDependencies([]goListPackage{
		{ImportPath: "fmt", Standard: true},
		{ImportPath: "lydite/lydite", Module: &goListModule{Path: "lydite/lydite", Main: true}},
		{ImportPath: "example.com/a", Module: &goListModule{Path: "example.com/a", Version: "v1.0.0"}},
		{ImportPath: "example.com/a/sub", Module: &goListModule{Path: "example.com/a", Version: "v1.0.0"}},
	})
	if len(deps) != 1 || deps[0].Package != "example.com/a" {
		t.Fatalf("dependencies = %v, want the one dependency module, once", deps)
	}
	// No licence file anywhere to read, so the licence is Unknown rather than
	// empty: a dependency dropped because its licence could not be determined
	// is a dependency the gate silently allowed.
	if deps[0].Licence != licence.Unknown {
		t.Errorf("licence = %q, want %q", deps[0].Licence, licence.Unknown)
	}
}

// A module reached only transitively is named by no manifest line, and reports
// zero rather than a guess: after a finding became a review thread, a guessed
// line is a thread on code that has nothing to do with the claim.
func TestATransitiveModuleLocatesAtNoLine(t *testing.T) {
	found := LicenceFindings(probe(t), []licence.Dependency{
		{Package: "example.com/never-required", Version: "v1.0.0", Licence: "GPL-3.0"},
	})
	if len(found) != 1 || found[0].Line != 0 {
		t.Fatalf("findings = %v, want one located at no line", found)
	}
}

// Two licences against one module are two claims on one line, which is why the
// site is the pair and not the text of that line.
func TestTwoLicencesAgainstOneModuleAreTwoClaims(t *testing.T) {
	found := LicenceFindings(probe(t), []licence.Dependency{
		{Package: "github.com/juju/errors", Version: "v1.0.0", Licence: "LGPL-3.0"},
		{Package: "github.com/juju/errors", Version: "v1.0.0", Licence: "GPL-3.0"},
	})
	if len(found) != 2 {
		t.Fatalf("findings = %d, want one per licence", len(found))
	}
	if found[0].Site == found[1].Site {
		t.Fatalf("both claims share the site %q, so one of them is lost", found[0].Site)
	}
	if found[0].Line != found[1].Line || found[0].Line != 7 {
		t.Errorf("lines = %d and %d, want both on the require naming the module", found[0].Line, found[1].Line)
	}
}

// packages is each dependency's package, in the set's own order.
func packages(s licence.Set) []string {
	var out []string
	for _, d := range s.Dependencies() {
		out = append(out, d.Package)
	}
	return out
}
