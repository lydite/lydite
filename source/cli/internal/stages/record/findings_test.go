package recordstages

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
)

// recordLangEnabled stands in for `lydite scan`'s own answer: a language is on
// when its section of the configuration says so.
func recordLangEnabled(l runner.Lang, cfg config.Config) bool {
	switch l {
	case runner.Rust:
		return cfg.Rust.Enabled
	case runner.TypeScript:
		return cfg.TypeScript.Enabled
	case runner.Go:
		return cfg.Go.Enabled
	case runner.Shell:
		return cfg.Shell.Enabled
	}
	return false
}

// recordScannerGates stands in for `lydite scan`'s gate sets: each language
// carries a gate of its own, and every language with a dependency set carries
// the licence gate.
func recordScannerGates(l runner.Lang) []string {
	switch l {
	case runner.Go:
		return []string{"gosec", "govulncheck", licence.Gate}
	case runner.Rust:
		return []string{"clippy", licence.Gate}
	case runner.TypeScript:
		return []string{"biome", licence.Gate}
	case runner.Shell:
		return []string{"shellcheck"}
	}
	return nil
}

// recordFindingCounts is FindingCounts over the stand-in gate sets.
func recordFindingCounts(dir string, decl component.File, cfg config.Config, found []finding.Finding, scanned bool) (map[string]map[string]int, map[string]int) {
	return FindingCounts(dir, decl, cfg, found, scanned, recordLangEnabled, recordScannerGates)
}

// No scan document means no finding counts at all, and never a nought per
// applicable gate.
//
// A nought says the gate ran and found nothing. Inventing one for a scan that
// never happened is the single thing this channel must not do: it would draw a
// clean line through every commit whose scan job died.
func TestNoScanDocumentRecordsNoFindingCountAtAll(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "svc", Dir: "svc", Runner: "go-test"},
	}}
	perComponent, root := recordFindingCounts(t.TempDir(), decl, config.Default(), nil, false)
	if perComponent != nil || root != nil {
		t.Errorf("FindingCounts = %v / %v, want nothing recorded for a recording that read no scan", perComponent, root)
	}
	// And with a scan that read clean, the same declaration records noughts.
	perComponent, root = recordFindingCounts(t.TempDir(), decl, config.Default(), nil, true)
	if got, ok := perComponent["svc"]["gosec"]; !ok || got != 0 {
		t.Errorf("gosec = %d (present %v), want a recorded nought for a clean scan", got, ok)
	}
	if got, ok := root[semgrep.Gate]; !ok || got != 0 {
		t.Errorf("semgrep = %d (present %v), want a recorded nought for a clean scan", got, ok)
	}
	// The default configuration states no licence policy, so the gate ran over
	// nothing and a nought for it would be the same invention.
	if got, ok := perComponent["svc"][licence.Gate]; ok {
		t.Errorf("licence = %d, want no key: no policy governs the component", got)
	}
}

