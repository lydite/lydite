package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// fakeForge stands in for the hosting platform, recording what was published
// so a test can assert on the decision's effect rather than on its wording.
type fakeForge struct {
	permission string
	statuses   []map[string]any
	// changed is the platform's own list-files answer, which is the only
	// thing the comment surface asks about the pull request itself.
	changed   []map[string]any
	published []map[string]string
	comments  []string
}

func (f *fakeForge) start(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			_ = json.NewEncoder(w).Encode(f.changed)
		case strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = w.Write([]byte(`{"head":{"sha":"` + head + `"}}`))
		case strings.Contains(r.URL.Path, "/permission"):
			_, _ = w.Write([]byte(`{"permission":"` + f.permission + `"}`))
		case strings.Contains(r.URL.Path, "/statuses") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(f.statuses)
		case strings.Contains(r.URL.Path, "/statuses") && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.published = append(f.published, body)
			w.WriteHeader(http.StatusCreated)
		case strings.Contains(r.URL.Path, "/comments"):
			if r.Method == http.MethodPost {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.comments = append(f.comments, body["body"])
				w.WriteHeader(http.StatusCreated)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Setenv("GITHUB_TOKEN", "token")
}

const head = "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091"

func eventFile(t *testing.T, body, login string, at time.Time) string {
	t.Helper()
	payload := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       40,
			"pull_request": map[string]any{"url": "https://example.invalid/pulls/40"},
		},
		"comment": map[string]any{
			"body":       body,
			"created_at": at.Format(time.RFC3339),
			"user":       map[string]any{"login": login},
		},
		"repository": map[string]any{"full_name": "lydite/lydite"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func statusEntry(state string, at time.Time) map[string]any {
	return map[string]any{
		"state": state, "context": clearance.Context,
		"description": "referred", "created_at": at.Format(time.RFC3339),
	}
}

func runClearanceCmd(t *testing.T, eventPath string) string {
	t.Helper()
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runClearance(context.Background(), cmd, eventPath, true); err != nil {
		t.Fatalf("runClearance: %v", err)
	}
	return out.String()
}

var (
	commented = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	earlier   = commented.Add(-time.Hour)
	later     = commented.Add(time.Hour)
)

func TestClearFlipsTheStandingReferralOnTheHead(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	out := runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 1 {
		t.Fatalf("published %d statuses, want 1", len(forge.published))
	}
	got := forge.published[0]
	if got["state"] != "success" || got["context"] != clearance.Context {
		t.Fatalf("published %+v", got)
	}
	if !strings.Contains(got["description"], "pedromvgomes") {
		t.Errorf("the clearance does not name who gave it: %q", got["description"])
	}
	if !strings.Contains(out, "clearance") {
		t.Errorf("report did not mention the clearance:\n%s", out)
	}
}

// The repository is public, so anyone may comment. Nothing a stranger writes
// may change a verdict.
func TestAStrangerChangesNoStatus(t *testing.T) {
	forge := &fakeForge{permission: "read", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "passer-by", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a stranger published %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "write access") {
		t.Errorf("the refusal does not say why: %+v", forge.comments)
	}
}

// A verdict recorded after the comment was written cannot be the one the
// person read, so their decision must not attach to it.
func TestAHeadThatMovedAfterTheCommentIsNotCleared(t *testing.T) {
	forge := &fakeForge{permission: "write", statuses: []map[string]any{statusEntry("pending", later)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("cleared a revision the commenter never read: %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "head moved") {
		t.Errorf("the refusal does not name the race: %+v", forge.comments)
	}
}

// The isolation gate is the author's to clear by splitting the change.
func TestAFailingGateIsNotClearedByComment(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("failure", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite clear", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a comment resolved a gate: %+v", forge.published)
	}
}

// Ordinary conversation must reach neither the platform nor the report as an
// action.
func TestAnOrdinaryCommentPublishesNothing(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "looks good, merging tomorrow", "pedromvgomes", commented))

	if len(forge.published) != 0 || len(forge.comments) != 0 {
		t.Fatalf("conversation caused %+v / %+v", forge.published, forge.comments)
	}
}

func TestExplainAnswersWithoutChangingTheVerdict(t *testing.T) {
	forge := &fakeForge{permission: "write", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite explain", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("explain changed a status: %+v", forge.published)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("explain posted %d comments, want 1", len(forge.comments))
	}
}

// A mistyped verb must never read as a clearance, and must not pass in
// silence either.
func TestAMistypedVerbIsAnsweredAndClearsNothing(t *testing.T) {
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite cler", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a typo cleared the change: %+v", forge.published)
	}
	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "unknown command") {
		t.Errorf("a typo went unanswered: %+v", forge.comments)
	}
}

