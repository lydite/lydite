package scanstages

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
)

// The name and never the directory: unique names are enforced and unique
// directories are not, and a scan row and a test row about one component have
// to carry the same token.
func TestLabelledNamesTheComponent(t *testing.T) {
	got := labelled([]executil.Result{{Name: "gosec"}, {Name: "govulncheck"}}, "api", "services/api")
	if len(got) != 2 || got[0].Name != "gosec(api)" || got[1].Name != "govulncheck(api)" {
		t.Fatalf("labelled = %+v, want each result named for the component", got)
	}
}

// A component's checks report paths relative to the component, and every
// other producer names a file from the scan root: one file named from two
// roots is two claims, and only one of them can be anchored.
func TestAComponentSFindingsAreRebasedOntoTheScanRoot(t *testing.T) {
	got := labelled([]executil.Result{{
		Name:     "biome",
		Findings: []finding.Finding{{Gate: "biome", Path: "src/a.ts", Line: 3}},
	}}, "web", "source/web")
	if got[0].Findings[0].Path != "source/web/src/a.ts" {
		t.Errorf("the path was not rebased: %q", got[0].Findings[0].Path)
	}
	if got[0].Findings[0].Component != "web" {
		t.Errorf("the claim does not name its component: %q", got[0].Findings[0].Component)
	}
}

// A scanner runs inside its component and reports paths relative to there,
// while every other producer names a file from the scan root. One file named
// from two roots is two claims, and only one of them can be anchored.
func TestAScannersClaimIsRebasedOntoTheScanRoot(t *testing.T) {
	t.Parallel()
	got := labelled([]executil.Result{{
		Name: "biome",
		Findings: []finding.Finding{
			{Gate: "biome", Path: "src/app.ts", Line: 12, Site: "lint/security/noGlobalEval\x1feval(x)"},
		},
	}}, "web", "apps/web")

	if len(got) != 1 || len(got[0].Findings) != 1 {
		t.Fatalf("the claim did not survive labelling: %+v", got)
	}
	f := got[0].Findings[0]
	if f.Path != "apps/web/src/app.ts" {
		t.Errorf("path is %q, want it rebased onto the scan root", f.Path)
	}
	if f.Component != "web" {
		t.Errorf("component is %q, want web", f.Component)
	}
}

// Labelling copies rather than writing through the caller's slice, so a result
// handed to it is not altered underneath whoever still holds it.
func TestLabellingDoesNotAlterTheResultItWasGiven(t *testing.T) {
	t.Parallel()
	original := []executil.Result{{
		Name:     "biome",
		Findings: []finding.Finding{{Gate: "biome", Path: "src/app.ts", Line: 12}},
	}}
	_ = labelled(original, "web", "apps/web")

	if got := original[0].Findings[0].Path; got != "src/app.ts" {
		t.Errorf("the caller's own finding was rewritten to %q", got)
	}
	if original[0].Name != "biome" {
		t.Errorf("the caller's own result was renamed to %q", original[0].Name)
	}
}

// A check's located claims name the row that made them, which is what lets
// the standing comment tell the claims it keeps from the ones a thread
// carries.
func TestACheckSFindingsNameTheirRow(t *testing.T) {
	found := findingsOf([]executil.Result{{
		Name: "biome(cli)", Err: errors.New("failed"),
		Findings: []finding.Finding{{Gate: "biome", Component: "cli", Path: "a.ts", Line: 3,
			Message: "a finding", Site: "one", Anchor: finding.AnchorLine}},
	}})
	if len(found) != 1 {
		t.Fatalf("the check's claim went missing: %+v", found)
	}
	if found[0].Row != "biome(cli)" {
		t.Errorf("the claim does not name the row that made it: %q", found[0].Row)
	}
}

// A scanner's claim on a line the change touched is a review thread on that
// line. Without an anchor every claim lydite makes about a repository's
// security lands in the standing comment, whatever the change did.
func TestAScanSClaimOnAChangedLineIsAnchoredToIt(t *testing.T) {
	changed := map[string][]int{"source/cli/a.go": {3, 4}}
	found := anchoredFindings(labelled([]executil.Result{{
		Name: "gosec", Err: errors.New("failed"),
		Findings: []finding.Finding{
			{Gate: "gosec", Path: "a.go", Line: 3, Message: "on a changed line", Site: "one"},
			{Gate: "gosec", Path: "a.go", Line: 40, Message: "elsewhere in a changed file", Site: "two"},
			{Gate: "gosec", Path: "b.go", Line: 1, Message: "in a file the change never touched", Site: "three"},
		},
	}}, "cli", "source/cli"), changed)

	got := map[string]finding.Anchor{}
	for _, f := range found {
		got[f.Message] = f.Anchor
	}
	want := map[string]finding.Anchor{
		"on a changed line":                  finding.AnchorLine,
		"elsewhere in a changed file":        finding.AnchorFile,
		"in a file the change never touched": finding.AnchorNowhere,
	}
	for message, wantAnchor := range want {
		if got[message] != wantAnchor {
			t.Errorf("%q anchored %q, want %q", message, got[message], wantAnchor)
		}
	}
}