// A licence nought is seeded only where a policy governs the component.
//
// Go and TypeScript gate on the repository's own licence.policy.allow alone; a
// Rust component gates on that or on its own deny.toml. A count seeded where
// neither applies gives a repository that never turned the gate on the trend
// line of one whose policy ran clean on every commit, which is
// absent-is-not-zero in the gate whose configuration is most often left unset.
func TestALicenceCountIsSeededOnlyWhereAPolicyGates(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: runner.GoTest},
		{Name: "svc", Dir: "svc", Runner: runner.CargoNextest},
		{Name: "web", Dir: "web", Runner: runner.Vitest},
	}}
	stated := config.Default()
	stated.Licence.Policy.Allow = []string{"MIT"}

	for _, tc := range []struct {
		name  string
		cfg   config.Config
		deny  bool
		wants map[string]bool // component name -> licence key expected
	}{
		{name: "no policy anywhere", cfg: config.Default(), wants: map[string]bool{"api": false, "svc": false, "web": false}},
		{name: "a stated policy", cfg: stated, wants: map[string]bool{"api": true, "svc": true, "web": true}},
		// A component's own deny.toml is Rust's alone: no other language has a
		// second policy source, so neither of its neighbours gates on it.
		{name: "a consumer deny.toml alone", cfg: config.Default(), deny: true,
			wants: map[string]bool{"api": false, "svc": true, "web": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, c := range decl.Components {
				if err := os.MkdirAll(filepath.Join(dir, c.Dir), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if tc.deny {
				recordWrite(t, dir, map[string]string{"svc/deny.toml": "[licenses]\n"})
			}

			perComponent, _ := recordFindingCounts(dir, decl, tc.cfg, nil, true)

			for name, want := range tc.wants {
				got, ok := perComponent[name][licence.Gate]
				if ok != want {
					t.Errorf("%s licence present = %v (%d), want %v", name, ok, got, want)
				}
				if ok && got != 0 {
					t.Errorf("%s licence = %d, want a nought for a clean scan", name, got)
				}
			}
			// The rest of each language's gate set is unaffected by the policy.
			if _, ok := perComponent["api"]["gosec"]; !ok {
				t.Errorf("api = %v, want gosec recorded whatever the licence policy says", perComponent["api"])
			}
			if _, ok := perComponent["svc"]["clippy"]; !ok {
				t.Errorf("svc = %v, want clippy recorded whatever the licence policy says", perComponent["svc"])
			}
		})
	}
}

// A root-scoped claim is recorded even when the gate that makes them is
// switched off, rather than panicking on a map the configuration said would
// never be needed.
//
// Semgrep off means scan runs it over nothing, so this is the shape no run
// produces — which is exactly why the map has to be created on demand: a count
// arriving for a gate the configuration did not expect must be recorded, and a
// write to a nil map is a crash in the one job holding a token that can push.
func TestARootScopedClaimIsRecordedWithItsGateSwitchedOff(t *testing.T) {
	cfg := config.Default()
	cfg.Semgrep.Enabled = false
	_, root := recordFindingCounts(t.TempDir(), component.File{}, cfg, []finding.Finding{{
		Gate: semgrep.Gate, Path: "svc/lib.go", Line: 2, Message: "tainted input",
		Site: "rule\x1fn + 1",
	}}, true)
	if got := root[semgrep.Gate]; got != 1 {
		t.Errorf("semgrep = %d, want the claim recorded rather than dropped or panicked on", got)
	}
}

// A component with no language, and one whose language is switched off, record
// no finding count.
//
// Neither has a gate that applies, so a nought for either would say a scanner
// looked at code lydite never offered it — a component declaring its own
// command implies no language at all, and `enabled: false` is the repository
// saying that language's checks do not run.
func TestAComponentWithNoApplicableGateRecordsNoCount(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "raw", Dir: "raw", Command: []string{"make", "check"}},
		{Name: "api", Dir: "api", Runner: "go-test"},
	}}
	cfg := config.Default()
	cfg.Go.Enabled = false
	cfg.Semgrep.Enabled = false
	cfg.Secrets.Enabled = false

	perComponent, root := recordFindingCounts(t.TempDir(), decl, cfg, nil, true)

	if len(perComponent) != 0 {
		t.Errorf("FindingCounts = %v, want no component recorded: one declares its own command and the other's language is off", perComponent)
	}
	if len(root) != 0 {
		t.Errorf("root = %v, want nothing for a scan whose root-scoped gates are all off", root)
	}
}

// Each root-scoped gate seeds its own nought, and only its own.
//
// Semgrep and gitleaks are switched on separately, so a nought recorded for one
// because the other is enabled would say a scanner ran over the tree that the
// configuration never asked to run — the absent-is-not-zero rule, in the pair of
// gates most likely to be configured apart.
func TestEachRootScopedGateSeedsItsOwnNought(t *testing.T) {
	for _, tc := range []struct {
		name    string
		semgrep bool
		wants   string
		absent  string
	}{
		{name: "secrets alone", semgrep: false, wants: secrets.Gate, absent: semgrep.Gate},
		{name: "semgrep alone", semgrep: true, wants: semgrep.Gate, absent: secrets.Gate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Semgrep.Enabled = tc.semgrep
			cfg.Secrets.Enabled = !tc.semgrep

			_, root := recordFindingCounts(t.TempDir(), component.File{}, cfg, nil, true)

			if got, ok := root[tc.wants]; !ok || got != 0 {
				t.Errorf("%s = %d (present %v), want a recorded nought for a clean scan", tc.wants, got, ok)
			}
			if _, ok := root[tc.absent]; ok {
				t.Errorf("%s recorded a count with the gate switched off: %v", tc.absent, root)
			}
		})
	}
}