// inCheckoutWith puts the test in a working directory shaped like the
// clearance job's own checkout of the default branch, which is where the
// declarations in force are read from. An empty document stands in for a
// repository that has declared nothing.
func inCheckoutWith(t *testing.T, exemptions string) {
	t.Helper()
	dir := t.TempDir()
	if exemptions != "" {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(referral.FileName)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, referral.FileName), []byte(exemptions), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// The proposal covers the paths nothing declared covers, and nothing else:
// the paths already under an exemption are not widened into a second entry,
// and the commenter contributes the name alone.
func TestExemptProposesAnEntryOverTheUncoveredPathsAlone(t *testing.T) {
	inCheckoutWith(t, "exemptions:\n  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed: []map[string]any{
			{"filename": "docs/one.md"},
			{"filename": "src/new.go", "status": "renamed", "previous_filename": "src/old.go"},
		},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt moved-sources", "pedromvgomes", commented))

	if len(forge.published) != 0 {
		t.Fatalf("a proposal changed a status: %+v", forge.published)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("posted %d comments, want 1", len(forge.comments))
	}
	body := forge.comments[0]
	for _, want := range []string{"exemptions:", "- name: moved-sources", referral.ReasonPlaceholderMarker,
		"      - src/new.go", "      - src/old.go"} {
		if !strings.Contains(body, want) {
			t.Errorf("the proposal is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "docs/one.md") {
		t.Errorf("a path an exemption already covers was proposed again:\n%s", body)
	}
}

// The generated entry is a draft and not an exemption: landed unedited it
// fails the parse every route to the file passes through.
func TestTheProposedEntryDoesNotParseAsAnExemption(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "docs/one.md"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt docs-only", "pedromvgomes", commented))

	block := fencedBlock(t, forge.comments[0])
	if _, err := referral.Parse([]byte(block), referral.FileName); err == nil {
		t.Fatal("the proposal parsed as a live exemption, so pasting it unedited would declare one")
	} else if !strings.Contains(err.Error(), "placeholder marker") {
		t.Errorf("the entry was rejected for something other than its unanswered reason: %v", err)
	}
}

// fencedBlock returns the one fenced block of a rendered comment, which is
// the YAML a reader copies out of it.
func fencedBlock(t *testing.T, body string) string {
	t.Helper()
	parts := strings.Split(body, "```")
	if len(parts) < 3 {
		t.Fatalf("the comment carries no fenced block:\n%s", body)
	}
	return strings.TrimPrefix(parts[1], "\n")
}

// A change every one of whose paths some exemption already covers has no
// entry to propose, and the refusal does not guess which of the things that
// can refer it anyway is doing so.
func TestExemptProposesNothingWhenEveryPathIsAlreadyCovered(t *testing.T) {
	inCheckoutWith(t, "exemptions:\n"+
		"  - name: docs\n    reason: prose only\n    paths: [\"docs/**\"]\n"+
		"  - name: sources\n    reason: code only\n    paths: [\"src/**\"]\n")
	forge := &fakeForge{
		permission: "write",
		statuses:   []map[string]any{statusEntry("pending", earlier)},
		changed:    []map[string]any{{"filename": "docs/one.md"}, {"filename": "src/a.go"}},
	}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt everything", "pedromvgomes", commented))

	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "already covered") {
		t.Fatalf("the refusal does not say why there is nothing to propose: %+v", forge.comments)
	}
	if strings.Contains(forge.comments[0], "exemptions:") {
		t.Errorf("an entry was proposed anyway:\n%s", forge.comments[0])
	}
}

// The shape names the entry, and a command without one is answered rather
// than guessed at.
func TestExemptWithoutAShapeIsAnswered(t *testing.T) {
	inCheckoutWith(t, "")
	forge := &fakeForge{permission: "admin", statuses: []map[string]any{statusEntry("pending", earlier)}}
	forge.start(t)

	runClearanceCmd(t, eventFile(t, "/lydite exempt", "pedromvgomes", commented))

	if len(forge.comments) != 1 || !strings.Contains(forge.comments[0], "unknown command") {
		t.Fatalf("a command with no shape went unanswered: %+v", forge.comments)
	}
}

func TestClearanceNeedsAnEventPayload(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	err := runClearance(context.Background(), cmd, "", true)
	if err == nil || !strings.Contains(err.Error(), "event payload") {
		t.Fatalf("err = %v, want a message naming the missing payload", err)
	}
}

func TestPublishNeedsThePlatformsEnvironmentRatherThanSkipping(t *testing.T) {
	for _, missing := range []string{"GITHUB_TOKEN", "GITHUB_REPOSITORY", "GITHUB_EVENT_PATH"} {
		t.Run("without "+missing, func(t *testing.T) {
			t.Setenv("GITHUB_TOKEN", "token")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_REPOSITORY", "lydite/lydite")
			t.Setenv("GITHUB_EVENT_PATH", filepath.Join(t.TempDir(), "absent.json"))
			t.Setenv(missing, "")
			if missing == "GITHUB_TOKEN" {
				t.Setenv("GH_TOKEN", "")
			}
			if _, err := resolveTarget("--publish", ""); err == nil {
				t.Fatalf("publishing without %s was accepted, so a run could report success having posted nothing", missing)
			}
		})
	}
}

func TestStateForKeepsAReferralDistinctFromAFailure(t *testing.T) {
	if stateFor(ui.VerdictRefer) == stateFor(ui.VerdictFail) {
		t.Fatal("a referral and a gate failure publish the same state, so a person cannot tell them apart")
	}
	if got := stateFor(ui.VerdictRefer); got != clearance.StatePending {
		t.Fatalf("a referral publishes %q, want pending", got)
	}
}

// A pending status renders as a yellow dot, which is what a job still
// running looks like. The description is the only thing that separates them.
func TestStatusDescriptionNamesTheWayForward(t *testing.T) {
	got := describe(referral.Decision{Referred: true}, ui.VerdictRefer)
	if !strings.Contains(got, "/lydite clear") {
		t.Fatalf("description %q does not say what resolves it", got)
	}
}

func TestPublishedDescriptionsFitThePlatformsLimit(t *testing.T) {
	d := referral.Decision{Referred: true, Exemption: "readme-only"}
	for _, verdict := range []ui.Verdict{ui.VerdictRefer, ui.VerdictFail, ui.VerdictPass} {
		got := describe(d, verdict)
		if n := len([]rune(got)); n == 0 || n > 140 {
			t.Errorf("%s description is %d characters: %q", verdict, n, got)
		}
	}
}