// A scan with no --diff-base reaches no change at all, so every claim it makes
// belongs in the standing comment rather than on a line of somebody's pull
// request. It is the shape `lydite-baseline.yml` runs on main.
func TestAScanOverAWholeRepositoryAnchorsNothing(t *testing.T) {
	found := anchoredFindings([]executil.Result{{
		Name: "gosec(cli)", Err: errors.New("failed"),
		Findings: []finding.Finding{{Gate: "gosec", Path: "a.go", Line: 3, Message: "a claim", Site: "one"}},
	}}, nil)
	if len(found) != 1 {
		t.Fatalf("the check's claim went missing: %+v", found)
	}
	for _, f := range found {
		if f.Anchor != finding.AnchorNowhere {
			t.Errorf("anchor = %q, want the claim unanchorable", f.Anchor)
		}
	}
}

// A crash is named by the gate its findings carry and the component they are
// bucketed under — never by the row's label, which is prose — and only for a
// result that says it crashed: a failing one that parsed is what found
// something.
func TestCrashesOfNamesTheBareGateAndTheComponent(t *testing.T) {
	results := []executil.Result{
		{Name: "gosec", Crashed: true, Err: errors.New("did not compile")},
		{Name: "govulncheck", Err: errors.New("exit status 3")},
	}
	got := crashesOf(results, "api")
	want := []finding.Crash{{Gate: "gosec", Component: "api"}}
	if !slices.Equal(got, want) {
		t.Errorf("crashes = %+v, want %+v", got, want)
	}
	if root := crashesOf([]executil.Result{{Name: "gitleaks", Crashed: true}}, ""); !slices.Equal(root, []finding.Crash{{Gate: "gitleaks"}}) {
		t.Errorf("root crashes = %+v, want gitleaks naming no component", root)
	}
}

// The names reported are the ones the child actually reads, so the two cases
// where the composition disagrees with the declaration are pinned here: a
// declared PATH is folded into lydite's own entry rather than set, and a key
// the resolved toolchain also sets is cancelled by composing last. A component
// that composed nothing says nothing at all — a warning for every component is
// a warning nobody reads.
func TestDeclaredEnvNamesWhatWasComposed(t *testing.T) {
	cases := []struct {
		name     string
		c        component.Component
		composed []string
		want     []string
	}{
		{
			name:     "no declaration at all",
			c:        component.Component{Name: "cli"},
			composed: []string{"PATH=/usr/bin"},
		},
		{
			name:     "an empty declaration is no declaration",
			c:        component.Component{Name: "cli", Env: map[string]string{}},
			composed: []string{"PATH=/usr/bin"},
		},
		{
			name:     "a declared variable is named",
			c:        component.Component{Name: "cli", Env: map[string]string{"SQLX_OFFLINE": "true", "CGO_ENABLED": "0"}},
			composed: []string{"CGO_ENABLED=0", "SQLX_OFFLINE=true"},
			want:     []string{"CGO_ENABLED", "SQLX_OFFLINE"},
		},
		{
			name:     "an empty value is still a declaration",
			c:        component.Component{Name: "cli", Env: map[string]string{"SQLX_OFFLINE": ""}},
			composed: []string{"SQLX_OFFLINE="},
			want:     []string{"SQLX_OFFLINE"},
		},
		{
			name:     "a declared PATH is the extension it is",
			c:        component.Component{Name: "cli", Env: map[string]string{"PATH": "ci-bin"}},
			composed: []string{"PATH=/usr/bin" + string(os.PathListSeparator) + "ci-bin"},
			want:     []string{"PATH (appended after lydite's own)"},
		},
		{
			name:     "a key the toolchain composes last never reached the check",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOTOOLCHAIN": "auto"}},
			composed: []string{"GOTOOLCHAIN=auto", "GOTOOLCHAIN=local"},
			want:     []string{"GOTOOLCHAIN (overridden by the resolved toolchain)"},
		},
		{
			name:     "a known steering variable is marked",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOVULNDB": "https://db.example"}},
			composed: []string{"GOVULNDB=https://db.example"},
			want:     []string{"GOVULNDB (steers a check)"},
		},
		{
			name:     "an ordinary variable that merely resembles a steering name is not marked",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOFLAG": "not-a-steering-name"}},
			composed: []string{"GOFLAG=not-a-steering-name"},
			want:     []string{"GOFLAG"},
		},
		{
			name:     "a steering variable the toolchain overrides carries both marks",
			c:        component.Component{Name: "cli", Env: map[string]string{"GOFLAGS": "-tags x"}},
			composed: []string{"GOFLAGS=-tags x", "GOFLAGS=-tags y"},
			want:     []string{"GOFLAGS (steers a check, overridden by the resolved toolchain)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, vars := testEnvironment{}.Declared(tc.c)
			if got := declaredEnvNames(dirs, vars, tc.composed); !slices.Equal(got, tc.want) {
				t.Errorf("declaredEnvNames = %q, want %q", got, tc.want)
			}
			var buf bytes.Buffer
			warnDeclaredEnv(&buf, tc.c.Name, dirs, vars, tc.composed)
			if (buf.Len() == 0) != (len(tc.want) == 0) {
				t.Errorf("warnDeclaredEnv wrote %q for %d composed name(s)", buf.String(), len(tc.want))
			}
		})
	}
}

