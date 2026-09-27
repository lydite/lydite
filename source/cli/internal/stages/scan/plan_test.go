package scanstages

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// testEnvironment composes a component's environment the way the CLI's own
// Environment does: its declaration sorted by key, a declared PATH split into
// directories appended after the toolchain's, and the toolchain's variables
// last so they win.
type testEnvironment struct{}

func (testEnvironment) Compose(tc *toolchain.Env, c component.Component) []string {
	var dirs []string
	if tc != nil {
		dirs = tc.PathDirs
	}
	declared, vars := testEnvironment{}.Declared(c)
	if tc == nil {
		return toolchain.Compose(dirs, declared, vars)
	}
	return toolchain.Compose(dirs, declared, vars, tc.Vars)
}

func (testEnvironment) Declared(c component.Component) (dirs, vars []string) {
	entries := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		entries = append(entries, k+"="+v)
	}
	sort.Strings(entries)
	path := ""
	for _, kv := range entries {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			continue
		}
		vars = append(vars, kv)
	}
	if path == "" {
		return nil, vars
	}
	return filepath.SplitList(path), vars
}

func plan(t *testing.T, root string, cfg config.Config, envs toolchain.Envs, components ...component.Component) []Planned {
	t.Helper()
	out, err := PlanComponents(context.Background(), PlanComponentsIn{
		Dir: root, File: component.File{Components: components}, Config: cfg,
		Envs: envs, Environment: testEnvironment{},
	})
	if err != nil {
		t.Fatalf("PlanComponents: %v", err)
	}
	if len(out.Plan) != len(components) {
		t.Fatalf("plan has %d entries for %d components, want one each", len(out.Plan), len(components))
	}
	return out.Plan
}

// dispositions is each entry's component and disposition, in plan order.
func dispositions(plan []Planned) []string {
	out := make([]string, 0, len(plan))
	for _, p := range plan {
		out = append(out, p.Component.Name+":"+string(p.Disposition))
	}
	return out
}

// Every component is planned, in declaration order, with the one disposition
// that says what the scan does with it — including the ones it does nothing
// with, which the report still has to name.
func TestPlanComponentsGivesEveryComponentItsDispositionInDeclarationOrder(t *testing.T) {
	cfg := config.Default()
	cfg.Rust.Enabled = false
	got := plan(t, "/repo", cfg, nil,
		component.Component{Name: "cli", Dir: "cli", Runner: runner.GoTest},
		component.Component{Name: "legacy", Dir: "legacy", Command: []string{"make", "check"}},
		component.Component{Name: "tools", Dir: "tools", DeclaredLang: runner.Python},
		component.Component{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
		component.Component{Name: "api", Dir: "api", DeclaredLang: runner.Rust},
		component.Component{Name: "cli-integration", Dir: "cli", Runner: runner.GoTest},
	)
	want := []string{
		"cli:scan", "legacy:unscanned", "tools:unscanned", "scripts:disabled", "api:disabled", "cli-integration:duplicate",
	}
	if !slices.Equal(dispositions(got), want) {
		t.Fatalf("plan = %v, want %v", dispositions(got), want)
	}
	for i, lang := range []runner.Lang{runner.Go, "", runner.Python, runner.Shell, runner.Rust, runner.Go} {
		if got[i].Lang != lang {
			t.Errorf("%s: lang = %q, want %q — the language the component is scanned as", got[i].Component.Name, got[i].Lang, lang)
		}
	}
	for _, p := range got {
		if p.Disposition == Scan {
			continue
		}
		if p.Dir != "" || p.Env.Check != nil || p.Env.Install != nil || p.ToolchainKey != "" {
			t.Errorf("%s (%s) carries %+v, want no directory, environment or key for an entry nothing checks", p.Component.Name, p.Disposition, p)
		}
	}
}

// A language with a runner and no scanner must not leave through the opt-out
// branch: scanlang.Enabled answers false for every language it has no key
// for, which would skip it as silently as a switch the repository never
// touched. A raw command states no language at all, which is its own case.
func TestALanguageWithNoScannerIsPlannedUnscannedAndNotDisabled(t *testing.T) {
	got := plan(t, "/repo", config.Default(), nil,
		component.Component{Name: "tools", Dir: ".", DeclaredLang: runner.Python},
		component.Component{Name: "legacy", Dir: ".", Command: []string{"make", "check"}},
	)
	if got[0].Disposition != Unscanned || got[0].Lang != runner.Python {
		t.Errorf("tools = %s as %q, want unscanned naming python", got[0].Disposition, got[0].Lang)
	}
	if got[1].Disposition != Unscanned || got[1].Lang != "" {
		t.Errorf("legacy = %s as %q, want unscanned naming no language", got[1].Disposition, got[1].Lang)
	}
}

// Shell is off until a repository switches it on, and stating it off is the
// same answer: either way its checks do not run, and the plan says so rather
// than dropping the component.
func TestAShellComponentWithShellOffIsPlannedDisabled(t *testing.T) {
	scripts := component.Component{Name: "scripts", Dir: ".", DeclaredLang: runner.Shell}
	for name, cfgYAML := range map[string]string{
		"unset": "semgrep:\n  enabled: false\n",
		"false": "shell:\n  enabled: false\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, config.FileName, cfgYAML)
			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			got := plan(t, dir, cfg, nil, scripts)
			if got[0].Disposition != Disabled || got[0].Lang != runner.Shell {
				t.Errorf("scripts = %s as %q, want disabled as shell", got[0].Disposition, got[0].Lang)
			}
		})
	}
	on := config.Default()
	on.Shell.Enabled = true
	if got := plan(t, "/repo", on, nil, scripts); got[0].Disposition != Scan {
		t.Errorf("scripts = %s with shell switched on, want scan", got[0].Disposition)
	}
}

