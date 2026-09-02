package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("Load with no file = %+v, want Default() = %+v", got, Default())
	}
}

func TestLoadPartialOverrideKeepsOtherDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go:\n  enabled: false\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Default()
	want.Go.Enabled = false
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	// Rust/TypeScript/Semgrep, untouched by the file, must keep Default()'s values.
	if !got.Rust.Enabled || !got.TypeScript.Enabled || !got.Semgrep.Enabled {
		t.Fatalf("an untouched section lost its default enabled=true: %+v", got)
	}
}

func TestLoadDisableLanguage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "rust:\n  enabled: false\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Rust.Enabled {
		t.Fatal("rust.enabled: false in the file did not disable Rust")
	}
	if !got.Go.Enabled || !got.TypeScript.Enabled {
		t.Fatalf("disabling rust incorrectly disabled another language: %+v", got)
	}
}

func TestLoadSemgrepConfigOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "semgrep:\n  config: p/security-audit\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Semgrep.Config != "p/security-audit" {
		t.Fatalf("Semgrep.Config = %q, want %q", got.Semgrep.Config, "p/security-audit")
	}
	if !got.Semgrep.Enabled {
		t.Fatal("overriding config incorrectly disabled semgrep")
	}
}

// Zero-config users get a small noise-absorbing tolerance on the coverage
// gates, so a sub-rounding-error dip (86.1% vs baseline 86.1%) doesn't fail
// unrelated PRs. The aggregate and patch knobs default independently.
func TestDefaultCoverageTolerance(t *testing.T) {
	if got := Default().Coverage.Tolerance; got != 0.1 {
		t.Fatalf("Coverage.Tolerance default = %v, want 0.1", got)
	}
	if got := Default().Coverage.Patch.Tolerance; got != 0.1 {
		t.Fatalf("Coverage.Patch.Tolerance default = %v, want 0.1", got)
	}
}

// Tolerances that would invert the gate (negative) or silently disable it
// (NaN, ±Inf are all valid YAML floats) must be rejected at load time with
// an error naming the key, not flow into the comparison.
func TestLoadRejectsInvalidTolerance(t *testing.T) {
	cases := map[string]string{
		"negative aggregate": "coverage:\n  tolerance: -0.1\n",
		"nan aggregate":      "coverage:\n  tolerance: .nan\n",
		"inf aggregate":      "coverage:\n  tolerance: .inf\n",
		"negative patch":     "coverage:\n  patch:\n    tolerance: -1\n",
		"nan patch":          "coverage:\n  patch:\n    tolerance: .nan\n",
	}
	for name, yml := range cases {
		dir := t.TempDir()
		write(t, dir, yml)
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: Load accepted an invalid tolerance (%q), want an error", name, yml)
		} else if !strings.Contains(err.Error(), "tolerance") {
			t.Errorf("%s: error should name the offending key, got: %v", name, err)
		}
	}
}

// An explicit tolerance: 0 tightens the gate to "fail any dip the report can
// display" — the merge must honor an explicitly-present zero, not treat it
// as "keep the default".
func TestLoadCoverageToleranceExplicitZero(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "coverage:\n  tolerance: 0\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Coverage.Tolerance != 0 {
		t.Fatalf("Coverage.Tolerance = %v, want 0 after explicit override", got.Coverage.Tolerance)
	}
	if !got.Coverage.Patch.Go.Enabled {
		t.Fatalf("setting coverage.tolerance incorrectly disabled patch coverage: %+v", got.Coverage)
	}
}

func TestLoadPatchCoverageDefaultsEnabled(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Coverage.Patch.Go.Enabled || !got.Coverage.Patch.Rust.Enabled || !got.Coverage.Patch.TypeScript.Enabled {
		t.Fatalf("patch coverage must default to enabled for every language: %+v", got.Coverage)
	}
}