// Names, never values. A declared value is arbitrary text the repository
// controls, and this line goes to a CI log that is world-readable on a public
// repository — so the value of a variable, and the directories of a declared
// PATH, must never appear however the names are assembled.
func TestDeclaredEnvValuesNeverReachTheWarning(t *testing.T) {
	const secret = "ghp_examplesecretvaluenobodyshouldsee"
	c := component.Component{Name: "cli", Env: map[string]string{
		"NPM_TOKEN":    secret,
		"DATABASE_URL": "postgres://user:" + secret + "@db/app",
		"PATH":         "/opt/" + secret + "/bin",
	}}
	dirs, vars := testEnvironment{}.Declared(c)
	var buf bytes.Buffer
	warnDeclaredEnv(&buf, c.Name, dirs, vars, testEnvironment{}.Compose(&toolchain.Env{}, c))
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("a declared value reached the warning:\n%s", buf.String())
	}
	for _, want := range []string{"NPM_TOKEN", "DATABASE_URL", "PATH (appended after lydite's own)"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("warning is missing %q:\n%s", want, buf.String())
		}
	}
}

// Only a language a check is written for is ever handed one. Any other planned
// for scanning is refused before a single check runs, so a plan that names a
// language nothing checks cannot pass for a clean scan — nor leave some
// components' checks half-run ahead of the error.
func TestRunChecksRefusesALanguageItHasNoChecksFor(t *testing.T) {
	plan := []Planned{
		{Component: component.Component{Name: "cli", Env: map[string]string{"LYDITE_MARK": "1"}}, Lang: runner.Go,
			Disposition: Scan, Dir: t.TempDir(), Env: executil.Env{Check: []string{"LYDITE_MARK=1"}}},
		{Component: component.Component{Name: "tools"}, Lang: runner.Python, Disposition: Scan, Dir: t.TempDir()},
	}
	var diagnostics bytes.Buffer
	_, err := RunChecks(context.Background(), RunChecksIn{Plan: plan, Environment: testEnvironment{}, Diagnostics: &diagnostics})
	if err == nil {
		t.Fatal("RunChecks accepted a planned language it has no checks for")
	}
	if !strings.Contains(err.Error(), "tools") || !strings.Contains(err.Error(), string(runner.Python)) {
		t.Errorf("error = %v, want the component and its language named", err)
	}
	if diagnostics.Len() != 0 {
		t.Errorf("diagnostics = %q, want nothing: the refusal comes before any component's checks", diagnostics.String())
	}
	for _, lang := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript, runner.Shell} {
		if _, err := checksFor(lang); err != nil {
			t.Errorf("checksFor(%s): %v, want every language with a scanner handled", lang, err)
		}
	}
}

// An entry nothing checks answers the zero value at its own index, so the
// caller walking the plan never pairs one component with another's checks.
func TestRunChecksAnswersEveryEntryThatIsNotAScanWithNothing(t *testing.T) {
	plan := []Planned{
		{Component: component.Component{Name: "legacy"}, Disposition: Unscanned},
		{Component: component.Component{Name: "scripts", Env: map[string]string{"LYDITE_MARK": "1"}}, Lang: runner.Shell, Disposition: Disabled},
		{Component: component.Component{Name: "api-integration"}, Lang: runner.Go, Disposition: Duplicate, DuplicateOf: "api"},
	}
	var diagnostics bytes.Buffer
	out, err := RunChecks(context.Background(), RunChecksIn{Plan: plan, Environment: testEnvironment{}, Diagnostics: &diagnostics})
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if len(out.Checks) != len(plan) {
		t.Fatalf("checks = %d entries for a plan of %d, want one per entry", len(out.Checks), len(plan))
	}
	for i, c := range out.Checks {
		if c.Results != nil || c.Findings != nil || c.Crashes != nil {
			t.Errorf("entry %d (%s) = %+v, want nothing for an entry nothing checks", i, plan[i].Component.Name, c)
		}
	}
	if diagnostics.Len() != 0 {
		t.Errorf("diagnostics = %q, want no warning for a component whose checks never run", diagnostics.String())
	}
}

