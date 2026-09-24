package scanlang_test

import (
	"testing"

	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scanlang"
)

// The languages scan has checks for, and nothing else: shell, which has a
// scanner and no runner, answers true, and a language with a runner and no
// scanner, or with neither, must answer false so its reader says what it
// could not do rather than claiming a check ran.
func TestOnlyALanguageWithScannersIsScanned(t *testing.T) {
	for _, l := range []runner.Lang{runner.Go, runner.Rust, runner.TypeScript, runner.Shell} {
		if !scanlang.Scanned(l) {
			t.Errorf("Scanned(%s) = false, want the languages scan has checks for", l)
		}
	}
	for _, l := range []runner.Lang{"", runner.Python, runner.Lang("cobol")} {
		if scanlang.Scanned(l) {
			t.Errorf("Scanned(%q) = true, want a language with no scanner", l)
		}
	}
}