// A language switched off in .lydite/config.yml runs nothing, and a language
// switched on alongside it is unaffected.
func TestADisabledLanguageIsPlannedDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Go.Enabled = false
	got := plan(t, "/repo", cfg, nil,
		component.Component{Name: "cli", Dir: "cli", Runner: runner.GoTest},
		component.Component{Name: "web", Dir: "web", DeclaredLang: runner.TypeScript},
	)
	if !slices.Equal(dispositions(got), []string{"cli:disabled", "web:scan"}) {
		t.Fatalf("plan = %v, want Go disabled and TypeScript scanned", dispositions(got))
	}
}

// component.validate enforces unique names and not unique directories, so two
// components over one root are legitimate — `lydite test` runs both suites.
// Their scanners read the identical tree, so running both spends time to
// report every finding twice under two labels. The first in declaration order
// carries the checks, and the second names it.
func TestTwoComponentsOverOneDirectoryAreScannedOnce(t *testing.T) {
	got := plan(t, "/repo", config.Default(), nil,
		component.Component{Name: "api", Dir: ".", Runner: runner.GoTest},
		component.Component{Name: "api-integration", Dir: ".", Runner: runner.GoTest},
	)
	if !slices.Equal(dispositions(got), []string{"api:scan", "api-integration:duplicate"}) {
		t.Fatalf("plan = %v, want the shared directory scanned once", dispositions(got))
	}
	if got[1].DuplicateOf != "api" {
		t.Errorf("duplicate of %q, want the component whose checks cover it", got[1].DuplicateOf)
	}
}

// Two components over one directory declaring different environments are two
// builds, not one. Dropping either scans that tree with an environment it
// never asked for, and the row would carry the other component's name.
func TestTwoComponentsOverOneDirectoryWithDifferentEnvironmentsBothRun(t *testing.T) {
	got := plan(t, "/repo", config.Default(), nil,
		component.Component{Name: "api", Dir: ".", Runner: runner.GoTest},
		component.Component{Name: "api-cgo", Dir: ".", Runner: runner.GoTest, Env: map[string]string{"CGO_ENABLED": "1"}},
	)
	if !slices.Equal(dispositions(got), []string{"api:scan", "api-cgo:scan"}) {
		t.Fatalf("plan = %v, want each environment scanned under its own component", dispositions(got))
	}
}

