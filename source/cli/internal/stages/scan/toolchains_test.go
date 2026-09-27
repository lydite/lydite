package scanstages

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// fakeToolchains is a Toolchains whose Ensure fails the test if called
// without a function supplied for it — a stage calling it when the test case
// expects no provisioning is itself the bug under test.
type fakeToolchains struct {
	t      *testing.T
	ensure func(ctx context.Context, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error)
}

func (f fakeToolchains) Ensure(ctx context.Context, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
	if f.ensure == nil {
		f.t.Fatal("Toolchains.Ensure: unexpected call")
	}
	return f.ensure(ctx, dir, cfg, units)
}

func TestProvisionToolchainsCallsEnsureWithTheScanUnits(t *testing.T) {
	file := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: runner.GoTest},
		{Name: "legacy", Dir: "legacy", Command: []string{"make", "check"}},
	}}
	cfg := config.Default()
	dir := t.TempDir()

	var gotDir string
	var gotUnits []toolchain.Unit
	want := toolchain.Envs{}
	out, err := ProvisionToolchains(context.Background(), ProvisionToolchainsIn{
		Dir: dir, File: file, Config: cfg,
		Toolchains: fakeToolchains{t: t, ensure: func(_ context.Context, dir string, _ config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
			gotDir, gotUnits = dir, units
			return want, nil
		}},
	})
	if err != nil {
		t.Fatalf("ProvisionToolchains: %v", err)
	}
	if gotDir != dir {
		t.Errorf("Ensure dir = %q, want %q", gotDir, dir)
	}
	if len(gotUnits) != 1 || gotUnits[0].Name != "cli" || gotUnits[0].Lang != runner.Go {
		t.Fatalf("Ensure units = %+v, want just the Go component — the raw command implies no language", gotUnits)
	}
	if out.Envs == nil {
		t.Errorf("ProvisionToolchains envs = nil, want Ensure's own return value")
	}
}

func TestProvisionToolchainsPropagatesTheToolchainsError(t *testing.T) {
	file := component.File{Components: []component.Component{{Name: "cli", Dir: ".", Runner: runner.GoTest}}}
	want := context.DeadlineExceeded
	_, err := ProvisionToolchains(context.Background(), ProvisionToolchainsIn{
		Dir: t.TempDir(), File: file, Config: config.Default(),
		Toolchains: fakeToolchains{t: t, ensure: func(context.Context, string, config.Config, []toolchain.Unit) (toolchain.Envs, error) {
			return nil, want
		}},
	})
	if err != want {
		t.Errorf("ProvisionToolchains error = %v, want Ensure's own %v", err, want)
	}
}

// A language switched off in .lydite/config.yml produces no unit: provisioning
// one would download a compiler nothing invokes.
func TestScanUnitsSkipsADisabledLanguage(t *testing.T) {
	file := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: runner.GoTest},
		{Name: "legacy", Dir: "."},
	}}
	cfg := config.Default()
	cfg.Go.Enabled = false

	if units := scanUnits(file, cfg); len(units) != 0 {
		t.Fatalf("units = %+v, want none when the only language is disabled", units)
	}
	cfg.Go.Enabled = true
	units := scanUnits(file, cfg)
	if len(units) != 1 || units[0].Name != "cli" || units[0].Lang != runner.Go {
		t.Fatalf("units = %+v, want just the Go component", units)
	}
}

// A declared lang: is the language a component is scanned as. A command
// component stating `lang: go` is provisioned Go for its checks; a
// `lang: shell` component is a unit only where shell is switched on.
func TestScanUnitsReadsADeclaredLang(t *testing.T) {
	file := component.File{Components: []component.Component{
		{Name: "tool", Dir: "tool", Command: []string{"make", "test"}, DeclaredLang: runner.Go},
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
	}}

	units := scanUnits(file, config.Default())
	if len(units) != 1 || units[0].Name != "tool" || units[0].Lang != runner.Go {
		t.Fatalf("scan units = %+v, want just tool, as Go, with shell off by default", units)
	}
	enabled := config.Default()
	enabled.Shell.Enabled = true
	units = scanUnits(file, enabled)
	if len(units) != 2 || units[1].Name != "scripts" || units[1].Lang != runner.Shell {
		t.Fatalf("scan units = %+v, want tool and scripts, as shell, with shell switched on", units)
	}
	if !anyLanguageDeclared(file) {
		t.Error("anyLanguageDeclared = false, want a declared lang: to count")
	}
}

