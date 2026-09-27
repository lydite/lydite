package scanflow

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/toolchain"
)

// Every binding in the declaration is checked by Build, so a flow that builds
// is one whose stages can only fail at run time for their own reasons.
func TestTheFlowBuilds(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Name() != Name {
		t.Errorf("Name() = %q, want %q", f.Name(), Name)
	}
}

// Params.Inputs supplies exactly the inputs the flow reads, each assignable to
// the type the flow reads it as. A key the flow reads and Params leaves out
// is a run refused before its first stage; a key Params supplies and nothing
// reads is a value the caller believes reaches a stage and does not.
func TestParamsSupplyExactlyTheInputsTheFlowReads(t *testing.T) {
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := f.Inputs()
	got := (Params{}).Inputs()
	if w, g := slices.Sorted(maps.Keys(want)), slices.Sorted(maps.Keys(got)); !slices.Equal(w, g) {
		t.Fatalf("the flow reads %v, and Params supplies %v", w, g)
	}
}

// fakeToolchains provisions nothing, for a repository whose one component
// declares no language a toolchain is needed for.
type fakeToolchains struct{}

func (fakeToolchains) Ensure(context.Context, string, config.Config, []toolchain.Unit) (toolchain.Envs, error) {
	return toolchain.Envs{}, nil
}

// fakeEnvironment composes and declares nothing, for a component with no env:
// of its own.
type fakeEnvironment struct{}

func (fakeEnvironment) Compose(*toolchain.Env, component.Component) []string { return nil }
func (fakeEnvironment) Declared(component.Component) (dirs, vars []string)   { return nil, nil }

// Semgrep and secrets each condition on load-config's own SemgrepEnabled and
// SecretsEnabled, so a repository that switches both off never reaches
// either scanner.
func TestSemgrepAndSecretsAreSkippedWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	lyditeDir := filepath.Join(dir, ".lydite")
	if err := os.MkdirAll(lyditeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	componentsYAML := "components:\n  - name: raw\n    dir: .\n    command: [\"true\"]\n"
	if err := os.WriteFile(filepath.Join(lyditeDir, "components.yml"), []byte(componentsYAML), 0o644); err != nil {
		t.Fatalf("WriteFile components.yml: %v", err)
	}
	configYAML := "semgrep:\n  enabled: false\nsecrets:\n  enabled: false\n"
	if err := os.WriteFile(filepath.Join(lyditeDir, "config.yml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("WriteFile config.yml: %v", err)
	}

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{
		Dir:         dir,
		Toolchains:  fakeToolchains{},
		Environment: fakeEnvironment{},
		Diagnostics: io.Discard,
	}
	r, err := f.Run(context.Background(), params.Inputs())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Skipped(StageSemgrep) {
		t.Errorf("semgrep status = %v, want skipped", r.Status(StageSemgrep))
	}
	if !r.Skipped(StageSecrets) {
		t.Errorf("secrets status = %v, want skipped", r.Status(StageSecrets))
	}
}
