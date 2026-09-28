package threadsflow

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"lydite/lydite/internal/flow"
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

// Params.Inputs supplies exactly the inputs the flow reads, each assignable
// to the type the flow reads it as. A key the flow reads and Params leaves
// out is a run refused before its first stage; a key Params supplies and
// nothing reads is a value the caller believes reaches a stage and does not.
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

// threadsRun builds the flow and runs it with p.
func threadsRun(t *testing.T, p Params) (*flow.Result, error) {
	t.Helper()
	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f.Run(context.Background(), p.Inputs())
}

// The three stages that establish what a run acts on and with — init-trust,
// init-scm, load-pull-request — fail in that order: a repository the
// environment does not name refuses before a missing token is even asked
// about, and a missing token refuses before the event payload is read. Every
// later stage, which reads one of their outputs, is never reached.
func TestFailurePrecedence(t *testing.T) {
	downstream := []string{
		StageReadFindings, StageListThreads, StagePlan, StageWriteOps,
		StageTakeDown, StageAnswer, StageOpen,
	}

	t.Run("no repository", func(t *testing.T) {
		t.Setenv("GITHUB_REPOSITORY", "")
		t.Setenv("GITHUB_TOKEN", "a")
		t.Setenv("GH_TOKEN", "")
		res, err := threadsRun(t, Params{EventPath: "event.json"})
		if err == nil {
			t.Fatal("Run succeeded with GITHUB_REPOSITORY unset")
		}
		var serr *flow.StageError
		if !errors.As(err, &serr) || serr.Stage != StageInitTrust {
			t.Fatalf("err = %v, want a *StageError for %s", err, StageInitTrust)
		}
		for _, stage := range append([]string{StageInitSCM, StageLoadPullRequest}, downstream...) {
			if st := res.Status(stage); st != flow.StatusNotReached {
				t.Errorf("%s = %v, want not reached", stage, st)
			}
		}
	})

	t.Run("no token", func(t *testing.T) {
		t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		res, err := threadsRun(t, Params{EventPath: "event.json"})
		if err == nil {
			t.Fatal("Run succeeded with no token in the environment")
		}
		var serr *flow.StageError
		if !errors.As(err, &serr) || serr.Stage != StageInitSCM {
			t.Fatalf("err = %v, want a *StageError for %s", err, StageInitSCM)
		}
		if st := res.Status(StageInitTrust); st != flow.StatusSucceeded {
			t.Errorf("%s = %v, want succeeded", StageInitTrust, st)
		}
		for _, stage := range append([]string{StageLoadPullRequest}, downstream...) {
			if st := res.Status(stage); st != flow.StatusNotReached {
				t.Errorf("%s = %v, want not reached", stage, st)
			}
		}
	})

	t.Run("no event", func(t *testing.T) {
		t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
		t.Setenv("GITHUB_TOKEN", "a")
		t.Setenv("GH_TOKEN", "")
		res, err := threadsRun(t, Params{EventPath: ""})
		if err == nil {
			t.Fatal("Run succeeded with no event path")
		}
		var serr *flow.StageError
		if !errors.As(err, &serr) || serr.Stage != StageLoadPullRequest {
			t.Fatalf("err = %v, want a *StageError for %s", err, StageLoadPullRequest)
		}
		for _, stage := range []string{StageInitTrust, StageInitSCM} {
			if st := res.Status(stage); st != flow.StatusSucceeded {
				t.Errorf("%s = %v, want succeeded", stage, st)
			}
		}
		for _, stage := range downstream {
			if st := res.Status(stage); st != flow.StatusNotReached {
				t.Errorf("%s = %v, want not reached", stage, st)
			}
		}
	})
}
