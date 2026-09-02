// Package config loads .lydite/config.yml, an optional config file: lydite's
// default (no file present) is to scan everything it detects with every
// check enabled and to produce coverage itself. The file cannot tune
// severity or suppress individual findings (that's what a fix-up pass +
// #nosec/nosemgrep annotations in the scanned repo itself are for). What it
// can do falls in two groups:
//
//   - Opt out: disable a language's checks entirely, exclude specific paths
//     from ecosystem/package detection, override Semgrep's ruleset, adjust
//     the coverage gates' noise tolerance.
//   - Describe the repo's pipeline: coverage.source says whether lydite or
//     a prior CI job owns coverage production, and coverage.{go,rust} locate
//     that job's reports. These aren't narrowing anything — they're facts
//     about how the repo is built that every invocation in it shares, which
//     is exactly why they belong in a file at the scan root rather than in a
//     flag each caller has to remember to repeat.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the directory at the scan root holding every file that configures
// lydite: this file, the exemption set (referral.FileName) and the component
// declaration (component.FileName).
//
// One directory rather than a family of dotfiles at the root, because a
// dotfile beside a dot-directory that both configure the same tool is an
// arrangement whose shape nobody can predict from either half.
const Dir = ".lydite"

// FileName is the config file lydite looks for, relative to the scan root.
const FileName = Dir + "/config.yml"

// Language is the opt-out surface for one of the three supported ecosystems.
type Language struct {
	Enabled bool     `yaml:"enabled"`
	Exclude []string `yaml:"exclude"`
}

// Linter names which engine backs the TypeScript check. Biome is the only
// value, and the key survives a single-engine world for one reason: a repo
// that still carries `linter: eslint` must be told, not silently switched.
// Dropping the field would make yaml.Unmarshal ignore it, and such a repo
// would start gating on a different rule set — more correctness findings,
// fewer Node/backend security heuristics — with nothing in the output saying
// so. See docs/adr/0008.
type Linter string

const (
	// LinterBiome reports Biome's security *and* correctness groups.
	LinterBiome Linter = "biome"
	// LinterESLint is retired. It is defined so validateLinter can recognise
	// it and name what happened, and is never a value lydite acts on.
	LinterESLint Linter = "eslint"
)

// TypeScriptLanguage extends Language with TS-only coverage install
// configuration.
type TypeScriptLanguage struct {
	Language `yaml:",inline"`
	// Linter selects the engine backing the TypeScript check. LinterBiome is
	// the only accepted value.
	Linter Linter `yaml:"linter"`
	// Install overrides coverage's install-command auto-detection (npm ci /
	// corepack enable && yarn install --immutable / pnpm install
	// --frozen-lockfile, chosen by the root's lockfile) with an explicit
	// shell command. Needed for Corepack-pinned or otherwise nonstandard
	// install flows auto-detection can't infer, or to resolve an ambiguous
	// multi-lockfile root that auto-detection otherwise skips. Only
	// consulted by coverage (internal/coverage), never by scan. Unset means:
	// use auto-detection, falling back to no install step if no single
	// recognized lockfile is found.
	Install string `yaml:"install,omitempty"`
}

// Semgrep is the opt-out/override surface for the Semgrep check.
type Semgrep struct {
	Enabled bool   `yaml:"enabled"`
	Config  string `yaml:"config"`
}

// PatchLanguage is the opt-out surface for one language's patch-coverage gate.
type PatchLanguage struct {
	Enabled bool `yaml:"enabled"`
}

// PatchCoverage is the opt-out surface for the patch-coverage gate, per
// language. Patch coverage has no threshold of its own — it always gates
// against that language's existing aggregate baseline (patch% >=
// baseline% - tolerance).
type PatchCoverage struct {
	Rust       PatchLanguage `yaml:"rust"`
	TypeScript PatchLanguage `yaml:"typescript"`
	Go         PatchLanguage `yaml:"go"`
	// Tolerance is the patch gate's own dip allowance in percentage points,
	// deliberately independent of Coverage.Tolerance: patch% is an exact
	// hit/total ratio with no measurement noise, but the baseline it gates
	// against is a noisy aggregate — hence a small default of its own.
	// Keeping the knobs separate means raising the aggregate tolerance for a
	// noisy test suite never silently weakens the untested-new-code check.
	Tolerance float64 `yaml:"tolerance"`
}

