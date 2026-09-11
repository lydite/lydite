package typescript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/finding"
)

// biomeCfg decodes the embedded config so assertions read against structure
// rather than substrings — a `"security": "error"` that landed in the wrong
// object would satisfy strings.Contains and gate on nothing.
func biomeCfg(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(biomeConfig, &m); err != nil {
		t.Fatalf("biome.json is not valid JSON: %v", err)
	}
	return m
}

// TestBiomeConfigEnablesOnlyTheGatedGroups pins the contract the linter choice
// promises: security and correctness gate, and nothing else does. Without
// recommended:false, Biome's default preset turns on style and suspicious too,
// and lydite would start failing PRs over formatting-adjacent opinions it never
// agreed to enforce.
func TestBiomeConfigEnablesOnlyTheGatedGroups(t *testing.T) {
	rules, ok := biomeCfg(t)["linter"].(map[string]any)["rules"].(map[string]any)
	if !ok {
		t.Fatal("biome.json has no linter.rules object")
	}
	// `preset: "none"` rather than the older `recommended: false`: Biome's docs
	// mark `recommended` deprecated in favour of `preset`, and Dependabot now
	// bumps @biomejs/biome weekly, so the forward-compatible spelling is the one
	// to be pinned to. Verified byte-identical output on a real fixture against
	// 2.5.8, whose PresetConfig enum is ["recommended", "all", "none"].
	if rules["preset"] != "none" {
		t.Errorf(`linter.rules.preset = %v, want "none" — Biome's default preset would enable style/suspicious too`, rules["preset"])
	}
	if _, present := rules["recommended"]; present {
		t.Error("linter.rules.recommended is set; it is deprecated in favour of preset")
	}
	for _, group := range []string{"security", "correctness"} {
		if rules[group] != "error" {
			t.Errorf("linter.rules.%s = %v, want \"error\"", group, rules[group])
		}
	}
	for _, group := range []string{"style", "suspicious", "complexity", "a11y", "nursery", "performance"} {
		if _, present := rules[group]; present {
			t.Errorf("linter.rules.%s is set; lydite gates on security and correctness only", group)
		}
	}
}

// TestBiomeConfigDisablesFormatterAndAssist guards against lydite reporting a
// formatting diff as a security finding. Biome is a formatter as well as a
// linter, and both default to on.
func TestBiomeConfigDisablesFormatterAndAssist(t *testing.T) {
	cfg := biomeCfg(t)
	for _, key := range []string{"formatter", "assist"} {
		section, ok := cfg[key].(map[string]any)
		if !ok {
			t.Errorf("biome.json has no %s section; it defaults to enabled", key)
			continue
		}
		if section["enabled"] != false {
			t.Errorf("%s.enabled = %v, want false", key, section["enabled"])
		}
	}
}

// TestBiomeConfigEnablesTailwindDirectives guards a failure that had no
// workaround from the scanned repository. Tailwind v4 moved configuration into
// ordinary .css files — @theme, @custom-variant, @utility, @source — and
// Biome's CSS parser rejects all of them unless this is set. The result is a
// `parse` diagnostic, which reportableBiome keeps on purpose (a file lydite
// could not lint is worth knowing about), so the TypeScript check failed on a
// stylesheet that was never wrong.
//
// A repository cannot fix it locally, which is why it belongs here: suppression
// comments do not apply to parse errors, and a nested biome.json is ignored
// when Biome runs under --config-path, as lintDirBiome does.
func TestBiomeConfigEnablesTailwindDirectives(t *testing.T) {
	css, ok := biomeCfg(t)["css"].(map[string]any)
	if !ok {
		t.Fatal("biome.json has no css section; Tailwind v4 directives fail to parse without it")
	}
	parser, ok := css["parser"].(map[string]any)
	if !ok {
		t.Fatal("biome.json has no css.parser section")
	}
	if parser["tailwindDirectives"] != true {
		t.Errorf("css.parser.tailwindDirectives = %v, want true", parser["tailwindDirectives"])
	}
}

// TestBiomeConfigIgnoresMatchesDefaultSkipDirs guards a failure verified
// against Biome directly, on 2.5.8 and again on 2.5.10: with these negations
// removed, Biome lints
// dist/ and reports findings inside a minified production bundle. The ignores
// are load-bearing, not decorative.
func TestBiomeConfigIgnoresMatchesDefaultSkipDirs(t *testing.T) {
	files, ok := biomeCfg(t)["files"].(map[string]any)
	if !ok {
		t.Fatal("biome.json has no files section")
	}
	includes, ok := files["includes"].([]any)
	if !ok {
		t.Fatal("biome.json has no files.includes list")
	}
	got := map[string]bool{}
	for _, p := range includes {
		got[p.(string)] = true
	}
	if !got["**"] {
		t.Error(`files.includes lacks "**"; the negations below only subtract from what it matches`)
	}
	for _, dir := range []string{"node_modules", "dist", "build", "target", "vendor", ".git", ".bare", ".next", "coverage"} {
		if !got["!**/"+dir] {
			t.Errorf("files.includes missing %q", "!**/"+dir)
		}
	}
}

