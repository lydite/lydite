package scanstages

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
)

// write puts content at rel under dir, creating the directories it needs.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigWithNoFileIsTheDefaults(t *testing.T) {
	out, err := LoadConfig(context.Background(), LoadConfigIn{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	def := config.Default()
	if out.Config.Semgrep != def.Semgrep || out.Config.Secrets != def.Secrets {
		t.Errorf("LoadConfig with no file = semgrep %+v, secrets %+v; want the defaults %+v, %+v",
			out.Config.Semgrep, out.Config.Secrets, def.Semgrep, def.Secrets)
	}
	if !out.SemgrepEnabled || !out.SecretsEnabled {
		t.Errorf("LoadConfig with no file: SemgrepEnabled = %v, SecretsEnabled = %v; want both on, as the defaults are",
			out.SemgrepEnabled, out.SecretsEnabled)
	}
	if out.SemgrepConfig != def.Semgrep.Config {
		t.Errorf("LoadConfig with no file: SemgrepConfig = %q, want the default %q", out.SemgrepConfig, def.Semgrep.Config)
	}
}

// SemgrepConfig is the flat restatement semgrep's stage binds Config from, so
// it must follow semgrep.config exactly, default or overridden.
func TestLoadConfigStatesSemgrepConfigFromItsOwnKey(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "semgrep:\n  config: p/security-audit\n")
	out, err := LoadConfig(context.Background(), LoadConfigIn{Dir: dir})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if out.SemgrepConfig != "p/security-audit" || out.Config.Semgrep.Config != "p/security-audit" {
		t.Errorf("SemgrepConfig = %q, Config.Semgrep.Config = %q; want %q",
			out.SemgrepConfig, out.Config.Semgrep.Config, "p/security-audit")
	}
}

// The flat switches are what a flow's conditions read, so each must follow
// its own key in the file and never the other's.
func TestLoadConfigStatesEachRootScanSwitchFromItsOwnKey(t *testing.T) {
	cases := []struct {
		name            string
		file            string
		semgrep, secret bool
	}{
		{"both off", "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n", false, false},
		{"semgrep off", "semgrep:\n  enabled: false\n", false, true},
		{"secrets off", "secrets:\n  enabled: false\n", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, config.FileName, tc.file)
			out, err := LoadConfig(context.Background(), LoadConfigIn{Dir: dir})
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if out.SemgrepEnabled != tc.semgrep || out.Config.Semgrep.Enabled != tc.semgrep {
				t.Errorf("SemgrepEnabled = %v, Config.Semgrep.Enabled = %v; want %v",
					out.SemgrepEnabled, out.Config.Semgrep.Enabled, tc.semgrep)
			}
			if out.SecretsEnabled != tc.secret || out.Config.Secrets.Enabled != tc.secret {
				t.Errorf("SecretsEnabled = %v, Config.Secrets.Enabled = %v; want %v",
					out.SecretsEnabled, out.Config.Secrets.Enabled, tc.secret)
			}
		})
	}
}

// A configuration that does not parse is the stage's error, carrying
// config.Load's own text, never a scan under the defaults.
func TestLoadConfigRefusesAFileThatDoesNotParse(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "semgrep: [\n")
	_, want := config.Load(dir)
	if want == nil {
		t.Fatal("config.Load accepted the malformed file this test relies on")
	}
	_, err := LoadConfig(context.Background(), LoadConfigIn{Dir: dir})
	if err == nil || err.Error() != want.Error() {
		t.Errorf("LoadConfig error = %v; want config.Load's own %v", err, want)
	}
}

func TestLoadComponentsReturnsTheDeclaration(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName,
		"components:\n  - name: cli\n    dir: .\n    runner: go-test\n  - name: web\n    dir: web\n    runner: vitest\n")
	write(t, dir, "go.mod", "module x\n\ngo 1.26\n")
	write(t, dir, "web/package.json", "{}\n")
	out, err := LoadComponents(context.Background(), LoadComponentsIn{Dir: dir})
	if err != nil {
		t.Fatalf("LoadComponents: %v", err)
	}
	var names []string
	for _, c := range out.File.Components {
		names = append(names, c.Name)
	}
	if len(names) != 2 || names[0] != "cli" || names[1] != "web" {
		t.Errorf("LoadComponents components = %v; want [cli web], in declaration order", names)
	}
}

// A repository declaring nothing would be scanned by nothing while the job
// stayed green, so an empty declaration — or none at all — is refused, naming
// the file to write.
func TestLoadComponentsRefusesAnEmptyDeclaration(t *testing.T) {
	cases := []struct {
		name    string
		present bool
		file    string
	}{
		{"no file", false, ""},
		{"an empty file", true, ""},
		{"an empty list", true, "components: []\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.present {
				write(t, dir, component.FileName, tc.file)
			}
			_, err := LoadComponents(context.Background(), LoadComponentsIn{Dir: dir})
			want := "no components declared in " + filepath.Join(dir, filepath.FromSlash(component.FileName)) +
				": scan runs the checks each component's language implies, so declare what this repository builds"
			if err == nil || err.Error() != want {
				t.Errorf("LoadComponents error = %v; want %q", err, want)
			}
		})
	}
}

// A declaration that does not validate is component.Load's error, unchanged.
func TestLoadComponentsRefusesADeclarationThatDoesNotValidate(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, component.FileName, "components:\n  - name: cli\n    dir: missing\n    runner: go-test\n")
	_, want := component.Load(dir)
	if want == nil {
		t.Fatal("component.Load accepted the declaration this test relies on")
	}
	_, err := LoadComponents(context.Background(), LoadComponentsIn{Dir: dir})
	if err == nil || err.Error() != want.Error() {
		t.Errorf("LoadComponents error = %v; want component.Load's own %v", err, want)
	}
}
