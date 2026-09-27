package reviewstages

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
)

// Every verdict, and every way a description is chosen for one, composes the
// exact status both publishing routes carry: the state, the context, the
// description, the job's link, and the revision and pull request the payload
// pointed at.
func TestComposeStatusComposesEveryVerdict(t *testing.T) {
	cases := []struct {
		name        string
		verdict     reviewdecision.Verdict
		decision    referral.Decision
		state       clearance.State
		description string
	}{
		{
			name:        "a failure",
			verdict:     reviewdecision.VerdictFail,
			decision:    referral.Decision{Bundled: []string{"a.go"}},
			state:       clearance.StateFailure,
			description: "exemption change not isolated — split it into its own pull request",
		},
		{
			name:        "a referral an exemption matched",
			verdict:     reviewdecision.VerdictRefer,
			decision:    referral.Decision{Referred: true, Exemption: "readme-only"},
			state:       clearance.StatePending,
			description: "readme-only matched, then disqualified — comment /lydite clear",
		},
		{
			name:        "a referral nothing matched",
			verdict:     reviewdecision.VerdictRefer,
			decision:    referral.Decision{Referred: true},
			state:       clearance.StatePending,
			description: "referred — comment /lydite clear",
		},
		{
			name:        "an empty change",
			verdict:     reviewdecision.VerdictPass,
			decision:    referral.Decision{Empty: true},
			state:       clearance.StateSuccess,
			description: "no changes against the base",
		},
		{
			name:        "an exempt change",
			verdict:     reviewdecision.VerdictPass,
			decision:    referral.Decision{Exemption: "readme-only"},
			state:       clearance.StateSuccess,
			description: "exempt: readme-only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ComposeStatus(context.Background(), ComposeStatusIn{
				Result:    reviewdecision.Result{Decision: tc.decision, Verdict: tc.verdict},
				Ref:       forge.PullRequestRef{Number: 7, SHA: "abc123", Title: "fix: a thing"},
				TargetURL: "https://github.com/owner/name/actions/runs/1",
			})
			if err != nil {
				t.Fatalf("ComposeStatus: %v", err)
			}
			want := forge.Status{
				State:       tc.state,
				Context:     clearance.Context,
				Description: tc.description,
				TargetURL:   "https://github.com/owner/name/actions/runs/1",
				SHA:         "abc123",
				PullRequest: 7,
			}
			if out.Status != want {
				t.Errorf("ComposeStatus = %+v; want %+v", out.Status, want)
			}
		})
	}
}

// No job to link to leaves the link out rather than inventing one.
func TestComposeStatusLeavesAnEmptyTargetURLEmpty(t *testing.T) {
	out, err := ComposeStatus(context.Background(), ComposeStatusIn{
		Result: reviewdecision.Result{Verdict: reviewdecision.VerdictPass, Decision: referral.Decision{Empty: true}},
		Ref:    forge.PullRequestRef{Number: 7, SHA: "abc123"},
	})
	if err != nil {
		t.Fatalf("ComposeStatus: %v", err)
	}
	if out.Status.TargetURL != "" {
		t.Errorf("TargetURL = %q; want empty", out.Status.TargetURL)
	}
}

// A verdict with no status of its own is refused, never published as the
// success every unnamed value would otherwise fall through to.
func TestComposeStatusRefusesAVerdictItDoesNotName(t *testing.T) {
	for _, verdict := range []reviewdecision.Verdict{"", "unmeasured"} {
		_, err := ComposeStatus(context.Background(), ComposeStatusIn{
			Result: reviewdecision.Result{Verdict: verdict, Decision: referral.Decision{Exemption: "readme-only"}},
			Ref:    forge.PullRequestRef{Number: 7, SHA: "abc123"},
		})
		if err == nil {
			t.Errorf("ComposeStatus composed a status for verdict %q", verdict)
		}
	}
}

// The document RenderStatus writes is the status, as forge.WriteStatus
// renders it for the step that posts it.
func TestRenderStatusWritesTheDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status", "referral.json")
	status := forge.Status{
		State:       clearance.StatePending,
		Context:     clearance.Context,
		Description: "referred — comment /lydite clear",
		TargetURL:   "https://github.com/owner/name/actions/runs/1",
		SHA:         "abc123",
		PullRequest: 7,
	}
	if _, err := RenderStatus(context.Background(), RenderStatusIn{Path: path, Status: status}); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the test's own temporary path
	if err != nil {
		t.Fatal(err)
	}
	var got forge.Status
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the rendered document does not parse: %v\n%s", err, raw)
	}
	if got != status {
		t.Errorf("rendered %+v; want %+v", got, status)
	}
}

func TestRenderStatusPropagatesAnUnwritablePath(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderStatus(context.Background(), RenderStatusIn{Path: filepath.Join(blocker, "referral.json")}); err == nil {
		t.Error("RenderStatus beneath a file succeeded")
	}
}
