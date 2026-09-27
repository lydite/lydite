package scanstages

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
)

// semgrepStage's own invocation is a seam because semgrep.Check has no way
// to prove what it was called with short of a real Semgrep install: its
// failure path (no pinned version on PATH) returns before ever building the
// argv that would carry the base. A fake check is what makes "Semgrep
// receives the diagnostics writer" and "Semgrep receives the base
// semgrepBase gives for the token flag" observable without one — the same
// injected-function shape gateLicences uses for its own language dispatch.
func TestSemgrepStagePassesTheResolvedBaseAndTheDiagnosticsWriter(t *testing.T) {
	cases := []struct {
		name              string
		appToken          bool
		baseSHA, wantBase string
	}{
		{"no token: the resolved base reaches Semgrep", false, "deadbeef", "deadbeef"},
		{"a token: semgrep ci scopes itself, so no base reaches it", true, "deadbeef", ""},
		{"no base to give", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			var gotDir, gotConfig, gotBase string
			var gotWriter io.Writer
			fake := func(_ context.Context, dir, rulesetConfig, baseSHA string, w io.Writer) executil.Result {
				gotDir, gotConfig, gotBase, gotWriter = dir, rulesetConfig, baseSHA, w
				return executil.Result{Name: semgrep.Gate}
			}
			out, err := semgrepStage(context.Background(), SemgrepIn{
				Dir:             "/repo",
				BaseSHA:         tc.baseSHA,
				SemgrepAppToken: tc.appToken,
				Config:          "auto",
				Diagnostics:     &diagnostics,
			}, fake)
			if err != nil {
				t.Fatalf("semgrepStage: %v", err)
			}
			if gotDir != "/repo" {
				t.Errorf("dir = %q, want the scan root", gotDir)
			}
			if gotConfig != "auto" {
				t.Errorf("config = %q, want the ruleset In carried", gotConfig)
			}
			if gotBase != tc.wantBase {
				t.Errorf("base = %q, want %q — semgrepBase(%q, %v)", gotBase, tc.wantBase, tc.baseSHA, tc.appToken)
			}
			if gotWriter != io.Writer(&diagnostics) {
				t.Errorf("writer = %v, want In.Diagnostics itself", gotWriter)
			}
			if out.Result.Name != semgrep.Gate {
				t.Errorf("Result = %+v, want the check's own return value carried through", out.Result)
			}
		})
	}
}

// The result, the located claims and the crashed bucket are all derived from
// the one Result the invocation returned — anchored and bucketed exactly as
// every language check's are, through anchoredFindings and crashesOf.
func TestSemgrepStageAnchorsFindingsAndReportsCrashes(t *testing.T) {
	fake := func(context.Context, string, string, string, io.Writer) executil.Result {
		return executil.Result{
			Name:    semgrep.Gate,
			Crashed: true,
			Findings: []finding.Finding{
				{Gate: semgrep.Gate, Path: "cli/main.go", Line: 5},
				{Gate: semgrep.Gate, Path: "cli/other.go", Line: 99},
			},
		}
	}
	out, err := semgrepStage(context.Background(), SemgrepIn{
		Dir:         "/repo",
		Diagnostics: &bytes.Buffer{},
		Changed:     map[string][]int{"cli/main.go": {5}},
	}, fake)
	if err != nil {
		t.Fatalf("semgrepStage: %v", err)
	}
	if len(out.Findings) != 2 {
		t.Fatalf("Findings = %+v, want both of Semgrep's claims", out.Findings)
	}
	for _, f := range out.Findings {
		if f.Component != "" {
			t.Errorf("finding %+v names a component; Semgrep is root-scoped and attributes nothing", f)
		}
		if f.Row != semgrep.Gate {
			t.Errorf("finding %+v names row %q, want the gate that made it", f, f.Row)
		}
	}
	if out.Findings[0].Anchor != finding.AnchorLine {
		t.Errorf("Findings[0].Anchor = %q, want %q — its line is one the change touched", out.Findings[0].Anchor, finding.AnchorLine)
	}
	if out.Findings[1].Anchor != finding.AnchorNowhere {
		t.Errorf("Findings[1].Anchor = %q, want %q — its path is not in Changed", out.Findings[1].Anchor, finding.AnchorNowhere)
	}
	if len(out.Crashes) != 1 || out.Crashes[0] != (finding.Crash{Gate: semgrep.Gate}) {
		t.Errorf("Crashes = %+v, want one crash naming the gate and no component", out.Crashes)
	}
}

// The exported Semgrep wires the real semgrep.Check, not only the fake the
// seam tests above use. PATH is stripped so neither semgrep nor pipx is
// found: ensure() fails immediately, with no network and no pipx install,
// after Check has already written its .semgrepignore warning to the
// diagnostics writer this stage was given — proving the default wiring
// reaches the same writer the seam tests observe directly.
func TestSemgrepPassesItsDiagnosticsWriterThroughToSemgrepCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	write(t, dir, ".semgrepignore", "docs/\n")

	var diagnostics bytes.Buffer
	out, err := Semgrep(context.Background(), SemgrepIn{Dir: dir, Config: "auto", Diagnostics: &diagnostics})
	if err != nil {
		t.Fatalf("Semgrep: %v", err)
	}
	if !strings.Contains(diagnostics.String(), ".semgrepignore") {
		t.Fatalf("diagnostics = %q, want the .semgrepignore warning semgrep.Check writes to the writer it was given", diagnostics.String())
	}
	if len(out.Crashes) != 1 || out.Crashes[0].Gate != semgrep.Gate {
		t.Errorf("Crashes = %+v, want Semgrep's own crash — it could not install", out.Crashes)
	}
}

// The secret gate's data half, ported from the command test of the same
// name: a tree carrying a credential fails the row and locates the leak as a
// claim naming no component, with the matched text never reaching Site.
//
// Real gitleaks, as the command test it is ported from already relies on:
// no test double exists for it, and a stub would only prove lydite's own
// wiring rather than what gitleaks actually reports.
func TestSecretsIsRootScopedAndCountsNoComponent(t *testing.T) {
	// Invented, and assembled from halves: generic-api-key wants a
	// high-entropy value beside a keyword, which is a shape this file must
	// not itself carry.
	invented := "8a7b6c5d4e3f2a1b0c9d" + "8e7f6a5b4c3d2e1f0a9b"
	dir := t.TempDir()
	write(t, dir, "config.yml", "aws_key: \""+invented+"\"\n")

	out, err := Secrets(context.Background(), SecretsIn{Dir: dir})
	if err != nil {
		t.Fatalf("Secrets: %v", err)
	}
	if out.Result.Ok() {
		t.Fatalf("Result = %+v, want a failing row over a tree holding a credential", out.Result)
	}
	if len(out.Crashes) != 1 || out.Crashes[0] != (finding.Crash{Gate: secrets.Gate}) {
		t.Errorf("Crashes = %+v, want one crash naming the gate and no component — dir is not a git repository, so the claims are not scoped", out.Crashes)
	}
	var claims int
	for _, f := range out.Findings {
		if f.Gate != secrets.Gate {
			continue
		}
		claims++
		if f.Component != "" {
			t.Errorf("claim %+v names a component; gitleaks is root-scoped and attributes nothing", f)
		}
		if f.Row != secrets.Gate {
			t.Errorf("claim %+v names row %q, want the gate that made it", f, f.Row)
		}
		if strings.Contains(f.Site, invented) {
			t.Errorf("the site carries the credential: %q", f.Site)
		}
	}
	if claims == 0 {
		t.Errorf("Findings = %+v, want the leak as a located claim", out.Findings)
	}
}