// gitInit stages every file under dir into git's index without committing —
// enough for git ls-files to see them, which is all Unscanned reads.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-b", "main", "."}, {"add", "-A"}} {
		if r := executil.RunQuiet(context.Background(), dir, "git", args...); !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Stderr)
		}
	}
}

func warnUnscanned(t *testing.T, dir string, file component.File, cfg config.Config) ([]byte, WarnUnscannedOut) {
	t.Helper()
	var w bytes.Buffer
	out, err := WarnUnscanned(context.Background(), WarnUnscannedIn{Dir: dir, File: file, Config: cfg, Diagnostics: &w})
	if err != nil {
		t.Fatalf("WarnUnscanned: %v", err)
	}
	return w.Bytes(), out
}

// The hole the orphan gate cannot close. A component rooted at `.` covers
// every path in the repository, so a Go component at the root leaves a
// TypeScript directory beside it orphaning nothing while no TypeScript check
// ever runs.
func TestWarnUnscannedNamesALanguageNoComponentDeclares(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	warning, out := warnUnscanned(t, dir, file, config.Default())

	if len(out.Gaps) != 1 || out.Gaps[0].Lang != runner.TypeScript {
		t.Fatalf("gaps = %+v, want TypeScript alone — Go is declared and covers main.go", out.Gaps)
	}
	if !strings.Contains(string(warning), component.FileName) {
		t.Errorf("warning = %q, want it to name the file that fixes it", warning)
	}
}

// A language switched off is an answer, not an oversight: the repository said
// it wants no check over that code, and warning about it would be lydite
// arguing with a decision it was told about.
func TestWarnUnscannedIsSilentAboutADisabledLanguage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	write(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)

	cfg := config.Default()
	cfg.TypeScript.Enabled = false

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, cfg); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want nothing when the language is switched off", out.Gaps)
	}
}

// Outside a git repository there is no file list and therefore no question to
// answer, which is the shape the orphan gate already has for the same case.
func TestWarnUnscannedSaysNothingOutsideAGitRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	write(t, dir, "web/app.ts", "export const x = 1;\n")

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	warning, out := warnUnscanned(t, dir, file, config.Default())
	if len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want nothing outside a repository", out.Gaps)
	}
	if len(warning) != 0 {
		t.Errorf("warning = %q, want silence", warning)
	}
}

// The wardnet shape. Two Go modules, one declared: the language is covered,
// so a check keyed on languages alone says nothing while the second module is
// scanned by nobody.
func TestWarnUnscannedNamesAModuleNoComponentCovers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: wctl\n    dir: wctl\n    runner: go-test\n")
	write(t, dir, "wctl/go.mod", "module wctl\n\ngo 1.26\n")
	write(t, dir, "wctl/main.go", "package main\n")
	write(t, dir, "sdk/wardnet-go/go.mod", "module sdk\n\ngo 1.26\n")
	write(t, dir, "sdk/wardnet-go/client.go", "package sdk\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	warning, out := warnUnscanned(t, dir, file, config.Default())

	if len(out.Gaps) != 1 || out.Gaps[0].Lang != runner.Go {
		t.Fatalf("gaps = %+v, want the undeclared Go module reported", out.Gaps)
	}
	if !slices.Equal(out.Gaps[0].Files, []string{"sdk/wardnet-go/client.go"}) {
		t.Fatalf("files = %v, want only the module no component covers", out.Gaps[0].Files)
	}
	if !strings.Contains(string(warning), "sdk/wardnet-go/client.go") {
		t.Errorf("warning = %q, want it to name an example file", warning)
	}
}

// An exclude is the repository's reviewable statement that a path is claimed
// by no component, which is the same statement this warning asks for.
func TestWarnUnscannedIsSilencedByAnExclude(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n"+
			"excludes: [\"vendor-fixtures/**\"]\n")
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "vendor-fixtures/src/lib.rs", "pub fn x() {}\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none when the path is excluded", out.Gaps)
	}
}

// Containment is not enough for Go. A nested go.mod starts a separate module
// the enclosing module's package graph excludes, so `./...` at the root
// never compiles it and neither gosec nor govulncheck sees it — while a
// component rooted at `.` contains every path in the repository.
func TestWarnUnscannedNamesANestedModuleUnderARootComponent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	write(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "sdk/go.mod", "module sdk\n\ngo 1.26\n")
	write(t, dir, "sdk/client.go", "package sdk\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, out := warnUnscanned(t, dir, file, config.Default())
	if len(out.Gaps) != 1 || !slices.Equal(out.Gaps[0].Files, []string{"sdk/client.go"}) {
		t.Fatalf("gaps = %+v, want only the nested module's file — main.go is in the component's own module", out.Gaps)
	}
}

