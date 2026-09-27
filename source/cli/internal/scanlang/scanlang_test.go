package scanlang_test

import (
	"testing"

	"lydite/lydite/internal/config"
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

// Enabled reads each language's own switch in .lydite/config.yml, and a
// language it does not enumerate answers false regardless of what the config
// carries.
func TestEnabledReadsEachLanguagesOwnSwitch(t *testing.T) {
	cfg := config.Config{
		Rust:       config.Language{Enabled: true},
		TypeScript: config.TypeScriptLanguage{Language: config.Language{Enabled: false}},
		Go:         config.Language{Enabled: true},
		Shell:      config.Language{Enabled: false},
	}

	cases := []struct {
		lang runner.Lang
		want bool
	}{
		{runner.Rust, true},
		{runner.TypeScript, false},
		{runner.Go, true},
		{runner.Shell, false},
		{"", false},
		{runner.Python, false},
		{runner.Lang("cobol"), false},
	}

	for _, c := range cases {
		if got := scanlang.Enabled(c.lang, cfg); got != c.want {
			t.Errorf("Enabled(%q, cfg) = %v, want %v", c.lang, got, c.want)
		}
	}
}