func TestLoadPatchCoverageOptOut(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "coverage:\n  patch:\n    go:\n      enabled: false\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Coverage.Patch.Go.Enabled {
		t.Fatal("coverage.patch.go.enabled: false in the file did not disable Go patch coverage")
	}
	if !got.Coverage.Patch.Rust.Enabled || !got.Coverage.Patch.TypeScript.Enabled {
		t.Fatalf("disabling go patch coverage incorrectly disabled another language: %+v", got.Coverage)
	}
}

func TestLoadTypeScriptInstallOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "typescript:\n  install: \"corepack enable && yarn install --immutable\"\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TypeScript.Install != "corepack enable && yarn install --immutable" {
		t.Fatalf("TypeScript.Install = %q, want the configured override", got.TypeScript.Install)
	}
	// TypeScriptLanguage's embedded Language fields must still merge onto
	// Default() normally alongside the new Install field.
	if !got.TypeScript.Enabled {
		t.Fatal("setting typescript.install incorrectly disabled TypeScript")
	}
}

func TestLoadTypeScriptInstallDefaultsEmpty(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TypeScript.Install != "" {
		t.Fatalf("TypeScript.Install = %q, want empty (auto-detect) by default", got.TypeScript.Install)
	}
}

// Every key lydite has stopped reading is rejected by name, never ignored.
//
// Ignoring is what makes a repository measure something other than what its
// author wrote while every run still reports a pass, and `coverage.source:
// report` in particular said "never run the tests" — accepted silently, lydite
// would run every suite in a pipeline built on the promise that it would not.
func TestLoadRejectsRemovedCoverageKeys(t *testing.T) {
	for _, tc := range []struct {
		key  string
		yaml string
	}{
		{"coverage.source", "coverage:\n  source: report\n"},
		{"coverage.source", "coverage:\n  source: run\n"},
		{"coverage.go.report", "coverage:\n  go:\n    report: coverage.out\n"},
		{"coverage.rust.report", "coverage:\n  rust:\n    report: coverage/llvm-cov.json\n"},
		{"coverage.rust.lcov", "coverage:\n  rust:\n    lcov: coverage/lcov.info\n"},
	} {
		t.Run(tc.key+" "+tc.yaml, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, tc.yaml)
			_, err := Load(dir)
			if err == nil {
				t.Fatalf("Load accepted %s, which lydite no longer reads", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name %s", err, tc.key)
			}
		})
	}
}

// A repository that never carried one of those keys is untouched: the
// rejection must fire on the key being written, not on the section existing.
func TestLoadAcceptsACoverageSectionWithoutRemovedKeys(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "coverage:\n  tolerance: 0.5\n  floor: 40\n  patch:\n    tolerance: 0.2\n")
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Coverage.Tolerance != 0.5 || got.Coverage.Floor != 40 || got.Coverage.Patch.Tolerance != 0.2 {
		t.Errorf("coverage = %+v, want the file's values", got.Coverage)
	}
}

// The toolchain section carries an on/off switch and overrides, and
// deliberately no default versions: the repo's own manifests (go.mod,
// rust-toolchain.toml, engines.node/.nvmrc) are the source of truth, and a
// version defaulted here would be a second one that could drift.
func TestLoadToolchainDefaults(t *testing.T) {
	d := Default()
	if !d.Toolchain.Enabled {
		t.Error("toolchain provisioning should default to enabled")
	}
	if d.Toolchain.Go != "" || d.Toolchain.Rust != "" || d.Toolchain.Node != "" {
		t.Errorf("no toolchain version may be defaulted; the manifests state them: %+v", d.Toolchain)
	}
}

func TestLoadToolchainOverride(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "toolchain:\n  enabled: false\n  go: \"1.27.0\"\n")

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Toolchain.Enabled {
		t.Error("toolchain.enabled: false did not disable provisioning")
	}
	if got.Toolchain.Go != "1.27.0" {
		t.Errorf("Toolchain.Go = %q, want the override 1.27.0", got.Toolchain.Go)
	}
	// The section is new and sits alongside the rest; naming it must not
	// zero its neighbours.
	if !got.Rust.Enabled || got.Coverage.Tolerance != 0.1 || !got.Coverage.Patch.Go.Enabled {
		t.Errorf("the toolchain section disturbed other defaults: %+v", got)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "rust: [this is not a mapping\n")

	if _, err := Load(dir); err == nil {
		t.Fatal("expected an error parsing invalid YAML, got nil")
	}
}

