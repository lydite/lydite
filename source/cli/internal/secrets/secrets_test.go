package secrets

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func TestArgvKeepsTheReportACopy(t *testing.T) {
	// --report-path writes a copy: gitleaks keeps printing its findings and
	// keeps its exit status, which is what decides the row. Without --verbose it
	// prints a count and no location, and without --redact the credential
	// reaches the CI log and the report artifact alike.
	got := argv("/tmp/report.json")

	if got[0] != "dir" {
		t.Errorf("argv = %q, want the working tree scanned rather than the history", got)
	}
	for _, want := range []string{"--no-banner", "--redact", "--verbose"} {
		if !slices.Contains(got, want) {
			t.Errorf("argv = %q, want %s", got, want)
		}
	}
	if i := slices.Index(got, "--report-format"); i < 0 || got[i+1] != "json" {
		t.Errorf("argv = %q, want the report written as json", got)
	}
	if i := slices.Index(got, "--report-path"); i < 0 || got[i+1] != "/tmp/report.json" {
		t.Errorf("argv = %q, want the report written where the caller asked", got)
	}
	if got[len(got)-1] != "." {
		t.Errorf("argv = %q, want the scan root last", got)
	}
	// gitleaks discovers a repository's own .gitleaks.toml at the scan root, and
	// a --config lydite passed would beat it — the Biome limitation ADR 0020
	// records rather than a model to copy. --ignore-gitleaks-allow would
	// likewise overrule an opt-out a reviewer approved in the source.
	for _, unwanted := range []string{"--config", "--ignore-gitleaks-allow", "--baseline-path"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("argv = %q, want no %s", got, unwanted)
		}
	}
}

func TestArgvNamesADifferentReportEachTime(t *testing.T) {
	// The path is the caller's temporary file, so two runs cannot read each
	// other's report.
	first, second := argv("/tmp/a.json"), argv("/tmp/b.json")
	if slices.Equal(first, second) {
		t.Errorf("argv = %q for both report paths", first)
	}
}

func TestGateNamesTheTool(t *testing.T) {
	// A row names what a reader would re-run, as `gosec` and `semgrep` do.
	if Gate != "gitleaks" {
		t.Errorf("Gate = %q, want gitleaks", Gate)
	}
	if got := FindingGates(); !slices.Equal(got, []string{Gate}) {
		t.Errorf("FindingGates() = %q, want just the one gate", got)
	}
	// A fresh slice per call: a package-level one is a variable every caller
	// can edit.
	FindingGates()[0] = "edited"
	if FindingGates()[0] != Gate {
		t.Error("FindingGates() hands back a slice a caller can write through")
	}
}

func TestThePinIsTheVersionTheInstallAsksFor(t *testing.T) {
	// The module path is github.com/zricethezav/gitleaks/v8: the repository
	// moved to gitleaks/gitleaks and the module path did not, so requiring the
	// new one fails resolution with "module declares its path as".
	want := "github.com/zricethezav/gitleaks/v8@" + gitleaksVersion
	if gitleaksPkg != want {
		t.Errorf("gitleaksPkg = %q, want %q", gitleaksPkg, want)
	}
}

// gitleaks exits leaksExit for a leak, so a failing run whose report names one
// is a whole walk. A report lydite could not read, a walk the status says did
// not finish and a scope git could not be asked for each leave claims whose
// difference from the last run's says nothing about the tree.
func TestCrashedOnlyWhereTheWalkIsNotAWholeAnswer(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "leaks.json", `[{"RuleID":"generic-api-key","File":"a.env","StartLine":1,"StartColumn":2}]`)
	write(t, dir, "empty.json", `[]`)
	write(t, dir, "bad.json", `not json`)
	leaks, empty := filepath.Join(dir, "leaks.json"), filepath.Join(dir, "empty.json")

	if crashed(exitedWith(t, leaksExit), leaks, nil) {
		t.Error("a walk that found a leak read as a crash")
	}
	if crashed(nil, empty, nil) {
		t.Error("a clean walk read as a crash")
	}
	if crashed(nil, empty, errNoScope) {
		t.Error("git listing nothing beside a report naming nothing read as a crash")
	}
	cases := map[string]bool{
		"no report":                       crashed(nil, filepath.Join(dir, "absent.json"), nil),
		"an unparseable report":           crashed(nil, filepath.Join(dir, "bad.json"), nil),
		"a walk that did not finish":      crashed(exitedWith(t, leaksExit), empty, nil),
		"an unknown status":               crashed(exitedWith(t, 126), leaks, nil),
		"a scope git could not answer":    crashed(nil, leaks, errors.New("git failed")),
		"no scope beside a reported leak": crashed(exitedWith(t, leaksExit), leaks, errNoScope),
	}
	for name, got := range cases {
		if !got {
			t.Errorf("%s: not crashed", name)
		}
	}
}
