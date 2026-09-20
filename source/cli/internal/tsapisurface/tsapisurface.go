// Package tsapisurface compares a TypeScript package's public API between two
// trees on disk and reports every incompatible change as a located finding.
//
// The comparison is `@microsoft/api-extractor` at the version lydite pins, run
// as a subprocess against each tree: it is an npm package, so there is no
// library to call from Go and no cgo to reach one with. What it emits is an API
// report — every re-export resolved to the declaration it names, doc comments
// stripped, the result sorted — and no verdict at all, so the classification is
// lydite's own: a declaration the head report holds and the base does not is an
// addition and compatible, and one the base holds and the head does not, or one
// whose text changed, is a break. See [ADR 0040]'s TypeScript amendment.
//
// The package knows nothing about git, components or verdicts. It is handed two
// directories that already hold the component checked out, and hands back raw
// findings for a caller to finish.
//
// api-extractor reads a declaration file and nothing else, so each tree's own
// install and its own build have to have run — and their exit codes are what
// say the tree could be measured. `tsc` writes a complete `dist/` for a tree
// that does not typecheck, and an export whose type is inferred from a
// dependency degrades to `any` when node_modules is absent, so neither the
// presence of a declaration nor api-extractor's own exit code can stand in for
// them: a comparison over a tree whose install did not complete invents breaks
// that are not there.
//
// Running those two steps runs the change's own code — `npm ci` executes
// lifecycle scripts and the build executes the tree's own compiler
// configuration, which is the same class as a Rust crate's build.rs. They run
// with an isolated environment rather than this process's own — see
// isolatedEnv — so that a component's own declared variables and this process's
// ambient ones don't collide inside the child. That keeps this process's own
// environment out of the child's cmd.Env; it is not, by itself, a boundary
// against a credential this process holds, for the reason
// internal/rustapisurface states at the same point. The caller that runs this
// comparison from a job holding a publishing credential must not itself hold
// that credential — see the referral / referral-publish split in
// .github/workflows/lydite-pr.yml, and
// agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md.
//
// [ADR 0040]: ../../../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md
package tsapisurface

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/typescript"
)

// Outcome is how much one comparison could say.
//
// Three states, because a surface that could not be compared reports no
// findings and so does one that was compared and found unbroken. Collapsing the
// two would render a gate that never ran as the green of one that ran and
// passed.
type Outcome int

const (
	// Unmeasurable is the zero value on purpose: a Result nobody filled in says
	// the surface was not compared, never that it is clean.
	Unmeasurable Outcome = iota
	// Unbroken is a comparison that ran and found no incompatible change.
	Unbroken
	// Broken is a comparison that ran and found at least one.
	Broken
)

// Result is what one comparison said.
type Result struct {
	Outcome Outcome
	// Findings are the incompatible changes, and are non-empty exactly when
	// Outcome is Broken.
	Findings []finding.Finding
	// Reason is why the surface could not be compared, in the words of whatever
	// refused — the tool's own account, or the install that failed. Empty for
	// every other outcome.
	Reason string
	// Skipped names every package under the component that resolves no entry
	// point, which is a package with no surface a consumer can reach rather
	// than a failure to compare one. A component where *every* package is one
	// of these is Unmeasurable instead: it opted in and there is nothing to
	// compare.
	//
	// internal/rustapisurface carries no such field because cargo selects the
	// packages itself. Here the selection is lydite's, so what it left out is
	// lydite's to say.
	Skipped []string
}

// Request is one component's comparison.
//
// A struct rather than internal/apisurface's positional arguments, because a
// subprocess needs what an in-process library does not: two environments, which
// are deliberately not the same one, and somewhere to put the output of an
// install and a build a developer would otherwise watch in silence.
type Request struct {
	// BaseDir and HeadDir are directories already holding the component
	// checked out, each at the directory its package.json roots. Materialising
	// the base tree is the caller's work; this package does no git.
	BaseDir, HeadDir string
	// Gate and Component name the caller's Finding.Gate and Finding.Component.
	// Both are supplied rather than fixed here because this package knows
	// nothing about report rows or declared components.
	Gate, Component string
	// Env is the environment the comparison runs under. Check reaches both
	// trees' install and build and api-extractor itself — the component's
	// resolved toolchain and the environment its declaration asks for. Install
	// reaches nothing but lydite's own provisioning of the pinned tool, and
	// carries none of what the scanned repository supplied: see executil.Env.
	Env executil.Env
	// Progress is where the two trees' own install and build output goes. Nil
	// discards it.
	Progress io.Writer
}