// Coverage is the opt-out/override surface for coverage gating.
//
// There is deliberately no `typescript:` section here. Istanbul/Vitest write
// <pkgDir>/coverage/{coverage-summary.json,lcov.info} by fixed convention,
// not by project-varying choice, so there has never been anything for an
// override to say — the existing no-override precedent for TS carries over
// unchanged.
type Coverage struct {
	Patch PatchCoverage `yaml:"patch"`
	// Tolerance is the number of percentage points a language's aggregate
	// coverage may dip below its baseline before the gate fails. Coverage
	// measurement is noisy at the sub-tenth level (timing-dependent
	// instrumentation, tool version drift), so a strict comparison fails PRs
	// with "86.1% vs baseline 86.1%, regressed 0.0%" — a dip smaller than
	// the displayed precision — even when the PR touches no code in that
	// language. The comparison happens at the report's display precision
	// (tenths), so 0 means "fail any dip the report can show" rather than
	// "fail any bit-level difference". Within-tolerance dips are also
	// restored to the prior value when a main run records a new baseline, so
	// tolerated dips can't compound across merges. The patch gate has its
	// own knob (Patch.Tolerance).
	Tolerance float64 `yaml:"tolerance"`
	// Floor is the minimum coverage percentage any single measured unit — a
	// Go module, a Rust crate/workspace root, a TypeScript package — must
	// reach. 0 disables the check, which is the default: it is opt-in, so
	// upgrading lydite never starts failing a repo over a gap it has always
	// had.
	//
	// It exists because the aggregate figure is a line-weighted ratio, and
	// weighting by lines is deliberately blind to a small unit nobody tests:
	// a package with 0 of 8 lines covered is 0.1% of an 8,305-line repo, so
	// the headline barely moves and the gate says nothing. The two questions
	// are genuinely different — "did this change leave code untested?" is the
	// aggregate, "is there a unit nobody tests at all?" is this — and neither
	// answers the other. See docs/adr/0007-line-weighted-coverage-aggregation.md.
	//
	// Compared at the report's display precision (tenths), like the
	// tolerances, so a unit shown as meeting the floor is never failed for a
	// difference the report cannot show.
	Floor float64 `yaml:"floor"`
}

// Toolchain is the override surface for language-toolchain provisioning —
// making sure the Go, Rust and Node runtimes lydite's checks need are
// present at the version the repo requires.
//
// It carries no version by default, and that is the design rather than an
// omission. The versions come from the files that already state them and that
// the language's own tooling already enforces: the `go` and `toolchain`
// directives in every discovered go.mod, the channel in rust-toolchain.toml,
// engines.node or .nvmrc. Restating one here could only agree redundantly or
// drift silently, and a stale duplicate is worse than no duplicate because it
// reads as authoritative. The fields below exist for the case where a repo
// needs a deliberate local exception to what its manifests say — an override,
// not a source of truth.
type Toolchain struct {
	// Enabled turns provisioning off while keeping the diagnostics. Set false
	// on an air-gapped runner, or one where every toolchain is already
	// provisioned by the image: lydite still reports what each ecosystem
	// declares and whether PATH satisfies it, but never downloads or installs.
	Enabled bool `yaml:"enabled"`
	// Go, Rust and Node override the version read from the manifests. Unset
	// (the intended state) means "use what the repo declares".
	Go   string `yaml:"go,omitempty"`
	Rust string `yaml:"rust,omitempty"`
	Node string `yaml:"node,omitempty"`
}

// Config is lydite's full, resolved configuration for one scan.
type Config struct {
	Rust       Language           `yaml:"rust"`
	TypeScript TypeScriptLanguage `yaml:"typescript"`
	Go         Language           `yaml:"go"`
	Semgrep    Semgrep            `yaml:"semgrep"`
	Coverage   Coverage           `yaml:"coverage"`
	Toolchain  Toolchain          `yaml:"toolchain"`
}

