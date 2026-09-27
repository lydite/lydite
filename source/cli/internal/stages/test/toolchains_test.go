package teststages

import (
	"context"
	"testing"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
)

// A component declaring its own command implies no language, and one
// declaring no suite runs nothing: neither needs a toolchain, so none is
// resolved for either.
func TestToolchainsResolvesNothingForAComponentThatImpliesNoLanguage(t *testing.T) {
	out, err := Toolchains(context.Background(), ToolchainsIn{
		Dir:    t.TempDir(),
		Config: config.Default(),
		Own: []component.Component{
			{Name: "raw", Dir: ".", Command: []string{"true"}},
			{Name: "scripts", Dir: ".", DeclaredLang: runner.Shell},
		},
	})
	if err != nil {
		t.Fatalf("Toolchains: %v", err)
	}
	if env := out.Envs.For("raw"); env != nil {
		t.Errorf("raw = %+v, want no toolchain", env)
	}
	if env := out.Envs.For("scripts"); env != nil {
		t.Errorf("scripts = %+v, want no toolchain", env)
	}
}

// An override this run's own configuration states and lydite cannot compare is
// refused, before any suite runs under a toolchain nobody asked for.
func TestToolchainsRefusesAnOverrideItCannotCompare(t *testing.T) {
	cfg := config.Default()
	cfg.Toolchain.Go = "1.26.x"
	_, err := Toolchains(context.Background(), ToolchainsIn{
		Dir:    t.TempDir(),
		Config: cfg,
		Own:    []component.Component{{Name: "api", Dir: ".", Runner: runner.GoTest}},
	})
	if err == nil {
		t.Error("Toolchains accepted a Go override it cannot compare")
	}
}
