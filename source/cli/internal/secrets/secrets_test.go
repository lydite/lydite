package secrets

import (
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