// reportFileName is what api-extractor is told to call the report. The report
// names the package in its own header, so the filename takes no part in the
// comparison.
const reportFileName = "surface.api.md"

// configPattern is the api-extractor configuration lydite writes into the
// package it is about to read.
//
// It goes in the package rather than in a temporary directory because
// api-extractor finds the package.json of the project it analyses by walking up
// from the configuration file, and refuses a configuration that reaches none.
// The file is removed as soon as the run is over.
const configPattern = "lydite-api-extractor-*.json"

// Compare reports every incompatible change to the public API of the TypeScript
// packages under req.HeadDir.
//
// There is no error return. Every way this comparison fails is one fact for the
// caller — the surface could not be compared, and here is what said so — and a
// second channel carrying half of them would leave a caller that handled only
// the other half silently reporting a pass.
//
// The findings come back raw. Path is relative to the tree the package was
// found in and **not** to the scan root, so a caller that knows the component's
// directory rebases them the way scan's labelled step does for every other
// producer. Ordinal and Anchor are left at their zero values for the same
// reason: both are decisions made over a report's whole set and against the
// lines the change touched, neither of which this package is given.
//
// [lydite:exclude_from_coverage][the proving ground installs the pinned
// api-extractor and builds two real trees with the component's own npm; a unit
// test here would run the machine's own node — what is lydite's to get right is
// the invocation, which argv and the written configuration state, and what is
// done with the report, which declarations and compare test directly against
// the reports recorded from the tool]
func Compare(ctx context.Context, req Request) Result {
	progress := req.Progress
	if progress == nil { // [lydite:exclude_from_mutation][Compare itself installs a tool and builds two real trees, the same reason it is excluded from coverage above; nothing here is exercised by a unit test]
		progress = io.Discard
	}
	toolchain, err := typescript.EnsureAPIExtractor(ctx, req.Env.Install)
	if err != nil {
		return Result{Reason: "installing the pinned api-extractor: " + err.Error()}
	}
	bin := filepath.Join(toolchain, "node_modules", ".bin", typescript.APIExtractorBin)

	surfaces, err := surfaces(req.BaseDir, req.HeadDir)
	if err != nil {
		return Result{Reason: "reading the component's package.json: " + err.Error()}
	}
	compared, skipped := partition(surfaces)
	if len(compared) == 0 {
		return Result{Reason: "no package under this component names an entry point, so it declares no surface a consumer can reach", Skipped: skipped}
	}

	env := isolatedEnv(req.Env.Check)
	for at, tree := range trees(req) {
		if !needsTree(compared, at) {
			// A tree with nothing to read here needs no install and no build.
			// The merge-base of a component this change introduces is exactly
			// this shape: surfaces already tolerates it having no manifest at
			// all, and a directory that does not exist would otherwise fail
			// npm before that tolerance is ever reached, turning "every
			// declaration is an addition" into "uncomputable" for every
			// component a change adds.
			continue
		}
		if reason := prepare(ctx, tree.dir, env, progress); reason != "" {
			return Result{Reason: tree.what + ": " + reason, Skipped: skipped}
		}
	}

	var findings []finding.Finding
	for _, s := range compared {
		for _, subpath := range s.subpaths {
			var reports [2][]declaration
			for at, tree := range trees(req) {
				text, reason := read(ctx, bin, env, tree.dir, s.rel, s.entries[at][subpath])
				if reason != "" {
					return Result{Reason: tree.what + ": " + reason, Skipped: skipped}
				}
				reports[at] = declarations(text)
			}
			findings = append(findings, compare(reports[0], reports[1], pkg{rel: s.rel, name: s.name}, subpath, req)...)
		}
	}
	return Result{Outcome: outcomeFor(findings), Findings: findings, Skipped: skipped}
}

// outcomeFor is Unbroken for no findings and Broken for at least one — the
// only two outcomes a comparison that ran can reach, Unmeasurable being the
// zero value a comparison that never ran leaves behind.
func outcomeFor(findings []finding.Finding) Outcome {
	if len(findings) > 0 {
		return Broken
	}
	return Unbroken
}

// partition splits the packages that have a surface to compare from the ones
// that name no entry point.
//
// A package naming none is skipped and said so rather than failed: that is the
// analogue of a Rust workspace's binary-only member, and the test is naming no
// entry point rather than being private — a private package with an exports map
// has a surface its workspace siblings consume, and a published package that
// names nothing has none.
func partition(surfaces []surface) ([]surface, []string) {
	var compared []surface
	var skipped []string
	for _, s := range surfaces {
		if len(s.subpaths) == 0 {
			skipped = append(skipped, s.label())
			continue
		}
		compared = append(compared, s)
	}
	return compared, skipped
}