// A root-scoped gate's claims are counted over the repository rather than
// inside a component, which is where ledger.Record.RootFindings holds them.
func TestASecretClaimIsCountedOverTheRepositoryAndNotInAComponent(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}

	perComponent, root := recordFindingCounts(t.TempDir(), decl, config.Default(), []finding.Finding{{
		Gate: secrets.Gate, Path: "cli/config.yml", Line: 3,
		Message: "Detected a Generic API Key. Rotate this credential",
		Site:    "generic-api-key\x1faws_key: ",
	}}, true)

	if got := root[secrets.Gate]; got != 1 {
		t.Errorf("root[%s] = %d, want the claim counted over the repository", secrets.Gate, got)
	}
	if got, ok := perComponent["cli"][secrets.Gate]; ok {
		t.Errorf("perComponent[cli][%s] = %d, want no key: the claim names no component", secrets.Gate, got)
	}
}

// A component scanned as a declared lang: records its language's gate noughts
// like a runner component does, and one whose language is switched off or
// states none records none — a nought there would read as a clean scan of
// source nothing checked.
func TestFindingCountsReadTheLanguageAComponentIsScannedAs(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "tool", Dir: "tool", Command: []string{"make", "test"}, DeclaredLang: runner.Go},
		{Name: "scripts", Dir: "scripts", DeclaredLang: runner.Shell},
		{Name: "legacy", Dir: "legacy", Command: []string{"make", "check"}},
	}}

	perComponent, _ := recordFindingCounts(t.TempDir(), decl, config.Default(), nil, true)

	if want := map[string]int{"gosec": 0, "govulncheck": 0}; !reflect.DeepEqual(perComponent["tool"], want) {
		t.Errorf("tool = %v, want %v: a nought for each Go gate it is scanned by", perComponent["tool"], want)
	}
	for _, name := range []string{"scripts", "legacy"} {
		if got, ok := perComponent[name]; ok {
			t.Errorf("perComponent[%s] = %v, want no entry: no gate scans it", name, got)
		}
	}

	// Switched on, shell records its own gate's nought and nothing else: it
	// has no dependency set, so no licence key.
	enabled := config.Default()
	enabled.Shell.Enabled = true
	enabled.Licence.Policy.Allow = []string{"MIT"}
	perComponent, _ = recordFindingCounts(t.TempDir(), decl, enabled, nil, true)
	if got := perComponent["scripts"]; !reflect.DeepEqual(got, map[string]int{"shellcheck": 0}) {
		t.Errorf("perComponent[scripts] = %v, want only a shellcheck nought", got)
	}
}

// A claim is counted under the component it names, bounded by the
// declaration: a component the tree no longer declares records nothing, and a
// gate the language does not name is keyed by the claim that proves it ran.
func TestAClaimIsCountedUnderTheDeclaredComponentItNames(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}
	cfg := config.Default()
	cfg.Semgrep.Enabled = false
	cfg.Secrets.Enabled = false

	perComponent, root := recordFindingCounts(t.TempDir(), decl, cfg, []finding.Finding{
		{Gate: "gosec", Component: "cli", Path: "cli/a.go", Rule: "G101"},
		{Gate: "gosec", Component: "cli", Path: "cli/b.go", Rule: "G101"},
		{Gate: "staticcheck", Component: "cli", Path: "cli/a.go", Rule: "SA1000"},
		{Gate: "gosec", Component: "gone", Path: "gone/a.go", Rule: "G101"},
	}, true)

	want := map[string]map[string]int{"cli": {"gosec": 2, "govulncheck": 0, "staticcheck": 1}}
	if !reflect.DeepEqual(perComponent, want) {
		t.Errorf("perComponent = %v, want %v", perComponent, want)
	}
	if root != nil {
		t.Errorf("root = %v, want nothing: every root-scoped gate is off and none made a claim", root)
	}
}