func write(t *testing.T, dir, contents string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(FileName))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTypeScriptLinterDefaultsToBiome(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TypeScript.Linter != LinterBiome {
		t.Errorf("TypeScript.Linter = %q, want %q — Biome is the only engine", cfg.TypeScript.Linter, LinterBiome)
	}
}

func TestLoadTypeScriptLinterBiome(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "typescript:\n  linter: biome\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TypeScript.Linter != LinterBiome {
		t.Errorf("TypeScript.Linter = %q, want %q", cfg.TypeScript.Linter, LinterBiome)
	}
	// Naming the linter explicitly must not disturb anything else in the section.
	if !cfg.TypeScript.Enabled {
		t.Error("TypeScript.Enabled was zeroed by a partial typescript: section")
	}
}

// A repo still carrying `linter: eslint` has stated which rule set it gates on.
// Accepting the key and running Biome anyway would change that silently —
// Biome's security group is six JSX/eval/secret rules where
// eslint-plugin-security was Node/backend heuristics, and Biome's correctness
// group fires on things ESLint never reported. It must be told.
func TestLoadRejectsTheRetiredESLintValue(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "typescript:\n  linter: eslint\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load silently accepted the retired eslint linter")
	}
	for _, want := range []string{"typescript.linter", "eslint", "biome"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it cannot be acted on: %v", want, err)
		}
	}
}

// TestLoadRejectsUnknownLinter guards the failure mode the validator exists
// for: a misspelled value that silently fell back would have a repo believe it
// had configured something lydite never read, while every run reported [PASS].
func TestLoadRejectsUnknownLinter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "typescript:\n  linter: bimoe\n")
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load accepted an unknown typescript.linter")
	}
	if !strings.Contains(err.Error(), "typescript.linter") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

// The floor is opt-in: absent from .lydite/config.yml it must be 0, which disables
// the gate. A default above 0 would fail existing repos on a lydite upgrade
// over a gap they never agreed to gate on.
func TestCoverageFloorDefaultsToDisabled(t *testing.T) {
	if got := Default().Coverage.Floor; got != 0 {
		t.Errorf("default coverage.floor = %v, want 0 (disabled)", got)
	}
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Coverage.Floor; got != 0 {
		t.Errorf("coverage.floor with no config file = %v, want 0", got)
	}
}

func TestCoverageFloorLoadsFromFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "coverage:\n  floor: 60\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Coverage.Floor != 60 {
		t.Errorf("coverage.floor = %v, want 60", cfg.Coverage.Floor)
	}
	// Setting the floor must not disturb the other coverage defaults.
	if cfg.Coverage.Tolerance != 0.1 || cfg.Coverage.Patch.Tolerance != 0.1 {
		t.Errorf("coverage defaults disturbed: %+v", cfg.Coverage)
	}
}

// A floor no unit could satisfy, or one that quietly disables itself, is
// rejected rather than accepted: NaN makes every comparison false, so the
// gate would print nothing and pass while looking configured, and a floor
// above 100 fails a fully covered unit.
func TestCoverageFloorRejectsImpossibleValues(t *testing.T) {
	for _, body := range []string{
		"coverage:\n  floor: -1\n",
		"coverage:\n  floor: 101\n",
		"coverage:\n  floor: .nan\n",
		"coverage:\n  floor: .inf\n",
	} {
		dir := t.TempDir()
		write(t, dir, body)
		if _, err := Load(dir); err == nil {
			t.Errorf("Load accepted %q, want a rejection", body)
		}
	}
}