// The key is the environment the check is actually handed, toolchain
// included: two components over one directory declaring nothing, resolved to
// different toolchains, are two builds just as two declarations are.
func TestTwoComponentsOverOneDirectoryUnderDifferentToolchainsBothRun(t *testing.T) {
	envs := toolchain.Envs{
		"api":    {Vars: []string{"GOTOOLCHAIN=local"}, Resolved: "go1.26.6"},
		"api-v2": {Vars: []string{"GOTOOLCHAIN=local"}, PathDirs: []string{"/cache/go1.27.0/bin"}, Resolved: "go1.27.0"},
	}
	got := plan(t, "/repo", config.Default(), envs,
		component.Component{Name: "api", Dir: ".", Runner: runner.GoTest},
		component.Component{Name: "api-v2", Dir: ".", Runner: runner.GoTest},
	)
	if !slices.Equal(dispositions(got), []string{"api:scan", "api-v2:scan"}) {
		t.Fatalf("plan = %v, want each toolchain scanned under its own component", dispositions(got))
	}
}

// The key is the language as well as the directory and the environment: a Go
// module and the shell scripts beside it are read by different scanners, so
// neither covers the other.
func TestTwoLanguagesOverOneDirectoryBothRun(t *testing.T) {
	cfg := config.Default()
	cfg.Shell.Enabled = true
	got := plan(t, "/repo", cfg, nil,
		component.Component{Name: "cli", Dir: ".", Runner: runner.GoTest},
		component.Component{Name: "scripts", Dir: ".", DeclaredLang: runner.Shell},
	)
	if !slices.Equal(dispositions(got), []string{"cli:scan", "scripts:scan"}) {
		t.Fatalf("plan = %v, want both languages scanned", dispositions(got))
	}
}

// A scan entry carries everything its checks run with, so nothing later
// resolves it again: the directory joined onto the scan root, the component's
// declared environment composed onto its toolchain for the checks, the
// toolchain alone for what installs lydite's own tools, and the toolchain's
// key.
//
// A repository may say how its own code builds; it may not say where lydite's
// scanners come from. `go install`, `cargo install` and `npm ci` read GOPROXY,
// GOSUMDB, CARGO_REGISTRIES_* and npm_config_registry, so a declared
// environment reaching them chooses which binary lydite fetches and then runs.
func TestAScanEntryCarriesItsDirectoryEnvironmentAndToolchainKey(t *testing.T) {
	root := t.TempDir()
	c := component.Component{Name: "svc", Dir: "services/svc", Runner: runner.GoTest, Env: map[string]string{
		"GOPROXY":      "http://127.0.0.1:1",
		"SQLX_OFFLINE": "true",
	}}
	tc := &toolchain.Env{Vars: []string{"GOTOOLCHAIN=local"}, Resolved: "go1.26.6"}
	got := plan(t, root, config.Default(), toolchain.Envs{"svc": tc}, c)[0]

	if got.Disposition != Scan {
		t.Fatalf("disposition = %s, want scan", got.Disposition)
	}
	if want := filepath.Join(root, "services", "svc"); got.Dir != want {
		t.Errorf("dir = %q, want %q", got.Dir, want)
	}
	if want := (testEnvironment{}).Compose(tc, c); !slices.Equal(got.Env.Check, want) {
		t.Errorf("check env = %q, want the Environment's composition %q", got.Env.Check, want)
	}
	if !slices.Contains(got.Env.Check, "SQLX_OFFLINE=true") {
		t.Errorf("check env = %q, want the component's declared variable", got.Env.Check)
	}
	for _, kv := range got.Env.Install {
		if strings.HasPrefix(kv, "GOPROXY=") || strings.HasPrefix(kv, "SQLX_OFFLINE=") {
			t.Fatalf("install env carries %q from the scanned repository", kv)
		}
	}
	if !slices.Equal(got.Env.Install, tc.Environ()) {
		t.Errorf("install env = %q, want lydite's own resolved toolchain %q", got.Env.Install, tc.Environ())
	}
	if got.ToolchainKey != tc.Key() {
		t.Errorf("toolchain key = %q, want %q", got.ToolchainKey, tc.Key())
	}
}

// A component no toolchain was resolved for runs under the ambient one, and
// its key says so rather than being empty.
func TestAScanEntryWithNoResolvedToolchainIsKeyedAmbient(t *testing.T) {
	got := plan(t, "/repo", config.Default(), nil, component.Component{Name: "cli", Dir: ".", Runner: runner.GoTest})[0]
	if want := (*toolchain.Env)(nil).Key(); got.ToolchainKey != want {
		t.Errorf("toolchain key = %q, want %q", got.ToolchainKey, want)
	}
	if got.Env.Install != nil {
		t.Errorf("install env = %q, want nothing added to the ambient environment", got.Env.Install)
	}
}
