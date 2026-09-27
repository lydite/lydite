package truststages_test

import (
	"context"
	"testing"

	truststages "lydite/lydite/internal/stages/trust"
)

func TestInitTrustReturnsTheEnvironmentsTrustedContext(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
	t.Setenv("GITHUB_TOKEN", "a")
	t.Setenv("GH_TOKEN", "")
	out, err := truststages.InitTrust(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("InitTrust: %v", err)
	}
	if !out.Trusted.CanWrite() {
		t.Error("CanWrite() = false with GITHUB_TOKEN set")
	}
	if got := out.Trusted.Token(); got != "a" {
		t.Errorf("Token() = %q; want %q", got, "a")
	}
	if got := out.Trusted.Repository(); got != "lydite/lydite" {
		t.Errorf("Repository() = %q; want %q", got, "lydite/lydite")
	}
}

func TestInitTrustWithoutATokenCannotWrite(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	out, err := truststages.InitTrust(context.Background(), struct{}{})
	if err != nil {
		t.Fatalf("InitTrust: %v", err)
	}
	if out.Trusted.CanWrite() {
		t.Error("CanWrite() = true with no token in the environment")
	}
}

func TestInitTrustFailsWithoutARepository(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("GITHUB_TOKEN", "a")
	out, err := truststages.InitTrust(context.Background(), struct{}{})
	if err == nil {
		t.Fatal("InitTrust succeeded with GITHUB_REPOSITORY unset")
	}
	if out.Trusted.CanWrite() {
		t.Error("the failed stage's TrustedContext answers CanWrite true")
	}
}