// goModule writes a dependency-free Go module under root/rel.
func goModule(t *testing.T, root, rel string) string {
	t.Helper()
	write(t, root, rel+"/go.mod", "module "+filepath.Base(rel)+"\n\ngo 1.26\n")
	write(t, root, rel+"/main.go", "package main\n\nfunc main() {}\n")
	return filepath.Join(root, filepath.FromSlash(rel))
}

// The declared-environment warning reaches the diagnostics writer before that
// component's checks stream a byte, and each component's checks land at that
// component's own index, whatever sits between them in the plan.
//
// The scanners stream through executil's process-wide target, pointed here at
// the same buffer as the diagnostics, so the one buffer holds both in the
// order they happened. This test runs the real gosec and govulncheck, and
// does not run in parallel with anything that streams.
func TestRunChecksWarnsBeforeEachComponentsChecksAndAnswersAtItsIndex(t *testing.T) {
	root := t.TempDir()
	aDir := goModule(t, root, "a")
	bDir := goModule(t, root, "b")
	a := component.Component{Name: "a", Dir: "a", Runner: runner.GoTest, Env: map[string]string{"LYDITE_A": "secretvalue-a"}}
	b := component.Component{Name: "b", Dir: "b", Runner: runner.GoTest, Env: map[string]string{"LYDITE_B": "secretvalue-b"}}
	plan := []Planned{
		{Component: a, Lang: runner.Go, Disposition: Scan, Dir: aDir,
			Env: executil.Env{Check: testEnvironment{}.Compose(nil, a)}, ToolchainKey: (*toolchain.Env)(nil).Key()},
		{Component: component.Component{Name: "legacy", Dir: "legacy"}, Disposition: Unscanned},
		{Component: b, Lang: runner.Go, Disposition: Scan, Dir: bDir,
			Env: executil.Env{Check: testEnvironment{}.Compose(nil, b)}, ToolchainKey: (*toolchain.Env)(nil).Key()},
		{Component: component.Component{Name: "a-again", Dir: "a"}, Lang: runner.Go, Disposition: Duplicate, DuplicateOf: "a"},
	}

	var stream bytes.Buffer
	executil.StreamTo(&stream)
	t.Cleanup(func() { executil.StreamTo(os.Stdout) })

	out, err := RunChecks(context.Background(), RunChecksIn{Plan: plan, Environment: testEnvironment{}, Diagnostics: &stream})
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}

	text := stream.String()
	warnA := "warning: a's checks are composed with the environment " + component.FileName + " declares: LYDITE_A"
	warnB := "warning: b's checks are composed with the environment " + component.FileName + " declares: LYDITE_B"
	if !strings.HasPrefix(text, warnA) {
		t.Fatalf("stream begins %q, want a's warning ahead of anything its checks printed", firstLine(text))
	}
	endA := strings.Index(text, "\n") + 1
	atB := strings.Index(text, warnB)
	if atB < 0 {
		t.Fatalf("stream carries no warning for b:\n%s", text)
	}
	if strings.TrimSpace(text[endA:atB]) == "" {
		t.Errorf("nothing streamed between a's warning and b's, want a's checks to have run there:\n%s", text)
	}
	endB := atB + strings.Index(text[atB:], "\n") + 1
	if strings.TrimSpace(text[endB:]) == "" {
		t.Errorf("nothing streamed after b's warning, want b's checks to have run after it:\n%s", text)
	}
	if strings.Contains(text, "secretvalue") {
		t.Errorf("a declared value reached the stream:\n%s", text)
	}

	if len(out.Checks) != len(plan) {
		t.Fatalf("checks = %d entries for a plan of %d, want one per entry", len(out.Checks), len(plan))
	}
	for i, want := range [][]string{
		{"gosec(a)", "govulncheck(a)"},
		nil,
		{"gosec(b)", "govulncheck(b)"},
		nil,
	} {
		var names []string
		for _, r := range out.Checks[i].Results {
			names = append(names, r.Name)
		}
		if !slices.Equal(names, want) {
			t.Errorf("entry %d (%s) ran %v, want %v", i, plan[i].Component.Name, names, want)
		}
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
