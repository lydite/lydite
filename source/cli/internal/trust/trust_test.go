package trust_test

import (
	"testing"

	"lydite/lydite/internal/trust"
)

func TestFromEnvironmentFollowsTheTokenEnvironment(t *testing.T) {
	cases := []struct {
		name        string
		githubToken string
		ghToken     string
		wantToken   string
		wantWrite   bool
	}{
		{name: "neither set", wantToken: "", wantWrite: false},
		{name: "GITHUB_TOKEN set", githubToken: "a", wantToken: "a", wantWrite: true},
		{name: "GH_TOKEN set", ghToken: "b", wantToken: "b", wantWrite: true},
		{name: "both set prefers GITHUB_TOKEN", githubToken: "a", ghToken: "b", wantToken: "a", wantWrite: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_TOKEN", tc.ghToken)
			trusted, err := trust.FromEnvironment()
			if err != nil {
				t.Fatalf("FromEnvironment: %v", err)
			}
			if got := trusted.Token(); got != tc.wantToken {
				t.Errorf("Token() = %q; want %q", got, tc.wantToken)
			}
			if got := trusted.CanWrite(); got != tc.wantWrite {
				t.Errorf("CanWrite() = %t; want %t", got, tc.wantWrite)
			}
		})
	}
}

func TestFromEnvironmentReadsTheRepository(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "owner/name")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	trusted, err := trust.FromEnvironment()
	if err != nil {
		t.Fatalf("FromEnvironment: %v", err)
	}
	if got := trusted.Repository(); got != "owner/name" {
		t.Errorf("Repository() = %q; want %q", got, "owner/name")
	}
}

func TestFromEnvironmentRefusesAnUnusableRepository(t *testing.T) {
	for _, slug := range []string{"", "owner", "owner/", "/name", "owner/name/extra"} {
		t.Run(slug, func(t *testing.T) {
			t.Setenv("GITHUB_REPOSITORY", slug)
			t.Setenv("GITHUB_TOKEN", "a")
			trusted, err := trust.FromEnvironment()
			if err == nil {
				t.Fatalf("FromEnvironment with GITHUB_REPOSITORY=%q succeeded", slug)
			}
			if trusted.CanWrite() || trusted.Token() != "" || trusted.Repository() != "" {
				t.Error("the refused TrustedContext carries a token or a repository")
			}
		})
	}
}

func TestZeroValueHoldsNothing(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
	t.Setenv("GITHUB_TOKEN", "a")
	var zero trust.TrustedContext
	if zero.CanWrite() {
		t.Error("a TrustedContext made outside the package answers CanWrite true")
	}
	if zero.Token() != "" {
		t.Error("a TrustedContext made outside the package carries a token")
	}
	if zero.Repository() != "" {
		t.Error("a TrustedContext made outside the package names a repository")
	}
}
