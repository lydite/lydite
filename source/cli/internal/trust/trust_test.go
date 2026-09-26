package trust_test

import (
	"context"
	"testing"

	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/trust"
)

func initialized(t *testing.T) (trust.TrustedContext, bool) {
	t.Helper()
	c := flow.NewContext()
	f := flow.Flow{Stages: []flow.Stage{{
		Name:       "trust",
		Components: []flow.StageComponent{trust.InitializeTrustContext{}},
	}}}
	if err := f.Run(context.Background(), c); err != nil {
		t.Fatalf("Flow.Run: %v", err)
	}
	return trust.Get(c)
}

func TestCanWriteFollowsTheTokenEnvironment(t *testing.T) {
	cases := []struct {
		name        string
		githubToken string
		ghToken     string
		want        bool
	}{
		{name: "neither set", want: false},
		{name: "GITHUB_TOKEN set", githubToken: "x", want: true},
		{name: "GH_TOKEN set", ghToken: "x", want: true},
		{name: "both set", githubToken: "x", ghToken: "y", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", tc.githubToken)
			t.Setenv("GH_TOKEN", tc.ghToken)
			trusted, ok := initialized(t)
			if !ok {
				t.Fatal("no TrustedContext joined after InitializeTrustContext ran")
			}
			if got := trusted.CanWrite(); got != tc.want {
				t.Errorf("CanWrite() = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestGetReportsAbsentBeforeInitialization(t *testing.T) {
	got, ok := trust.Get(flow.NewContext())
	if ok {
		t.Fatal("Get on a Context no stage initialized reported present")
	}
	if got.CanWrite() {
		t.Error("the absent TrustedContext answers CanWrite true")
	}
}

func TestZeroValueCannotWrite(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "x")
	if (trust.TrustedContext{}).CanWrite() {
		t.Error("a TrustedContext made outside the package answers CanWrite true")
	}
}
