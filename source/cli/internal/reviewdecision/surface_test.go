package reviewdecision

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// repo commits baseFiles as the base revision, then headFiles on top under
// the message given, and returns the repository directory and the base SHA.
func repo(t *testing.T, baseFiles, headFiles map[string]string, message ...string) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	head := "head"
	if len(message) > 0 {
		head = message[0]
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write(baseFiles)
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write(headFiles)
	run("add", "-A")
	run("commit", "--allow-empty", "-m", head)
	return dir, base
}

// A Rust crate whose public API a comparison would compare, the declaration
// that opts it in, and the config that keeps provisioning out of the run.
const (
	crateCargoToml = "[package]\nname = \"probe\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
	crateAPI       = "pub fn do_thing(n: i32) -> i32 {\n    n\n}\n"
	crateOptIn     = "components:\n  - name: probe\n    dir: probe\n    runner: cargo-nextest\n    api_surface: {}\n"
	noProvisioning = "toolchain:\n  enabled: false\n"
)

func crateBase(componentsYML string) map[string]string {
	return map[string]string{
		"README.md":        "hello",
		component.FileName: componentsYML,
		config.FileName:    noProvisioning,
		"probe/Cargo.toml": crateCargoToml,
		"probe/src/lib.rs": crateAPI,
		referral.FileName:  "exemptions:\n  - name: probe-source\n    reason: ordinary source edits\n    paths: [\"probe/**\"]\n",
	}
}

// A Go module whose exported API the comparison loads. `go 1.26` rather than
// a patch version so the comparison runs under whatever Go the machine
// already has, without provisioning one.
const (
	sdkGoMod = "module example.com/sdk\n\ngo 1.26\n"
	sdkAPI   = "package sdk\n\n// Do runs the thing.\nfunc Do(n int) error { return nil }\n"
	sdkOptIn = "components:\n  - name: sdk\n    dir: sdk\n    runner: go-test\n    api_surface: {}\n"
)

func sdkBase() map[string]string {
	return map[string]string{
		"README.md":        "hello",
		component.FileName: sdkOptIn,
		config.FileName:    noProvisioning,
		"sdk/go.mod":       sdkGoMod,
		"sdk/api.go":       sdkAPI,
		referral.FileName:  "exemptions:\n  - name: sdk-source\n    reason: ordinary source edits\n    paths: [\"sdk/**\"]\n",
	}
}

// noToolchains provisions nothing, which is what a machine whose config turns
// provisioning off resolves to: every component runs under the ambient tools.
type noToolchains struct{ err error }

func (n noToolchains) Ensure(context.Context, string, config.Config, []component.Component) (toolchain.Envs, error) {
	return nil, n.err
}

func (noToolchains) CheckEnv(*toolchain.Env, component.Component) []string { return nil }

// A referral on a large change names a few examples rather than every path:
// a verdict a reader has to scroll past hundreds of lines to reach is one
// they stop reading.
func TestCappedTruncatesWithACount(t *testing.T) {
	var items []string
	for i := 0; i < ListCap+5; i++ {
		items = append(items, fmt.Sprintf("path/%d.go", i))
	}
	got := Capped(items)
	if len(got) != ListCap+1 {
		t.Fatalf("got %d entries, want %d plus a tail", len(got), ListCap)
	}
	if got[len(got)-1] != "…and 5 more" {
		t.Errorf("tail = %q, want a count of what is not shown", got[len(got)-1])
	}
	// The three-index slice keeps the tail out of the caller's backing
	// array, so a second call cannot see the first call's tail.
	if items[ListCap] != fmt.Sprintf("path/%d.go", ListCap) {
		t.Errorf("capped overwrote its input: %q", items[ListCap])
	}
	if short := []string{"a", "b"}; len(Capped(short)) != 2 {
		t.Errorf("a list within the cap must pass through unchanged")
	}
}