// needsTree reports whether the tree at index at names a declaration for at
// least one compared package's subpath, which is the only reason its install
// and its build have anything to contribute.
func needsTree(compared []surface, at int) bool {
	for _, s := range compared {
		for _, subpath := range s.subpaths {
			if s.entries[at][subpath].dts != "" {
				return true
			}
		}
	}
	return false
}

// tree is one side of the comparison, and what to call it when it is the side
// that refused.
type tree struct {
	what, dir string
}

// trees is the two sides, in the order every per-entry-point read walks them:
// the merge-base first, so that index 0 is the base report everywhere.
func trees(req Request) [2]tree {
	return [2]tree{
		{what: "the merge-base tree", dir: req.BaseDir},
		{what: "this change's tree", dir: req.HeadDir},
	}
}

// surface is one package as both trees declare it.
type surface struct {
	// rel and name identify the package, as pkg's own fields do.
	rel, name string
	// subpaths is every entry point either tree names, sorted.
	//
	// The union rather than the head's own: a subpath only the merge-base named
	// is a whole entry point withdrawn, which is every declaration under it
	// removed, and reading only the head would report nothing at all.
	subpaths []string
	// entries is what each tree resolves for each of those subpaths, indexed
	// the way trees is. A subpath a tree does not name has no entry there and
	// no report either, which is what makes a new entry point pure addition.
	entries [2]map[string]entry
}

// label is how a package is named back to a caller.
func (s surface) label() string { return pkg{rel: s.rel, name: s.name}.label() }

// surfaces is every package under the head tree, with the entry points each
// tree names for it.
//
// The packages are enumerated from the head tree: a package the merge-base
// declared and this change does not is a directory that is gone, which the
// component's own file-level gates report and which this comparison has no
// manifest left to read.
func surfaces(baseDir, headDir string) ([]surface, error) {
	head, err := packagesOf(headDir)
	if err != nil {
		return nil, err
	}
	// A base tree with no readable manifest at all — a component this change
	// introduces — is every package new, which is every declaration an
	// addition. That is not a failure to compare.
	base, err := packagesOf(baseDir)
	if err != nil {
		base = nil
	}
	byRel := map[string]pkg{}
	for _, p := range base {
		byRel[p.rel] = p
	}
	out := make([]surface, 0, len(head))
	for _, p := range head {
		s := surface{rel: p.rel, name: p.name}
		s.entries[0] = bySubpath(byRel[p.rel].entries)
		s.entries[1] = bySubpath(p.entries)
		for subpath := range s.entries[0] {
			if _, both := s.entries[1][subpath]; !both {
				s.subpaths = append(s.subpaths, subpath)
			}
		}
		for subpath := range s.entries[1] {
			s.subpaths = append(s.subpaths, subpath)
		}
		sort.Strings(s.subpaths)
		out = append(out, s)
	}
	return out, nil
}

// bySubpath keys one tree's entry points by the subpath a consumer writes.
func bySubpath(entries []entry) map[string]entry {
	out := make(map[string]entry, len(entries))
	for _, e := range entries {
		out[e.subpath] = e
	}
	return out
}

// step is one of the two commands that have to succeed before a tree can be
// read, and what a caller is told when it does not.
type step struct {
	args    []string
	failure string
}

// steps are the install and the build, in order, for the component rooted at
// dir.
//
// `npm ci` where a lockfile resolved the tree and `npm install` where none did:
// ci installs precisely what was resolved and refuses a lockfile that disagrees
// with its package.json, which is the stronger of the two wherever it can run
// at all.
//
// The build is the tree's own `build` script, and `--if-present` rather than a
// failure where there is none: a package that builds by some other name emits
// no declaration, and api-extractor then refuses to read one and says which
// path was missing, which tells a reader more than a missing script would.
func steps(dir string) []step {
	install := "install"
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err == nil {
		install = "ci"
	}
	build := []string{"run", "build", "--if-present"}
	if root, err := readManifest(filepath.Join(dir, manifestName)); err == nil && len(workspaceGlobs(root.Workspaces)) > 0 {
		build = append(build, "--workspaces", "--include-workspace-root")
	}
	return []step{
		{
			args: []string{install, "--no-audit", "--no-fund"},
			failure: "npm " + install + " did not complete, so nothing here can be read: a partial node_modules degrades an " +
				"export whose type is inferred from a dependency to `any`, which invents breaks that are not there",
		},
		{
			args:    build,
			failure: "the build did not complete, so the declarations api-extractor would read are not the ones this tree declares",
		},
	}
}

