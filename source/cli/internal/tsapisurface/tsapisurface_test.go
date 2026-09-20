package tsapisurface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A Result nobody filled in says the surface was not compared, and never that
// it is clean.
func TestTheZeroResultIsUnmeasurable(t *testing.T) {
	if (Result{}).Outcome != Unmeasurable {
		t.Errorf("the zero Result reports %v, want Unmeasurable", (Result{}).Outcome)
	}
}

// A lockfile is what makes `npm ci` possible, and ci is the stronger install:
// it installs precisely what was resolved and refuses a lockfile that disagrees
// with its package.json.
func TestTheInstallIsCiOnlyWhereALockfileResolvedTheTree(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, ".", `{"name":"@probe/one"}`)
	if got := steps(dir)[0].args[0]; got != "install" {
		t.Errorf("a tree with no lockfile installs with %q, want install", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing the lockfile: %v", err)
	}
	if got := steps(dir)[0].args[0]; got != "ci" {
		t.Errorf("a tree with a lockfile installs with %q, want ci", got)
	}
}

// A workspace root's own build script builds nothing its members declare, so
// the build has to reach every member and the root both.
func TestTheBuildReachesEveryWorkspaceMember(t *testing.T) {
	one := t.TempDir()
	writeManifest(t, one, ".", `{"name":"@probe/one"}`)
	if got := steps(one)[1].args; !slices.Equal(got, []string{"run", "build", "--if-present"}) {
		t.Errorf("a single package builds with %v", got)
	}

	root := t.TempDir()
	writeManifest(t, root, ".", `{"name":"@probe/root","workspaces":["packages/*"]}`)
	got := steps(root)[1].args
	for _, want := range []string{"--workspaces", "--include-workspace-root"} {
		if !slices.Contains(got, want) {
			t.Errorf("a workspace root builds with %v, which does not carry %q", got, want)
		}
	}
}

// Both steps have to succeed before a tree can be read, and each says which one
// did not: `tsc` writes a complete dist/ for a tree that does not typecheck, so
// nothing downstream can recover either failure from what is on disk.
func TestBothTheInstallAndTheBuildHaveToSucceed(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, ".", `{"name":"@probe/one"}`)
	got := steps(dir)
	if len(got) != 2 {
		t.Fatalf("got %d step(s), want the install and the build", len(got))
	}
	if !strings.Contains(got[0].failure, "node_modules") {
		t.Errorf("the install's failure does not say what a partial node_modules costs: %q", got[0].failure)
	}
	if !strings.Contains(got[1].failure, "declarations") {
		t.Errorf("the build's failure does not say what is missing: %q", got[1].failure)
	}
}

// The configuration api-extractor is handed produces the API report and nothing
// else: the doc model and the rollup would cost a second analysis of the same
// program and neither is compared.
func TestTheConfigurationProducesTheReportAndNothingElse(t *testing.T) {
	config := configFor("/tree/packages/core", entry{subpath: ".", dts: "dist/index.d.ts"}, "/out/report", "/out/temp")
	if !config.APIReport.Enabled {
		t.Error("the API report is off")
	}
	for what, enabled := range map[string]bool{
		"the doc model": config.DocModel.Enabled,
		"the rollup":    config.DtsRollup.Enabled,
		"the metadata":  config.TSDocMetadata.Enabled,
	} {
		if enabled {
			t.Errorf("%s is on", what)
		}
	}
	if config.MainEntryPointFilePath != filepath.Join("/tree/packages/core", "dist/index.d.ts") {
		t.Errorf("entry point = %q", config.MainEntryPointFilePath)
	}
	if config.ProjectFolder != "/tree/packages/core" {
		t.Errorf("project folder = %q", config.ProjectFolder)
	}
	if config.APIReport.ReportFolder != "/out/report" || config.APIReport.ReportTempFolder != "/out/temp" {
		t.Errorf("report folders = %q and %q", config.APIReport.ReportFolder, config.APIReport.ReportTempFolder)
	}
}

// Every path in the configuration is absolute, so where lydite put the
// configuration file itself decides nothing about what is analysed.
func TestEveryPathInTheConfigurationIsAbsolute(t *testing.T) {
	config := configFor("/tree", entry{subpath: ".", dts: "dist/index.d.ts"}, "/out/report", "/out/temp")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encoding the configuration: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("decoding the configuration: %v", err)
	}
	for _, path := range []string{
		config.ProjectFolder,
		config.MainEntryPointFilePath,
		config.Compiler.TsconfigFilePath,
		config.APIReport.ReportFolder,
		config.APIReport.ReportTempFolder,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%q is relative", path)
		}
	}
	if _, reported := fields["messages"]; !reported {
		t.Error("the configuration says nothing about which messages are reported")
	}
}

// ae-missing-release-tag fires once per exported declaration in every package
// that does not use release tags, and a warning is not a change to the surface.
func TestAMissingReleaseTagIsAWarningAndNotAReportLine(t *testing.T) {
	config := configFor("/tree", entry{subpath: ".", dts: "dist/index.d.ts"}, "/out/report", "/out/temp")
	message, reported := config.Messages.ExtractorMessageReporting["ae-missing-release-tag"]
	if !reported {
		t.Fatal("ae-missing-release-tag is left at api-extractor's own default")
	}
	if message.LogLevel != "warning" {
		t.Errorf("log level = %q, want warning", message.LogLevel)
	}
	if message.AddToAPIReportFile {
		t.Error("the warning is added to the report, where it would read as a change to the surface")
	}
}