// Only Rust and TypeScript execute the tree under review to compare it. A Go
// component's comparison loads the two trees with the Go tool and compiles
// nothing the change wrote, so guardCredential must never refuse one.
func TestUntrustedBuildNamesWhatOnlyRustAndTypeScriptExecute(t *testing.T) {
	if got := untrustedBuild(component.Component{Runner: runner.GoTest}); got != "" {
		t.Errorf("untrustedBuild(Go) = %q, want empty", got)
	}
	if got := untrustedBuild(component.Component{Runner: runner.CargoNextest}); got == "" {
		t.Error("untrustedBuild(Rust) must name what it executes")
	}
	if got := untrustedBuild(component.Component{Runner: runner.Vitest}); got == "" {
		t.Error("untrustedBuild(TypeScript) must name what it executes")
	}
}

// A component with nothing skipped gets no note, so a row that compared every
// package carries no stray detail line about one it never left out.
func TestSkippedNoteNamesEveryPackageLeftOutAndNothingWhenNoneWas(t *testing.T) {
	if got := SkippedNote(nil); got != "" {
		t.Errorf("SkippedNote(nil) = %q, want empty", got)
	}
	want := "not compared, because it names no entry point: @probe/tools"
	if got := SkippedNote([]string{"@probe/tools"}); got != want {
		t.Errorf("skippedNote = %q, want %q", got, want)
	}
}

// withNote appends a note to a reason only when there is one, so a reason with
// nothing to add is not left carrying a trailing separator.
func TestWithNoteAppendsOnlyWhenThereIsOne(t *testing.T) {
	if got := withNote("reason", ""); got != "reason" {
		t.Errorf("withNote with no note = %q, want %q", got, "reason")
	}
	if got := withNote("reason", "note"); got != "reason; note" {
		t.Errorf("withNote = %q, want %q", got, "reason; note")
	}
}

// A result's Dir is this tree's own component directory, never whatever the
// document claims for it: Dir decides where a finding's path is rebased and
// rendered, and the document is exactly what a malicious build script could
// have written.
func TestReconcileSurfacesTakesDirFromTheTreeNotTheDocument(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn),
		map[string]string{"README.md": "hello again"})

	doc := SurfaceDocument{
		Base: base,
		Results: []SurfaceComparison{
			{Component: "probe", Dir: "somewhere/else/entirely"},
		},
	}
	got, err := reconcileSurfaces(dir, base, doc)
	if err != nil {
		t.Fatalf("reconcileSurfaces: %v", err)
	}
	if len(got) != 1 || got[0].Dir != "probe" {
		t.Errorf("reconcileSurfaces = %+v, want Dir %q from the tree's own component", got, "probe")
	}
}