// The gate set is asked of the functions handed in, and of nothing else: the
// languages a scan checks and the gates each reports under are `lydite scan`'s
// to answer.
func TestFindingCountsAsksTheInjectedGateSet(t *testing.T) {
	decl := component.File{Components: []component.Component{
		{Name: "api", Dir: "api", Runner: "go-test"},
		{Name: "web", Dir: "web", Runner: "vitest"},
	}}
	var asked []runner.Lang
	onlyGo := func(l runner.Lang, _ config.Config) bool { return l == runner.Go }
	gates := func(l runner.Lang) []string {
		asked = append(asked, l)
		return []string{"custom"}
	}

	perComponent, _ := FindingCounts(t.TempDir(), decl, config.Default(), nil, true, onlyGo, gates)

	if want := map[string]map[string]int{"api": {"custom": 0}}; !reflect.DeepEqual(perComponent, want) {
		t.Errorf("perComponent = %v, want %v", perComponent, want)
	}
	if !reflect.DeepEqual(asked, []runner.Lang{runner.Go}) {
		t.Errorf("gate sets asked for %v, want only the enabled language's", asked)
	}
}

// The stage is FindingCounts over its In, and says nothing FindingCounts does
// not.
func TestCountFindingsIsFindingCountsOverItsIn(t *testing.T) {
	dir := t.TempDir()
	decl := component.File{Components: []component.Component{
		{Name: "cli", Dir: "cli", Runner: "go-test"},
	}}
	found := []finding.Finding{
		{Gate: "gosec", Component: "cli", Path: "cli/a.go", Rule: "G101"},
		{Gate: secrets.Gate, Path: "b.env", Rule: "generic-api-key"},
	}
	wantComponent, wantRoot := recordFindingCounts(dir, decl, config.Default(), found, true)

	out, err := CountFindings(context.Background(), CountFindingsIn{
		Dir: dir, Declaration: decl, Config: config.Default(), Found: found, Scanned: true,
		LangEnabled: recordLangEnabled, ScannerGates: recordScannerGates,
	})
	if err != nil {
		t.Fatalf("CountFindings: %v", err)
	}
	if !reflect.DeepEqual(out.PerComponent, wantComponent) || !reflect.DeepEqual(out.Root, wantRoot) {
		t.Errorf("CountFindings = %v / %v, want %v / %v", out.PerComponent, out.Root, wantComponent, wantRoot)
	}

	out, err = CountFindings(context.Background(), CountFindingsIn{
		Dir: dir, Declaration: decl, Config: config.Default(), Found: found, Scanned: false,
		LangEnabled: recordLangEnabled, ScannerGates: recordScannerGates,
	})
	if err != nil {
		t.Fatalf("CountFindings: %v", err)
	}
	if out.PerComponent != nil || out.Root != nil {
		t.Errorf("CountFindings = %+v, want nothing from a recording that read no scan", out)
	}
}

// The licence gate applies by language and policy: Go and TypeScript under the
// repository's own policy, Rust under that or its own deny.toml, and no other
// language at all.
func TestLicenceGatedFollowsEachLanguagesPolicySource(t *testing.T) {
	none := licence.NewPolicy(nil)
	stated := licence.NewPolicy([]string{"MIT"})
	bare := t.TempDir()
	denied := t.TempDir()
	recordWrite(t, denied, map[string]string{"deny.toml": "[licenses]\n"})

	for _, tc := range []struct {
		name   string
		cdir   string
		lang   runner.Lang
		policy licence.Policy
		want   bool
	}{
		{"go with no policy", bare, runner.Go, none, false},
		{"go under a policy", bare, runner.Go, stated, true},
		{"typescript under a policy", bare, runner.TypeScript, stated, true},
		{"typescript beside a deny.toml", denied, runner.TypeScript, none, false},
		{"rust with neither", bare, runner.Rust, none, false},
		{"rust under a policy", bare, runner.Rust, stated, true},
		{"rust under its own deny.toml", denied, runner.Rust, none, true},
		{"shell under a policy", bare, runner.Shell, stated, false},
		{"python under a policy", bare, runner.Python, stated, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := licenceGated(tc.cdir, tc.lang, tc.policy); got != tc.want {
				t.Errorf("licenceGated = %v, want %v", got, tc.want)
			}
		})
	}
}