// An entry point one tree does not name has no report there and is no failure:
// the other tree named it, and every declaration under it is an addition or a
// removal accordingly.
func TestAnUnnamedEntryPointReadsAsAnEmptyReport(t *testing.T) {
	report, reason := read(t.Context(), "/nonexistent/api-extractor", nil, t.TempDir(), ".", entry{subpath: "./gone"})
	if report != "" || reason != "" {
		t.Errorf("got report %q and reason %q, want neither", report, reason)
	}
}

// The environment the comparison runs under is what was declared plus the few
// ambient variables node and npm resolve their own state from, and nothing else
// this process holds.
func TestIsolatedEnvCarriesOnlyCheckAndTheAllowedAmbientVars(t *testing.T) {
	t.Setenv("HOME", "/home/probe")
	t.Setenv("LYDITE_SECRET_TOKEN", "must not reach the child")

	env := isolatedEnv([]string{"SQLX_OFFLINE=true"})
	if !slices.Contains(env, "SQLX_OFFLINE=true") {
		t.Errorf("the declared environment is missing from %v", env)
	}
	if !slices.Contains(env, "HOME=/home/probe") {
		t.Errorf("HOME is missing from %v", env)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "LYDITE_SECRET_TOKEN=") {
			t.Errorf("this process's own %q reached the child", kv)
		}
	}
}

// A declared ambient key is the caller's deliberate choice, not a value this
// isolation should second-guess.
func TestIsolatedEnvDoesNotOverrideADeclaredAmbientKey(t *testing.T) {
	t.Setenv("HOME", "/home/ambient")
	env := isolatedEnv([]string{"HOME=/home/declared"})
	if slices.Contains(env, "HOME=/home/ambient") {
		t.Errorf("the ambient HOME was appended beside the declared one: %v", env)
	}
	if !slices.Contains(env, "HOME=/home/declared") {
		t.Errorf("the declared HOME is missing from %v", env)
	}
}

// A child started with no PATH finds neither npm nor node, which reports every
// opted-in component uncomputable rather than comparing any of them.
func TestIsolatedEnvFallsBackToTheAmbientPathWhenCheckDeclaresNone(t *testing.T) {
	t.Setenv("PATH", "/probe/bin")
	if env := isolatedEnv(nil); !slices.Contains(env, "PATH=/probe/bin") {
		t.Errorf("PATH is missing from %v", env)
	}
	if env := isolatedEnv([]string{"PATH=/declared/bin"}); slices.Contains(env, "PATH=/probe/bin") {
		t.Errorf("the ambient PATH was appended beside the declared one: %v", env)
	}
}

// A refusal starts where the tool said something went wrong, so an npm install's
// progress above it does not fill a caller's referral.
func TestARefusalStartsAtTheFirstErrorLine(t *testing.T) {
	output := "api-extractor 7.59.1\nAnalysis will use the bundled TypeScript version 5.9.3\n" +
		"ERROR: Error parsing api-extractor.json:\nThe \"mainEntryPointFilePath\" path does not exist\n"
	got := refusal(output)
	if !strings.HasPrefix(got, "ERROR: Error parsing") {
		t.Errorf("refusal = %q, want it to start at the ERROR line", got)
	}
	if strings.Contains(got, "bundled TypeScript") {
		t.Errorf("refusal carries the progress above the error: %q", got)
	}
}

// A stream marking no error is kept from the end, where what it said last is.
func TestARefusalWithNoErrorLineKeepsTheEnd(t *testing.T) {
	var lines []string
	for at := range reasonLines * 2 {
		lines = append(lines, "line "+strconv.Itoa(at))
	}
	got := strings.Split(refusal(strings.Join(lines, "\n")), "\n")
	if len(got) != reasonLines {
		t.Fatalf("got %d line(s), want %d", len(got), reasonLines)
	}
	if got[len(got)-1] != lines[len(lines)-1] {
		t.Errorf("the last line is %q, want %q", got[len(got)-1], lines[len(lines)-1])
	}
}

// The cap holds at exactly the boundary, from either end.
func TestARefusalHoldsTheBoundaryAtExactlyReasonLines(t *testing.T) {
	var lines []string
	lines = append(lines, "error: the cause")
	for at := range reasonLines * 2 {
		lines = append(lines, "line "+strconv.Itoa(at))
	}
	if got := strings.Count(refusal(strings.Join(lines, "\n")), "\n") + 1; got != reasonLines {
		t.Errorf("got %d line(s), want %d", got, reasonLines)
	}
}

// writeManifest puts one package.json in a tree, for a case that needs the two
// trees to disagree about what a package declares.
func writeManifest(t *testing.T, tree, rel, text string) {
	t.Helper()
	dir := filepath.Join(tree, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(text), 0o600); err != nil {
		t.Fatalf("writing %s: %v", dir, err)
	}
}