// The same shape with one module: everything is in the component's module,
// so there is nothing to say.
func TestWarnUnscannedIsSilentUnderOneModule(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	write(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "internal/svc/svc.go", "package svc\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want silence in a single-module repository", out.Gaps)
	}
}

// A go.mod under testdata/ is a fixture the go command ignores when resolving
// packages, so the enclosing module does not scan it and neither does
// anything else.
func TestWarnUnscannedTreatsATestdataModuleAsNoBoundary(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: root\n    dir: .\n    runner: go-test\n")
	write(t, dir, "go.mod", "module root\n\ngo 1.26\n")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "testdata/broken/go.mod", "module broken\n\ngo 1.26\n")
	write(t, dir, "testdata/broken/x.go", "package broken\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want silence: a testdata module is a fixture", out.Gaps)
	}
}

// A component declaring a raw command has a component declared for it, so
// warning as well would tell its author to declare what they have declared.
func TestWarnUnscannedIsSilentAboutARawCommandComponent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName,
		"components:\n  - name: legacy\n    dir: legacy\n    command: [\"make\", \"check\"]\n")
	write(t, dir, "legacy/main.go", "package main\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none: scan already reports scan(legacy) unmeasured", out.Gaps)
	}
}

// A repository whose every component declares a raw command and no lang has
// no source in a language lydite knows by construction, so git tracking no
// recognised source at all — orphan.ErrNoFiles — is that repository's
// ordinary state, not something the "could not check" warning should fire on.
func TestWarnUnscannedIsSilentWhenNoComponentDeclaresALanguageAndGitTracksNoSource(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName,
		"components:\n  - name: legacy\n    dir: legacy\n    command: [\"make\", \"check\"]\n")
	write(t, dir, "legacy/Makefile", "check:\n\techo ok\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	warning, out := warnUnscanned(t, dir, file, config.Default())
	if len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none: git tracks no recognised source", out.Gaps)
	}
	if len(warning) != 0 {
		t.Errorf("warning = %q, want silence: no component declares a language for ErrNoFiles to be worth reporting", warning)
	}
}

// A component that does declare a language is a claim WarnUnscanned should
// be able to check — so when git tracks no source to check it against, that
// is worth saying, unlike the raw-command repository above.
func TestWarnUnscannedWarnsWhenADeclaredLanguageCannotBeChecked(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: legacy\n    dir: legacy\n    lang: go\n")
	write(t, dir, "legacy/Makefile", "check:\n\techo ok\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	warning, out := warnUnscanned(t, dir, file, config.Default())
	if len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none: WarnUnscanned reports nothing to a caller on this path", out.Gaps)
	}
	if !strings.Contains(string(warning), "could not check") {
		t.Errorf("warning = %q, want the could-not-check warning: a declared language went unchecked", warning)
	}
}

// The .js family is the extension of build output, configuration and tooling
// glue in every ecosystem, so a stray one is not an unscanned codebase.
func TestWarnUnscannedIgnoresAStrayJavaScriptFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: cli\n    dir: .\n    runner: go-test\n")
	write(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "docs/theme.js", "module.exports = {};\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want silence for a stray .js", out.Gaps)
	}

	// A .ts beside it is a different claim, and still reported.
	write(t, dir, "web/app.ts", "export const x = 1;\n")
	gitInit(t, dir)
	file, err = component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, out := warnUnscanned(t, dir, file, config.Default())
	if len(out.Gaps) != 1 || !slices.Equal(out.Gaps[0].Files, []string{"web/app.ts"}) {
		t.Fatalf("gaps = %+v, want the .ts alone", out.Gaps)
	}
}

// A Go component declared at a subdirectory of a single-module repository is
// scanned exactly as it should be, so asking whether its directory held a
// go.mod would report its own files as scanned by nobody.
func TestWarnUnscannedComponentInsideASingleModuleIsNotAGap(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: api\n    dir: services/api\n    runner: go-test\n")
	write(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	write(t, dir, "services/api/main.go", "package main\n\nfunc main() {}\n")
	gitInit(t, dir)

	file, err := component.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, out := warnUnscanned(t, dir, file, config.Default()); len(out.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want none: the component's files are in the module gosec runs over", out.Gaps)
	}
}