// prepare installs and builds one tree, and answers why it could not be, or the
// empty string.
//
// [lydite:exclude_from_coverage][it runs the component's own npm over a real
// tree, for the reason Compare itself is excluded]
func prepare(ctx context.Context, dir string, env []string, progress io.Writer) string {
	for _, s := range steps(dir) {
		res := executil.RunQuietIsolatedEnv(ctx, dir, env, "npm", s.args...)
		// The progress writer is a log a reader watches, so a write that failed
		// costs that reader the output and nothing else: the step's own exit
		// code below is what the comparison rests on.
		_, _ = fmt.Fprint(progress, res.Output, res.Stderr)
		if !res.Ok() {
			return s.failure + ":\n" + refusal(res.Output+res.Stderr)
		}
	}
	return ""
}

// read runs api-extractor over one entry point in one tree and answers the
// report it wrote, or why it could not be written.
//
// An entry the tree does not name has no report and is no failure: the other
// tree named it, and every declaration under it is an addition or a removal
// accordingly.
//
// [lydite:exclude_from_coverage][it runs the pinned api-extractor over a real
// built tree, for the reason Compare itself is excluded]
func read(ctx context.Context, bin string, env []string, treeDir, rel string, e entry) (string, string) {
	if e.dts == "" {
		return "", ""
	}
	pkgDir := filepath.Join(treeDir, filepath.FromSlash(rel))
	out, err := os.MkdirTemp("", "lydite-api-report-")
	if err != nil {
		return "", "making somewhere to put the API report: " + err.Error()
	}
	defer func() { _ = os.RemoveAll(out) }()
	reportDir, tempDir := filepath.Join(out, "report"), filepath.Join(out, "temp")
	for _, dir := range []string{reportDir, tempDir} {
		// api-extractor refuses to create either folder itself, and says so
		// with the same exit code a real refusal carries.
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", "making somewhere to put the API report: " + err.Error()
		}
	}
	config, err := json.Marshal(configFor(pkgDir, e, reportDir, tempDir))
	if err != nil {
		return "", "writing api-extractor's configuration: " + err.Error()
	}
	configPath, err := writeConfig(pkgDir, config)
	if err != nil {
		return "", "writing api-extractor's configuration: " + err.Error()
	}
	defer func() { _ = os.Remove(configPath) }()

	// --local so that a committed report file that is missing, or that differs
	// from what this run produced, is not itself a failure: lydite compares two
	// reports it generated and has no stake in whichever one the package keeps.
	res := executil.RunQuietIsolatedEnv(ctx, pkgDir, env, bin, "run", "--local", "--config", configPath)
	if !res.Ok() {
		return "", "api-extractor could not read " + e.subpath + " of " + rel + ":\n" + refusal(res.Output+res.Stderr)
	}
	report, err := os.ReadFile(filepath.Join(reportDir, reportFileName)) // #nosec G304 -- reportDir is this function's own MkdirTemp result
	if err != nil {
		return "", "api-extractor exited cleanly and wrote no report: " + err.Error()
	}
	return string(report), ""
}