// Default returns lydite's zero-config behavior: every language and Semgrep
// enabled, no excludes, Semgrep's ruleset set to "auto", lydite producing
// coverage itself, every language's patch-coverage gate enabled, and
// toolchain provisioning on with every version taken from the repo's own
// manifests.
func Default() Config {
	return Config{
		Rust:       Language{Enabled: true},
		TypeScript: TypeScriptLanguage{Language: Language{Enabled: true}, Linter: LinterBiome},
		Go:         Language{Enabled: true},
		Semgrep:    Semgrep{Enabled: true, Config: "auto"},
		Toolchain:  Toolchain{Enabled: true},
		Coverage: Coverage{
			Tolerance: 0.1,
			Patch: PatchCoverage{
				Rust:       PatchLanguage{Enabled: true},
				TypeScript: PatchLanguage{Enabled: true},
				Go:         PatchLanguage{Enabled: true},
				Tolerance:  0.1,
			},
		},
	}
}

// Load reads the config file from root if present, merging it onto Default().
// A missing file is not an error — it's the common case. Merge semantics:
// yaml.Unmarshal only overwrites fields explicitly present in the file, so a
// section omitted entirely (or a key omitted within a present section) keeps
// its Default() value rather than being zeroed.
func Load(root string) (Config, error) {
	cfg := Default()
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path) // #nosec G304 -- root is the CLI's own --dir flag, supplied by whoever runs lydite, not untrusted remote input
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := rejectRemoved(data); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := validateTolerances(cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := validateLinter(cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := validateFloor(cfg); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// LoadHistorical reads the configuration of a tree lydite is *measuring*,
// rather than one it is being configured by: the base tree a coverage baseline
// is computed at.
//
// It parses and merges exactly as Load does and then validates nothing, which
// is the whole difference. Every validation here exists to tell an author that
// what they wrote is stale or wrong — `coverage.source` is no longer read,
// `linter: eslint` gates on a rule set that is gone — and the author of a
// historical tree is not being addressed and cannot act. Worse, the rejection
// is guaranteed to fire on the one tree it must not: the base tree of the pull
// request that removes those keys still carries them, and the metric version
// bump makes that run a cache miss, so it always measures that tree. Validating
// it would fail every such migration, and keep failing every branch cut before
// the removal landed.
//
// Nothing read from a historical tree reaches a verdict. It supplies what the
// suites need in order to run there; the gate's own knobs come from the tree
// being gated. A file that is not YAML at all is still an error, because then
// there is nothing to read.
func LoadHistorical(root string) (Config, error) {
	cfg := Default()
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path) // #nosec G304 -- root is a worktree lydite created from a commit in the repository it was pointed at
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// validateSource rejects
// removedKeys names the keys lydite once read and no longer does, so a
// repository carrying one is told rather than quietly measured differently.
//
// Every one of them existed to locate a coverage report some other job
// produced. lydite now writes every report itself, to a path it chose, from
// the instrumented variant the component's own runner derives — so there is
// nothing left for any of them to say.
var removedKeys = []struct {
	Key string
	Why string
}{
	{"coverage.source",
		"lydite measures coverage from each component's own instrumented run, so there is no longer a pipeline half that owns producing it"},
	{"coverage.go.report", reportKeyRemoved},
	{"coverage.rust.report", reportKeyRemoved},
	{"coverage.rust.lcov", reportKeyRemoved},
}

// rejectRemoved refuses a config file that still carries a key lydite has
// stopped reading.
//
// Rejecting rather than ignoring, the stance validateLinter takes for `linter:
// eslint` and for the same reason. A dropped key means a repository measuring
// something other than what its author wrote, while every run still reports a
// pass — and `coverage.source: report` in particular said "never run the
// tests", so ignoring it would have lydite run every suite in a pipeline built
// on the promise that it would not.
//
// It walks the document rather than decoding into a shadow struct. A struct
// answers "was this key present" only through whatever its field types accept,
// and a mismatch there fails the decode instead of finding the key — which is
// a rejection that silently stops rejecting.
func rejectRemoved(data []byte) error {
	var doc yaml.Node
	// A document this cannot parse is one Load's own unmarshal has already
	// rejected with a better message.
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil //nolint:nilerr // the parse error is the caller's to report
	}
	for _, k := range removedKeys {
		if nodeAt(&doc, strings.Split(k.Key, ".")) != nil {
			return fmt.Errorf("%s is no longer supported: %s. Remove the key; see docs/adr/0019-coverage-per-component-gated-by-lydite-test.md", k.Key, k.Why)
		}
	}
	return nil
}

// nodeAt returns the node at a dotted key path, or nil when the path is not
// present. Only the presence of the key matters: a value of any shape, an
// explicit null included, is the repository having written it.
func nodeAt(n *yaml.Node, path []string) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, key := range path {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		found := false
		// Mapping content alternates key, value.
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				n = n.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return n
}

// reportKeyRemoved is the one sentence every removed report path wants, said
// once so the four cannot drift.
const reportKeyRemoved = "lydite writes every coverage report itself, at a path derived from the component's runner, so there is nothing left to locate"

// validateLinter accepts only LinterBiome, and gives the retired ESLint value
// its own message.
//
// Rejecting rather than ignoring is the whole point. A repo carrying
// `linter: eslint` has stated which rule set it gates on; accepting the key and
// running Biome anyway would change that silently — Biome's security group is
// six JSX/eval/secret rules where eslint-plugin-security was Node/backend
// heuristics, and Biome's correctness group fires on things ESLint never
// reported. A scan that quietly starts measuring something else is the failure
// this file rejects unknown values to prevent; a value that used to be valid
// deserves the same treatment, and a better sentence.
func validateLinter(cfg Config) error {
	switch cfg.TypeScript.Linter {
	case LinterBiome:
		return nil
	case LinterESLint:
		return fmt.Errorf("typescript.linter: %q is no longer supported — Biome is the only TypeScript linter. Remove the key (or set it to %q) and review the rule-set change in docs/adr/0008", LinterESLint, LinterBiome)
	default:
		return fmt.Errorf("typescript.linter must be %q, got %q", LinterBiome, cfg.TypeScript.Linter)
	}
}

// validateTolerances rejects tolerance values that silently invert or
// disable the coverage gates: a negative tolerance fails languages whose
// coverage held steady or improved ("regressed -0.3%"), and NaN (or ±Inf,
// both valid YAML) makes the gate comparison unconditionally false, turning
// both gates off while still printing [PASS] lines.
func validateTolerances(cfg Config) error {
	for name, tol := range map[string]float64{
		"coverage.tolerance":       cfg.Coverage.Tolerance,
		"coverage.patch.tolerance": cfg.Coverage.Patch.Tolerance,
	} {
		if math.IsNaN(tol) || math.IsInf(tol, 0) || tol < 0 {
			return fmt.Errorf("%s must be a finite, non-negative number of percentage points, got %v", name, tol)
		}
	}
	return nil
}

// validateFloor rejects a per-unit floor no unit could satisfy or that would
// silently disable itself. NaN makes every comparison false, so the gate
// prints nothing and passes while looking configured; a floor above 100 fails
// every unit including a fully covered one, which is a typo (1000 for 100)
// rather than a policy anyone holds.
func validateFloor(cfg Config) error {
	floor := cfg.Coverage.Floor
	if math.IsNaN(floor) || math.IsInf(floor, 0) || floor < 0 || floor > 100 {
		return fmt.Errorf("coverage.floor must be a percentage between 0 and 100 (0 disables it), got %v", floor)
	}
	return nil
}

// AllExcludes merges every language's exclude list — used by callers (scan,
// coverage) whose initial ecosystem-detection pass doesn't yet know which
// language a given excluded directory belongs to.
func (c Config) AllExcludes() []string {
	var out []string
	out = append(out, c.Rust.Exclude...)
	out = append(out, c.TypeScript.Exclude...)
	out = append(out, c.Go.Exclude...)
	return out
}