// A document that opens fine but decodes into nothing readable, or names no
// base commit, is exactly as unreadable as a missing file: readSurfaces
// refuses both rather than handing review a document with nothing in it.
func TestReadSurfacesRejectsAMalformedDocument(t *testing.T) {
	for name, raw := range map[string]string{
		"invalid json": `{not json`,
		"no base":      `{"results":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "surfaces.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadSurfaces(path); err == nil {
				t.Errorf("ReadSurfaces(%s) was accepted, want it refused", raw)
			}
		})
	}
}

// writeSurfaces reports the underlying failure rather than losing it: a path
// under a directory that does not exist cannot be created, and the caller
// needs that reason, not a silent success.
func TestWriteSurfacesReportsAnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "surfaces.json")
	if err := WriteSurfaces(path, "deadbeef", nil); err == nil {
		t.Error("writeSurfaces under a missing directory was accepted")
	}
}

// component.Load refuses api_surface on a component that declares no language,
// so a component reaching compareSurface without one is a defensive path rather
// than one reachable through review — this exercises it directly against a
// command-invoked component, which resolves to no language at all.
func TestCompareSurfaceHasNoComparisonForALanguagelessComponent(t *testing.T) {
	c := component.Component{Name: "tools", Dir: "tools", Command: []string{"make", "test"}}
	findings, skipped, uncomputable := compareSurface(context.Background(), c, t.TempDir(), t.TempDir(), toolchain.Envs(nil), nil, io.Discard)
	if findings != nil {
		t.Errorf("findings = %+v, want none", findings)
	}
	if skipped != nil {
		t.Errorf("skipped = %v, want none — nothing was selected to compare in the first place", skipped)
	}
	if uncomputable != "no public-API comparison exists for a component that declares no language" {
		t.Errorf("uncomputable = %q, want the missing language named as the reason", uncomputable)
	}
}

// A directory that is not a git repository at all cannot be entered at any
// prefix, so the failure is reported before a worktree is ever attempted.
func TestBaseWorktreeFailsOutsideAGitRepository(t *testing.T) {
	root, _, err := baseWorktree(context.Background(), t.TempDir(), "HEAD")
	if err == nil {
		t.Fatal("baseWorktree over a non-repository directory must fail")
	}
	if root != "" {
		t.Errorf("root = %q on error, want empty — a caller must not act on it", root)
	}
}

// A base that resolves the repository but names no real commit fails at the
// worktree checkout itself, and the temp directory it made is cleaned up
// rather than left behind.
func TestBaseWorktreeFailsOnAnUnknownCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if r := executil.RunQuiet(ctx, dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "one")

	root, _, err := baseWorktree(ctx, dir, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("baseWorktree over an unknown commit must fail")
	}
	if root != "" {
		t.Errorf("root = %q on error, want empty — a caller must not act on it", root)
	}
}

// A repository where nothing opted in pays for nothing: no config is read, no
// toolchain is provisioned and no worktree is added, so a toolchain that would
// fail is never asked.
func TestCompareSurfacesDoesNothingWhereNoComponentOptedIn(t *testing.T) {
	dir, base := repo(t, map[string]string{"README.md": "hello"}, map[string]string{"README.md": "again"})
	got, err := CompareSurfaces(context.Background(), dir, base, true, noToolchains{err: fmt.Errorf("provisioned")}, io.Discard)
	if err != nil || got != nil {
		t.Errorf("CompareSurfaces = %+v, %v, want nothing at all", got, err)
	}
}

// A toolchain that could not be provisioned fails the comparison outright:
// no component could be loaded by the Go its own directory declares.
func TestCompareSurfacesFailsWhenTheToolchainCannotBeProvisioned(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn), map[string]string{"README.md": "again"})
	if _, err := CompareSurfaces(context.Background(), dir, base, false, noToolchains{err: fmt.Errorf("no rustup")}, io.Discard); err == nil {
		t.Error("a toolchain that failed to provision was not reported")
	}
}

// Guarded, a Rust comparison never runs — it would execute the head tree's own
// build.rs beside a credential — and the component is uncomputable, naming why,
// rather than absent.
func TestCompareSurfacesRefusesAnUntrustedBuildWhenGuarded(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn), map[string]string{"probe/src/lib.rs": "pub fn other() {}\n"})
	got, err := CompareSurfaces(context.Background(), dir, base, true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("CompareSurfaces: %v", err)
	}
	if len(got) != 1 || got[0].Component != "probe" || got[0].Dir != "probe" {
		t.Fatalf("CompareSurfaces = %+v, want one result for probe", got)
	}
	if !strings.Contains(got[0].Uncomputable, "must not happen in the same process") {
		t.Errorf("uncomputable = %q, want the guard named as the reason", got[0].Uncomputable)
	}
}

// A base the worktree cannot be checked out at leaves every opted-in component
// uncomputable under its own name, since one missing from the result would be
// indistinguishable from one compared and found clean.
func TestCompareSurfacesNamesEveryComponentWhenTheBaseCannotBeCheckedOut(t *testing.T) {
	dir, _ := repo(t, crateBase(crateOptIn), map[string]string{"README.md": "again"})
	got, err := CompareSurfaces(context.Background(), dir, "0000000000000000000000000000000000000000", true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("CompareSurfaces: %v", err)
	}
	if len(got) != 1 || got[0].Component != "probe" || got[0].Uncomputable == "" {
		t.Errorf("CompareSurfaces = %+v, want probe uncomputable", got)
	}
}

// A Go comparison loads both trees and compiles nothing the change wrote, so
// it runs even when guarded, and a removed function is a finding located at
// the merge-base's declaration.
func TestCompareSurfacesFindsARemovedGoFunction(t *testing.T) {
	dir, base := repo(t, sdkBase(), map[string]string{"sdk/api.go": "package sdk\n"})
	got, err := CompareSurfaces(context.Background(), dir, base, true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("CompareSurfaces: %v", err)
	}
	if len(got) != 1 || got[0].Uncomputable != "" || len(got[0].Findings) == 0 {
		t.Fatalf("CompareSurfaces = %+v, want a finding for the removed function", got)
	}
	if got[0].Findings[0].Gate != GateAPISurface {
		t.Errorf("finding gate = %q, want %q", got[0].Findings[0].Gate, GateAPISurface)
	}
}

// A module path that moved is named as the reason the surface could not be
// compared, rather than reported as a break of every symbol.
func TestCompareSurfacesNamesAModulePathThatMoved(t *testing.T) {
	dir, base := repo(t, sdkBase(), map[string]string{"sdk/go.mod": "module example.com/moved\n\ngo 1.26\n"})
	got, err := CompareSurfaces(context.Background(), dir, base, true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("CompareSurfaces: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Uncomputable, "module path changed") {
		t.Errorf("CompareSurfaces = %+v, want the moved module path named", got)
	}
}

// With no document named, Surfaces is the comparison itself.
func TestSurfacesComparesWhenNoDocumentIsNamed(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn), map[string]string{"README.md": "again"})
	got, err := Surfaces(context.Background(), dir, base, "", true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Uncomputable, "must not happen in the same process") {
		t.Errorf("Surfaces = %+v, want the guarded comparison's own answer", got)
	}
}

// A document that cannot be read is no evidence any surface is clean, so every
// opted-in component is uncomputable rather than the run failing before any
// disqualification could be added.
func TestSurfacesMakesEveryComponentUncomputableForAnUnreadableDocument(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn), map[string]string{"README.md": "again"})
	got, err := Surfaces(context.Background(), dir, base, filepath.Join(t.TempDir(), "missing.json"), true, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Uncomputable, "the comparison document could not be read") {
		t.Errorf("Surfaces = %+v, want probe uncomputable for the unreadable document", got)
	}
}

// A document naming some other base is refused for every component, and one
// carrying no result for a component this tree says opted in leaves that
// component uncomputable rather than silently clean.
func TestSurfacesReconcilesADocumentAgainstThisRun(t *testing.T) {
	dir, base := repo(t, crateBase(crateOptIn), map[string]string{"README.md": "again"})
	ctx := context.Background()

	forged := filepath.Join(t.TempDir(), "forged.json")
	if err := WriteSurfaces(forged, "1111111111111111111111111111111111111111", nil); err != nil {
		t.Fatal(err)
	}
	got, err := Surfaces(ctx, dir, base, forged, false, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Uncomputable, "a different commit") {
		t.Errorf("Surfaces = %+v, want the forged base refused", got)
	}

	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := WriteSurfaces(empty, base, nil); err != nil {
		t.Fatal(err)
	}
	got, err = Surfaces(ctx, dir, base, empty, false, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Uncomputable, "carries no result for this component") {
		t.Errorf("Surfaces = %+v, want the missing result named", got)
	}

	clean := filepath.Join(t.TempDir(), "clean.json")
	if err := WriteSurfaces(clean, base, []SurfaceComparison{{Component: "probe"}}); err != nil {
		t.Fatal(err)
	}
	got, err = Surfaces(ctx, dir, base, clean, false, noToolchains{}, io.Discard)
	if err != nil {
		t.Fatalf("Surfaces: %v", err)
	}
	if len(got) != 1 || got[0].Uncomputable != "" || got[0].Dir != "probe" {
		t.Errorf("Surfaces = %+v, want probe's own clean result", got)
	}
}

// A components file that cannot be read fails the run, however the surfaces
// were asked for: nothing can say which components opted in.
func TestSurfacesFailsOnAMalformedComponentsFile(t *testing.T) {
	dir, base := repo(t, map[string]string{component.FileName: "components: [unterminated\n"}, map[string]string{"README.md": "x"})
	if _, err := Surfaces(context.Background(), dir, base, "", false, noToolchains{}, io.Discard); err == nil {
		t.Error("a malformed components file was not reported by the comparison")
	}
	if _, err := Surfaces(context.Background(), dir, base, filepath.Join(t.TempDir(), "missing.json"), false, noToolchains{}, io.Discard); err == nil {
		t.Error("a malformed components file was not reported for an unreadable document")
	}
}
