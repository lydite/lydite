package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/semgrep"
	"lydite/lydite/internal/ui"
)

// The "auto" path needs a real repo with an origin/main to resolve against, so
// it's left to the integration surface; what's worth pinning here is the two
// branches that decide whether git is consulted at all.
func TestResolveDiffBase(t *testing.T) {
	cases := []struct {
		name     string
		diffBase string
		appToken string
		want     string
	}{
		{"unset means scan everything", "", "", ""},
		{"literal ref passes through", "origin/release", "", "origin/release"},
		{"a token short-circuits auto — semgrep ci scopes itself", "auto", "tok", ""},
		{"a token short-circuits a literal ref too", "origin/release", "tok", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(semgrep.AppTokenEnv, tc.appToken)
			got, err := resolveDiffBase(context.Background(), t.TempDir(), tc.diffBase, "")
			if err != nil {
				t.Fatalf("resolveDiffBase(%q) returned %v", tc.diffBase, err)
			}
			if got != tc.want {
				t.Errorf("resolveDiffBase(%q) = %q, want %q", tc.diffBase, got, tc.want)
			}
		})
	}
}

// A failing check whose findings never streamed must print them. Biome sends
// its report to a file so the JSON cannot be corrupted by its own chatter,
// which means nothing reaches the terminal on its own — printing a bare
// status line left the developer to re-run the pinned toolchain by hand to
// find out what was wrong, and put nothing in the PR comment either.
func TestReportPrintsDetailForFailingChecks(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := report(cmd, ui.NewReport("scan"), []executil.Result{
		{
			Name:   "biome(.)",
			Detail: "src/bad.ts:1  lint/security/noGlobalEval  eval() is dangerous\nsrc/bad.ts:4  lint/correctness/noUnusedVariables  unused",
			Err:    errors.New("2 finding(s)"),
		},
	}, false, true)
	if err == nil {
		t.Fatal("a failing check must still return an error")
	}
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("a failing gate must exit 1, got %v", err)
	}
	for _, want := range []string{"✗ biome(.)", "noGlobalEval", "eval() is dangerous", "noUnusedVariables"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report output missing %q:\n%s", want, out.String())
		}
	}
}

// The JSON report is what anything automated reads, so it must carry the
// same verdict and the same findings as the text — a machine surface that
// can disagree with the human one is worse than no machine surface.
func TestReportJSONCarriesVerdictAndDetail(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "src/bad.ts:1  noGlobalEval", Err: errors.New("1 finding(s)")},
	}, true, false)
	var got struct {
		Command string `json:"command"`
		Verdict string `json:"verdict"`
		Exit    int    `json:"exit"`
		Rows    []struct {
			Status string   `json:"status"`
			Label  string   `json:"label"`
			Detail []string `json:"detail"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("the machine report must be valid JSON: %v\n%s", err, out.String())
	}
	if got.Command != "scan" || got.Verdict != "fail" || got.Exit != 1 {
		t.Errorf("got command=%q verdict=%q exit=%d, want scan/fail/1", got.Command, got.Verdict, got.Exit)
	}
	if len(got.Rows) != 1 || got.Rows[0].Status != "fail" || got.Rows[0].Label != "biome(.)" {
		t.Fatalf("unexpected rows: %+v", got.Rows)
	}
	if len(got.Rows[0].Detail) != 1 || !strings.Contains(got.Rows[0].Detail[0], "noGlobalEval") {
		t.Errorf("the finding must survive into JSON, got %+v", got.Rows[0].Detail)
	}
}

// A finding's own message is attacker-adjacent text: it can contain anything
// the scanned source contains, including something shaped like a verdict. A
// detail line is indented so it can never begin a line the way a status row
// does, which is what stops a finding from forging one.
func TestReportDetailCannotForgeAStatusLine(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "✓ biome(.) ... passed", Err: errors.New("1 finding(s)")},
	}, false, true)
	statusLines := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "✓ ") || strings.HasPrefix(line, "✗ ") {
			statusLines++
		}
	}
	if statusLines != 1 {
		t.Errorf("got %d unindented status lines, want exactly 1 — a finding forged one:\n%s", statusLines, out.String())
	}
}

// Passing checks print no detail, and a tool that streamed its own output is
// not reprinted: doing either would duplicate the log or bury the summary.
func TestReportPrintsNoDetailForPassingOrStreamingChecks(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	_ = report(cmd, ui.NewReport("scan"), []executil.Result{
		{Name: "biome(.)", Detail: "should not appear"},
		{Name: "semgrep", Output: "already streamed to the terminal", Err: errors.New("findings")},
	}, false, true)
	for _, unwanted := range []string{"should not appear", "already streamed"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("report printed %q:\n%s", unwanted, out.String())
		}
	}
}
