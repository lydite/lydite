package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// reviewRepo commits baseFiles as the base revision, then headFiles on top,
// and returns the repo directory and the base SHA.
func reviewRepo(t *testing.T, baseFiles, headFiles map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write(baseFiles)
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write(headFiles)
	run("add", "-A")
	run("commit", "-m", "head")
	return dir, base
}

func runReview(t *testing.T, dir, base string, extra ...string) (string, error) {
	t.Helper()
	cmd := newReviewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"--dir", dir, "--base", base, "--no-color"}, extra...))
	err := cmd.Execute()
	return out.String(), err
}

// The exemption set is read from the base commit, never from the branch. A
// change that widens the gate must get no benefit from its own widening —
// otherwise one pull request can declare itself exempt, which is the entire
// attack this ordering removes.
func TestReviewReadsExemptionsFromTheBaseNotTheBranch(t *testing.T) {
	selfServing := "exemptions:\n  - name: anything\n    reason: it is fine, trust me\n    paths: [\"**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{referral.FileName: selfServing, "src/auth.go": "package src"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a branch that exempts itself must still be referred:\n%s", out)
	}
	var exit ui.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("a referral must exit 2, got %v", err)
	}
	if !strings.Contains(out, "review referred in") {
		t.Errorf("expected a referral verdict, got:\n%s", out)
	}
}

// With the exemption in place at the base, the same shape of change goes
// through unattended.
func TestReviewPassesWhenTheBaseDeclaresAMatchingExemption(t *testing.T) {
	exemptions := "exemptions:\n  - name: readme-only\n    reason: prose changes nothing executable\n    paths: [\"README.md\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err != nil {
		t.Fatalf("expected an unattended pass, got %v:\n%s", err, out)
	}
	if !strings.Contains(out, "exempt: readme-only") {
		t.Errorf("a pass must name the declaration that allowed it, got:\n%s", out)
	}
}

// Day one: no file at all. Every change is referred, and the report says why
// rather than leaving the reader to guess that the feature is broken.
func TestReviewWithNoExemptionsFileRefersAndSaysSo(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("with nothing declared, everything is referred:\n%s", out)
	}
	if !strings.Contains(out, "no exemptions declared") {
		t.Errorf("expected the report to name the empty set, got:\n%s", out)
	}
}

// A disqualifier vetoes a match, and the report distinguishes that from
// "nothing matched" — the two have different remedies, and only one of them
// is a remedy the author can apply.
func TestReviewDisqualifierVetoesAMatchAndNamesTheEvidence(t *testing.T) {
	exemptions := "exemptions:\n  - name: go-source\n    reason: ordinary source edits\n    paths: [\"src/**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{referral.FileName: exemptions, "src/a.go": "package src\n"},
		map[string]string{"src/a.go": "package src\n\nvar x = eval() // #nosec G204\n"},
	)

	out, err := runReview(t, dir, base)
	if err == nil {
		t.Fatalf("a net-new suppression must veto the match:\n%s", out)
	}
	if !strings.Contains(out, "suppression added") || !strings.Contains(out, "src/a.go") {
		t.Errorf("expected the veto to name its evidence, got:\n%s", out)
	}
	if !strings.Contains(out, "go-source matched, then disqualified") {
		t.Errorf("expected the report to distinguish a vetoed match from no match, got:\n%s", out)
	}
}

func TestReviewJSONCarriesTheVerdict(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)

	out, _ := runReview(t, dir, base, "--json")
	var got struct {
		Command string `json:"command"`
		Verdict string `json:"verdict"`
		Exit    int    `json:"exit"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the machine report must be valid JSON: %v\n%s", err, out)
	}
	if got.Command != "review" || got.Verdict != "refer" || got.Exit != 2 {
		t.Errorf("got %+v, want review/refer/2", got)
	}
}

// The verdict is computed from HEAD so it matches the one CI will reach.
// Silently deciding on HEAD while the developer is looking at edited files is
// the one way this command gives a confidently wrong answer, so it says so.
func TestReviewWarnsWhenTheWorkingTreeIsDirty(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"README.md": "hello again"},
	)
	if err := os.WriteFile(filepath.Join(dir, "src.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _ := runReview(t, dir, base)
	if !strings.Contains(out, "uncommitted changes") {
		t.Errorf("expected the report to exclude and name uncommitted work, got:\n%s", out)
	}
}