// writeConfig puts the configuration in the package and answers where.
func writeConfig(pkgDir string, config []byte) (string, error) {
	file, err := os.CreateTemp(pkgDir, configPattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err := file.Write(config); err != nil { // [lydite:exclude_from_mutation][differs from the branch below only if Write or the second Close fails after CreateTemp has just opened this file, which needs a device error no test can provoke]
		_ = file.Close()
		return path, err
	}
	return path, file.Close()
}

// extractorConfig is the part of api-extractor.json lydite writes.
//
// Every path is absolute, so nothing depends on where the configuration file
// itself was put. The doc model, the rollup and the metadata are off because
// the API report is the whole of what is compared, and producing the others
// would cost a second analysis of the same program.
type extractorConfig struct {
	ProjectFolder          string         `json:"projectFolder"`
	MainEntryPointFilePath string         `json:"mainEntryPointFilePath"`
	Compiler               compilerConfig `json:"compiler"`
	APIReport              reportConfig   `json:"apiReport"`
	DocModel               featureConfig  `json:"docModel"`
	DtsRollup              featureConfig  `json:"dtsRollup"`
	TSDocMetadata          featureConfig  `json:"tsdocMetadata"`
	Messages               messagesConfig `json:"messages"`
}

type compilerConfig struct {
	TsconfigFilePath string `json:"tsconfigFilePath"`
}

type reportConfig struct {
	Enabled          bool   `json:"enabled"`
	ReportFileName   string `json:"reportFileName"`
	ReportFolder     string `json:"reportFolder"`
	ReportTempFolder string `json:"reportTempFolder"`
}

type featureConfig struct {
	Enabled bool `json:"enabled"`
}

// messagesConfig keeps api-extractor's own advice out of the exit code.
//
// `ae-missing-release-tag` fires once per exported declaration in every package
// that does not use release tags, which is most of them, and a warning is not a
// change to the surface. logLevel warning rather than none so the run's own
// output still carries it for a reader of the log.
type messagesConfig struct {
	ExtractorMessageReporting map[string]messageConfig `json:"extractorMessageReporting"`
}

type messageConfig struct {
	LogLevel           string `json:"logLevel"`
	AddToAPIReportFile bool   `json:"addToApiReportFile"`
}

// configFor is the configuration for one entry point in one tree.
func configFor(pkgDir string, e entry, reportDir, tempDir string) extractorConfig {
	return extractorConfig{
		ProjectFolder:          pkgDir,
		MainEntryPointFilePath: filepath.Join(pkgDir, filepath.FromSlash(e.dts)),
		Compiler:               compilerConfig{TsconfigFilePath: filepath.Join(pkgDir, "tsconfig.json")},
		APIReport: reportConfig{
			Enabled:          true,
			ReportFileName:   reportFileName,
			ReportFolder:     reportDir,
			ReportTempFolder: tempDir,
		},
		DocModel:      featureConfig{Enabled: false},
		DtsRollup:     featureConfig{Enabled: false},
		TSDocMetadata: featureConfig{Enabled: false},
		Messages: messagesConfig{ExtractorMessageReporting: map[string]messageConfig{
			"ae-missing-release-tag": {LogLevel: "warning", AddToAPIReportFile: false},
		}},
	}
}

// isolatedAmbientVars are the variables node and npm need to resolve their own
// state — a home directory, a package cache, a temporary directory — from this
// process's own environment. Nothing else in it reaches the comparison.
var isolatedAmbientVars = []string{"HOME", "npm_config_cache", "NODE_OPTIONS", "TMPDIR", "TMP", "TEMP", "USER", "LANG", "LC_ALL"}

// isolatedEnv is check plus whichever of the ambient variables above check does
// not already declare, and nothing else this process's own environment carries.
// check wins on a shared key rather than the two being concatenated and left to
// whichever libc's getenv() the child runs happens to prefer: a declared HOME is
// a deliberate choice the caller made, not a value this isolation should
// second-guess.
//
// The comparison installs and builds both trees, which runs the packages'
// lifecycle scripts and the trees' own compiler configuration — code the change
// under review controls, not lydite. review's own publish step runs with a
// credential that can write a commit status, and every other Run* in
// internal/executil layers its extra environment onto this process's own, which
// would hand that credential to a postinstall script the moment a component
// opts into api_surface. RunQuietIsolatedEnv is the one call that replaces the
// environment instead of extending it, for this reason.
func isolatedEnv(check []string) []string {
	declared := map[string]bool{}
	for _, kv := range check {
		if key, _, ok := strings.Cut(kv, "="); ok {
			declared[key] = true
		}
	}
	env := append([]string{}, check...)
	// PATH gets its own fallback rather than a place in isolatedAmbientVars:
	// toolchain.Compose adds no PATH entry at all when there are no directories
	// to add — an already-satisfied toolchain, or provisioning off — and a
	// child started with none can find neither npm nor node, which reports
	// every opted-in TypeScript component uncomputable rather than comparing
	// any of them.
	if !declared["PATH"] {
		if v, ok := os.LookupEnv("PATH"); ok {
			env = append(env, "PATH="+v)
		}
	}
	for _, key := range isolatedAmbientVars {
		if declared[key] {
			continue
		}
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// reasonLines caps how much of a tool's own account of a refusal travels into a
// caller's referral. Enough to name the cause, and not a whole npm install's
// progress.
const reasonLines = 10

// refusal is what the tool said about why it would not run.
//
// Both streams carry progress before anything went wrong, so the account starts
// at the first line the tool marked as an error and whatever ran fine above it
// is dropped. A stream marking none is kept from the end, where what it said
// last is.
func refusal(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	from := max(len(lines)-reasonLines, 0)
	for at, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "error") {
			from = at
			break
		}
	}
	lines = lines[from:]
	lines = lines[:min(len(lines), reasonLines)]
	return strings.Join(lines, "\n")
}