// TestReportableBiomeGatesOnlyOnOurGroups guards the containment described on
// reportableBiome: a nested biome.json declaring "root": false is merged into
// lydite's config by Biome, so the scanned project can inject its own rules
// into our run. Category filtering is the only thing that keeps those out of
// the verdict — and suppression/unused fires on the project's own biome-ignore
// comments for rules lydite never enabled. The category is plural —
// `suppressions/unused` — verified against Biome 2.5.8, not guessed from docs.
func TestReportableBiomeGatesOnlyOnOurGroups(t *testing.T) {
	for _, category := range []string{
		"lint/security/noGlobalEval",
		"lint/security/noSecrets",
		"lint/correctness/noUnreachable",
	} {
		if !reportableBiome(category) {
			t.Errorf("reportableBiome(%q) = false, want true", category)
		}
	}
	// A file Biome cannot parse emits only `parse` diagnostics (category verified
	// against 2.5.8 with a deliberately broken .ts file). Filtering those out
	// leaves count == 0, which clears the error and prints "no findings" — a
	// package where nothing was linted would report as a clean pass.
	// Failure categories, none of which are rule opinions: a package where these
	// fire was not successfully linted, and filtering them out reports it as a
	// clean pass. internalError/io is the nastier one — Biome emits it with
	// summary.errors == 0, so the exit code does not give it away either.
	for _, category := range []string{"parse", "syntax", "internalError/io", "configuration", "deserialize"} {
		if !reportableBiome(category) {
			t.Errorf("reportableBiome(%q) = false; a package that was never linted would be reported as a pass", category)
		}
	}
	for _, category := range []string{
		"lint/style/useConst",
		"lint/suspicious/noExplicitAny",
		"lint/complexity/noForEach",
		"lint/nursery/someRule",
		"suppressions/unused",
		"format",
		"assist/source/organizeImports",
	} {
		if reportableBiome(category) {
			t.Errorf("reportableBiome(%q) = true, want false", category)
		}
	}
}

// Biome is the one check lydite renders rather than the tool, because nothing
// of Biome's own reaches the terminal. What a consumer anchors and what a human
// reads must therefore come from the same set.
func TestBiomeDiagnosticsBecomeLocatedClaims(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.ts"), []byte(
		"const a = 1\neval(userInput)\neval(other)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := biomeReport{Diagnostics: []biomeDiagnostic{
		diagnosticAt("bad.ts", 2, "lint/security/noGlobalEval", "error", "eval() is dangerous"),
		diagnosticAt("bad.ts", 3, "lint/security/noGlobalEval", "error", "eval() is dangerous"),
		diagnosticAt("bad.ts", 1, "lint/style/useConst", "error", "an opinion lydite never agreed to enforce"),
	}}

	got := biomeFindings(dir, report)
	if len(got) != 2 {
		t.Fatalf("%d claims, want 2 — a style opinion is not one of lydite's", len(got))
	}
	if got[0].Rule != "lint/security/noGlobalEval" || got[0].Line != 2 || got[0].Path != "bad.ts" {
		t.Errorf("the claim lost its location: %+v", got[0])
	}
	if got[0].Severity != "error" {
		t.Errorf("severity is %q, want the tool's own word", got[0].Severity)
	}
	// One rule firing twice in one file is two claims, told apart by the code
	// each fired on.
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Errorf("two occurrences of one rule share the fingerprint %s", got[0].Fingerprint())
	}
}

// A rule firing on identical code in one file has nothing but source order to
// tell its two claims apart.
func TestIdenticalBiomeSitesAreNumbered(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.ts"), []byte(
		"eval(x)\neval(x)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := biomeFindings(dir, biomeReport{Diagnostics: []biomeDiagnostic{
		diagnosticAt("bad.ts", 1, "lint/security/noGlobalEval", "error", "eval() is dangerous"),
		diagnosticAt("bad.ts", 2, "lint/security/noGlobalEval", "error", "eval() is dangerous"),
	}})

	if len(got) != 2 || got[0].Ordinal != 0 || got[1].Ordinal != 1 {
		t.Fatalf("ordinals are wrong: %+v", got)
	}
	if got[0].Fingerprint() == got[1].Fingerprint() {
		t.Errorf("two identical sites share the fingerprint %s", got[0].Fingerprint())
	}
}

// diagnosticAt builds the subset of Biome's report lydite reads.
func diagnosticAt(path string, line int, category, severity, message string) biomeDiagnostic {
	var d biomeDiagnostic
	d.Category, d.Severity, d.Message = category, severity, message
	d.Location.Path = path
	d.Location.Start.Line = line
	return d
}

// reportableBiome deliberately keeps what is not a rule opinion, which includes
// a parse failure and a file Biome could not read. Those fail the row and are
// the reason it fails, and they locate nothing: a claim carrying line 0 of the
// component's own directory is not one anything can anchor or tell from the
// next.
func TestADiagnosticThatLocatesNothingMakesNoClaim(t *testing.T) {
	dir := t.TempDir()
	got := biomeFindings(dir, biomeReport{Diagnostics: []biomeDiagnostic{
		diagnosticAt("", 0, "internalError/io", "error", "a source file Biome cannot read"),
		diagnosticAt("bad.ts", 0, "parse", "error", "expected a declaration"),
	}})

	if len(got) != 0 {
		t.Errorf("a diagnostic that locates nothing produced %+v", got)
	}
}

// A scan runs over a whole repository rather than over a diff, so it knows of
// no change to place a claim against. Unanchorable is the safe default: such a
// claim belongs in the standing comment rather than offered to a platform that
// would refuse it.
func TestAScannersClaimIsUnanchorable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.ts"), []byte("eval(x)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := biomeFindings(dir, biomeReport{Diagnostics: []biomeDiagnostic{
		diagnosticAt("bad.ts", 1, "lint/security/noGlobalEval", "error", "eval() is dangerous"),
	}})

	if len(got) != 1 || got[0].Anchor != finding.AnchorNowhere {
		t.Errorf("a scanner's claim anchored %q, want nowhere", got[0].Anchor)
	}
}
