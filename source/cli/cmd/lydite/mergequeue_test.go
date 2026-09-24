package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// fakeRelay answers both endpoints the queue path talks to and keeps every
// request, so what is submitted is asserted rather than inferred from an exit
// code.
type fakeRelay struct {
	requests []*http.Request
	bodies   []string
	// status and answer are what the relay's own endpoint replies.
	status int
	answer string
	// tokenStatus and tokenAnswer are what the Actions token endpoint replies.
	tokenStatus int
	tokenAnswer string
	// fails and tokenFails are how each endpoint fails short of answering.
	fails      failMode
	tokenFails failMode
}

// failMode is how the fake fails rather than what it answers.
//
// A transport that never completed and a body that stops part way through are
// neither of them a status code, so neither is something an answer document can
// say — and both leave the run with no verdict, which is the whole of what this
// path has to report.
type failMode int

const (
	// answers is the endpoint replying, whatever it replies.
	answers failMode = iota
	// transportFails is a request that never completed.
	transportFails
	// bodyFails is a response whose body errors part way through the read.
	bodyFails
)

// brokenBody is a response body that errors rather than ending.
type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) {
	return 0, fmt.Errorf("the connection dropped before the answer ended")
}

func (f *fakeRelay) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, body)

	code, reply, mode := f.tokenStatus, f.tokenAnswer, f.tokenFails
	if strings.HasSuffix(req.URL.Path, queueRoute) {
		code, reply, mode = f.status, f.answer, f.fails
	}
	if mode == transportFails {
		return nil, fmt.Errorf("dial tcp: connection refused")
	}
	var content io.Reader = strings.NewReader(reply)
	if mode == bodyFails {
		content = brokenBody{}
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(content),
		Header:     http.Header{},
	}, nil
}

func newFakeRelay() *fakeRelay {
	return &fakeRelay{
		status:      http.StatusOK,
		answer:      `{"state":"success","description":"cleared by @octocat at 1a2b3c4d5e6f"}`,
		tokenStatus: http.StatusOK,
		tokenAnswer: `{"value":"an-oidc-token"}`,
		fails:       answers,
		tokenFails:  answers,
	}
}

// queueEvent writes a merge_group payload naming one entry on the given
// revision.
func queueEvent(t *testing.T, number int, headSHA, baseBranch string) string {
	t.Helper()
	payload := fmt.Sprintf(`{
	  "action": "checks_requested",
	  "merge_group": {
	    "head_sha": %q,
	    "head_ref": "refs/heads/gh-readonly-queue/%s/pr-%d-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_sha": "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_ref": "refs/heads/%s"
	  },
	  "repository": {"full_name": "vipengele/typescript"}
	}`, headSHA, baseBranch, number, baseBranch)
	path := filepath.Join(t.TempDir(), "merge-group.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runQueueCmd drives the command against a fake transport, with the mint
// endpoint the platform sets for a job granted id-token: write.
func runQueueCmd(t *testing.T, relay *fakeRelay, dir, base, eventPath string) (string, error) {
	t.Helper()
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://token.example/idtoken?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "the-mint-token")
	var out bytes.Buffer
	cmd := newQueueCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runQueue(context.Background(), cmd, relay, queueOptions{
		dir:       dir,
		eventPath: eventPath,
		relay:     "https://relay.example",
		base:      base,
		noColor:   true,
	})
	return out.String(), err
}

// submittedRequest is the queue submission's decoded body.
func submittedRequest(t *testing.T, relay *fakeRelay) queueRequest {
	t.Helper()
	for i, req := range relay.requests {
		if !strings.HasSuffix(req.URL.Path, queueRoute) {
			continue
		}
		var body queueRequest
		if err := json.Unmarshal([]byte(relay.bodies[i]), &body); err != nil {
			t.Fatalf("the submitted body does not parse: %v\n%s", err, relay.bodies[i])
		}
		return body
	}
	t.Fatalf("nothing was submitted to %s", queueRoute)
	return queueRequest{}
}

// The submission names the entry, the revision a verdict is published against
// and the fingerprint of the reasons the recomputed decision refers on. None of
// it is authority — the relay resolves each against the claim it verified — so
// what this pins is that the comparison the relay is asked to make is the one
// the ADR describes.
func TestQueueSubmitsTheRecomputedFingerprint(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	relay := newFakeRelay()
	event := queueEvent(t, 69, "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567", "main")

	out, err := runQueueCmd(t, relay, dir, base, event)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	body := submittedRequest(t, relay)
	decision, err := queueDecision(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	want := referral.Fingerprint(decision.Uncovered, decision.Disqualifications)
	if body.Fingerprint != want {
		t.Errorf("Fingerprint = %q, want %q — the reasons this entry refers on", body.Fingerprint, want)
	}
	if body.PullRequest != 69 {
		t.Errorf("PullRequest = %d, want the pull request the queue ref names", body.PullRequest)
	}
	if body.SHA != "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567" {
		t.Errorf("SHA = %q, want the revision the queue built", body.SHA)
	}
	if !strings.Contains(body.QueueRef, "gh-readonly-queue/main/pr-69-") {
		t.Errorf("QueueRef = %q, want the ref the payload carries", body.QueueRef)
	}
	if body.BaseSHA != base {
		t.Errorf("BaseSHA = %q, want the revision the decision was recomputed against", body.BaseSHA)
	}
	// The repository is nowhere in the body: the relay takes it from the
	// verified claim, so nothing here could name another one.
	if strings.Contains(relay.bodies[len(relay.bodies)-1], "vipengele") {
		t.Errorf("the submission names a repository:\n%s", relay.bodies[len(relay.bodies)-1])
	}
}

// Whether the decision refers at all travels with the fingerprint, because a
// fingerprint alone cannot say it: a change that refers for nothing has no
// clearance to be compared against, so an entry submitting only a fingerprint
// would wait on a clearance nobody can give. The commonest change there is —
// one every exemption already covers — is exactly that shape.
func TestQueueSaysWhenTheRecomputedDecisionDoesNotRefer(t *testing.T) {
	covering := "exemptions:\n  - name: documentation\n    reason: prose changes no behaviour\n    paths: [\"docs/**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello", referral.FileName: covering},
		map[string]string{"docs/guide.md": "a paragraph"},
	)
	relay := newFakeRelay()

	out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 12, "add1add1add1add1add1add1add1add1add1add1", "main"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	decision, err := queueDecision(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Referred {
		t.Fatalf("the fixture refers, so it says nothing about an unreferred entry: %+v", decision)
	}
	if body := submittedRequest(t, relay); body.Referred {
		t.Error("Referred = true for a change every exemption covers, so the relay waits on a clearance nobody can give")
	}
	// Spelled out in the document and not merely absent from it: the relay reads
	// an absent field as a submission that did not say, which is the comparison.
	if !strings.Contains(relay.bodies[len(relay.bodies)-1], `"referred":false`) {
		t.Errorf("the submission does not say the decision refers for nothing:\n%s", relay.bodies[len(relay.bodies)-1])
	}
	if !strings.Contains(out, "no referral") {
		t.Errorf("the report does not say the decision refers for nothing:\n%s", out)
	}
}

// A referred entry still says so, which is what keeps the field from reading as
// "unreferred unless proven otherwise" on the one shape a clearance exists for.
func TestQueueSaysWhenTheRecomputedDecisionRefers(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	relay := newFakeRelay()

	out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 12, "add1add1add1add1add1add1add1add1add1add1", "main"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if body := submittedRequest(t, relay); !body.Referred {
		t.Error("Referred = false for a change no exemption covers, so the entry would merge unattended")
	}
}

// The request is composed as the relay's own contract: a POST to the route,
// carrying the OIDC token minted for the relay's origin as its audience, so a
// token this run presents cannot be replayed against another service.
func TestQueueSubmissionIsAuthorisedWithATokenMintedForTheRelay(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	relay := newFakeRelay()

	if out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 4, "cafe1234cafe1234cafe1234cafe1234cafe1234", "main")); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	if len(relay.requests) != 2 {
		t.Fatalf("requests = %d, want the mint and the submission", len(relay.requests))
	}
	mint := relay.requests[0]
	if mint.Method != http.MethodGet || mint.Header.Get("authorization") != "Bearer the-mint-token" {
		t.Errorf("the mint request is %s with %q", mint.Method, mint.Header.Get("authorization"))
	}
	if got := mint.URL.Query().Get("audience"); got != "https://relay.example" {
		t.Errorf("audience = %q, want the relay's own origin", got)
	}
	if got := mint.URL.Query().Get("api-version"); got != "2.0" {
		t.Errorf("api-version = %q, want the query the platform's endpoint already carries", got)
	}
	submission := relay.requests[1]
	if submission.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", submission.Method)
	}
	if got := submission.URL.String(); got != "https://relay.example"+queueRoute {
		t.Errorf("url = %q, want the relay's queue route", got)
	}
	if got := submission.Header.Get("authorization"); got != "Bearer an-oidc-token" {
		t.Errorf("authorization = %q, want the minted token", got)
	}
	if got := submission.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

// The whole of the ADR's equivalence: the same change replayed onto a moved base
// refers for the same reasons, so it fingerprints the same and the clearance
// carries forward. A fingerprint over the tree, or over the revision, would
// differ here and the entry would be stuck.
func TestTheSameChangeOnAMovedBaseFingerprintsTheSame(t *testing.T) {
	first, firstBase := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	moved, movedBase := reviewRepo(t,
		map[string]string{"README.md": "hello", "docs/other.md": "somebody else's merge"},
		map[string]string{"src/auth.go": "package src"},
	)

	ctx := context.Background()
	before, err := queueDecision(ctx, first, firstBase)
	if err != nil {
		t.Fatal(err)
	}
	after, err := queueDecision(ctx, moved, movedBase)
	if err != nil {
		t.Fatal(err)
	}
	want := referral.Fingerprint(before.Uncovered, before.Disqualifications)
	if got := referral.Fingerprint(after.Uncovered, after.Disqualifications); got != want {
		t.Errorf("fingerprint = %q, want %q — the reasons are unchanged, so the clearance carries", got, want)
	}
}

// A change referring for different reasons fingerprints differently, which is
// the half that makes the comparison worth making.
func TestADifferentReasonFingerprintsDifferently(t *testing.T) {
	ctx := context.Background()
	one, oneBase := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	two, twoBase := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/billing.go": "package src"},
	)

	first, err := queueDecision(ctx, one, oneBase)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queueDecision(ctx, two, twoBase)
	if err != nil {
		t.Fatal(err)
	}
	if referral.Fingerprint(first.Uncovered, first.Disqualifications) ==
		referral.Fingerprint(second.Uncovered, second.Disqualifications) {
		t.Error("two changes uncovering different paths fingerprint alike, so no comparison could tell them apart")
	}
}

// The exemptions come from the base commit, never from the queue's own tree: an
// entry carrying a widening of the allowlist must get no benefit from it, the
// property Decide already always reads the file from the merge-base for.
func TestQueueReadsExemptionsFromTheBaseNotTheQueuedTree(t *testing.T) {
	selfServing := "exemptions:\n  - name: anything\n    reason: it is fine, trust me\n    paths: [\"**\"]\n"
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{referral.FileName: selfServing, "src/auth.go": "package src"},
	)

	decision, err := queueDecision(context.Background(), dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Referred {
		t.Error("a queued change that exempts itself is not exempt")
	}
	if len(decision.Uncovered) == 0 {
		t.Error("the recomputed decision names no uncovered path, so its fingerprint would read as fully exempt")
	}
}

// A pending answer is the mechanism working: the reasons moved, so the entry
// drops out of the queue and the author clears the pull request again. Failing
// the run would report that as a broken pipeline.
func TestQueuePendingIsNotAFailingRun(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	relay := newFakeRelay()
	relay.answer = `{"state":"pending","description":"the decision is not the one that was cleared"}`

	out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "not the one that was cleared") {
		t.Errorf("the report does not say what the relay answered:\n%s", out)
	}
}

// Nothing falls back, because there is nothing to fall back to: this job holds
// no token that could publish a status. A refused submission is "no verdict was
// published", which has to be said out loud.
func TestQueueFailsWhenTheRelayPublishesNothing(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, code := range []int{http.StatusForbidden, http.StatusConflict, http.StatusBadGateway} {
		relay := newFakeRelay()
		relay.status = code
		relay.answer = `{"error":"refused"}`

		out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
		if err == nil {
			t.Fatalf("a %d answer must fail the run:\n%s", code, out)
		}
		if !strings.Contains(err.Error(), fmt.Sprint(code)) {
			t.Errorf("error = %v, want the answer's own status code", err)
		}
	}
}

// A 200 naming no state says nothing about what was published, and a run that
// reported it as a verdict would be a green step nobody wrote.
func TestQueueFailsOnAnAnswerNamingNoState(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	relay := newFakeRelay()
	relay.answer = `{"description":"something"}`

	if out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 7, "beef1234beef1234beef1234beef1234beef1234", "main")); err == nil {
		t.Fatalf("an answer naming no state must fail the run:\n%s", out)
	}
}

// A job without id-token: write has nothing to present, and is told so here
// rather than by a 401 from the relay.
func TestQueueFailsWithoutAMintEndpoint(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	relay := newFakeRelay()

	err := runQueue(context.Background(), newQueueCmd(), relay, queueOptions{
		dir:       dir,
		base:      base,
		eventPath: queueEvent(t, 7, "beef1234beef1234beef1234beef1234beef1234", "main"),
		relay:     "https://relay.example",
		noColor:   true,
	})
	if err == nil {
		t.Fatal("a job that cannot mint an OIDC token must say so")
	}
	if len(relay.requests) != 0 {
		t.Errorf("requests = %d, want nothing attempted", len(relay.requests))
	}
	// No token comes back either. A caller reading the value alongside the
	// error would present an unminted string as a bearer token, which the
	// relay would answer 401 to rather than naming the missing permission.
	if token, err := actionsIDToken(context.Background(), relay, "https://relay.example"); token != "" || err == nil {
		t.Errorf("actionsIDToken = %q, %v; want no token and a refusal", token, err)
	}
}

// A mint that answers anything but a token is a run with nothing to present,
// and nothing is submitted with it: the relay would answer 401 to an empty
// bearer, which says the request was refused rather than that the token this
// job holds was never minted.
func TestQueueFailsWhenTheMintAnswersNoToken(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, tc := range []struct {
		name   string
		status int
		answer string
	}{
		{"a refusal", http.StatusForbidden, `{"error":"no"}`},
		{"a 200 naming no token", http.StatusOK, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.tokenStatus, relay.tokenAnswer = tc.status, tc.answer

			out, err := runQueueCmd(t, relay, dir, base, queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
			if err == nil {
				t.Fatalf("a mint answering no token must fail the run:\n%s", out)
			}
			for _, req := range relay.requests {
				if strings.HasSuffix(req.URL.Path, queueRoute) {
					t.Error("the comparison was submitted without a token to authorise it")
				}
			}
			// No token comes back either: a caller reading the value alongside
			// the error would present an unminted string as a bearer token,
			// which the relay would answer 401 to rather than naming the
			// missing permission.
			if token, err := actionsIDToken(context.Background(), relay, "https://relay.example"); token != "" || err == nil {
				t.Errorf("actionsIDToken = %q, %v; want no token and a refusal", token, err)
			}
		})
	}
}

// The state the relay published decides the row, because the two answers mean
// different things to whoever reads the log: success is the clearance carried
// forward, and anything else is an entry going back to a person.
func TestTheQueueRowFollowsTheStateTheRelayPublished(t *testing.T) {
	cleared := queueRow(queueAnswer{State: string(clearance.StateSuccess), Description: "cleared by @octocat"})
	if cleared.Status != ui.StatusPass {
		t.Errorf("status = %v for a published success, want %v", cleared.Status, ui.StatusPass)
	}
	for _, state := range []clearance.State{clearance.StatePending, clearance.StateFailure, clearance.StateError} {
		row := queueRow(queueAnswer{State: string(state), Description: "the decision is not the one that was cleared"})
		if row.Status != ui.StatusRefer {
			t.Errorf("status = %v for a %s answer, want %v", row.Status, state, ui.StatusRefer)
		}
	}
}

// Every flag the workflow passes is the command's own, reached through the
// clearance command that holds it: a registration that went missing leaves the
// invocation refused as an unknown flag, and a subcommand that was never added
// leaves it refused as an unknown command. Driving the whole command is what
// says so — runQueue taking the same values as parameters cannot.
func TestTheQueueCommandReadsEveryFlagTheWorkflowPasses(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	const headSHA = "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567"

	submitted := make(chan string, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		submitted <- string(raw)
		_, _ = w.Write([]byte(`{"state":"success","description":"cleared by @octocat"}`))
	}))
	defer relay.Close()
	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":"an-oidc-token"}`))
	}))
	defer mint.Close()
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", mint.URL+"/idtoken?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "the-mint-token")

	var out bytes.Buffer
	cmd := newClearanceCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"queue",
		"--dir", dir,
		"--base", base,
		"--event", queueEvent(t, 21, headSHA, "main"),
		"--relay", relay.URL,
		"--no-color",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v\n%s", err, out.String())
	}

	var body queueRequest
	select {
	case raw := <-submitted:
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("the submitted body does not parse: %v\n%s", err, raw)
		}
	default:
		t.Fatal("nothing was submitted, so the flags reached no submission")
	}
	if body.PullRequest != 21 || body.SHA != headSHA {
		t.Errorf("the submission names #%d at %s, want the entry --event named", body.PullRequest, body.SHA)
	}
	if body.BaseSHA != base {
		t.Errorf("BaseSHA = %q, want the revision --base named", body.BaseSHA)
	}
	if !strings.Contains(out.String(), "cleared by @octocat") {
		t.Errorf("the report does not say what the relay answered:\n%s", out.String())
	}
	// --no-color was honoured rather than merely accepted: an escape in the
	// report is colour a flag asked for the absence of.
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("the report carries colour --no-color asked to drop:\n%q", out.String())
	}
}

// The relay is the only route, so a run given none is refused before anything
// is measured rather than recomputing a decision it has nowhere to submit.
func TestQueueNeedsARelay(t *testing.T) {
	relay := newFakeRelay()
	err := runQueue(context.Background(), newQueueCmd(), relay, queueOptions{dir: t.TempDir(), base: "auto", noColor: true})
	if err == nil || !strings.Contains(err.Error(), "--relay") {
		t.Fatalf("err = %v, want a refusal naming --relay", err)
	}
}

// A payload that is not a merge_group's names no queue revision, and there is
// nothing to publish a verdict against.
func TestQueueRefusesAPayloadThatIsNotAMergeGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, []byte(`{"pull_request":{"number":9,"head":{"sha":"abc"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runQueue(context.Background(), newQueueCmd(), newFakeRelay(), queueOptions{
		dir:       t.TempDir(),
		base:      "auto",
		eventPath: path,
		relay:     "https://relay.example",
		noColor:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "merge_group") {
		t.Fatalf("err = %v, want a refusal naming the event this command answers", err)
	}
}

// The route and the origin are composed rather than assembled by hand at the
// call site, so a trailing slash on the configured origin does not become a
// path the relay has no route for.
func TestTheSubmissionURLToleratesATrailingSlash(t *testing.T) {
	req, err := newQueueRequest(context.Background(), "https://relay.example/", "token", queueRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://relay.example"+queueRoute {
		t.Errorf("url = %q", got)
	}
}

// The payload is the whole of what says which entry this run is for, so a run
// pointed at no payload and one pointed at a payload that is not there are both
// refused before anything is measured — and each says which it was, because a
// workflow that never wrote the event and one that wrote it elsewhere are
// different things to go and fix.
func TestQueueRefusesAnEventItCannotRead(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	t.Setenv("GITHUB_EVENT_PATH", "")

	err := runQueue(context.Background(), newQueueCmd(), newFakeRelay(), queueOptions{
		dir:     dir,
		base:    base,
		relay:   "https://relay.example",
		noColor: true,
	})
	if err == nil || !strings.Contains(err.Error(), "GITHUB_EVENT_PATH") {
		t.Fatalf("err = %v, want a refusal naming where the payload is looked for", err)
	}

	out, err := runQueueCmd(t, newFakeRelay(), dir, base, filepath.Join(t.TempDir(), "absent.json"))
	if err == nil || !strings.Contains(err.Error(), "reading the event payload") {
		t.Fatalf("err = %v, want a refusal naming the read\n%s", err, out)
	}
}

// A merge group naming a revision but no entry has no pull request to read a
// clearance off, and the revision alone is not something a comparison can be
// made against.
func TestQueueRefusesARefThatNamesNoEntry(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	path := filepath.Join(t.TempDir(), "merge-group.json")
	payload := `{
	  "merge_group": {
	    "head_sha": "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef",
	    "head_ref": "refs/heads/gh-readonly-queue/main",
	    "base_ref": "refs/heads/main"
	  }
	}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runQueueCmd(t, newFakeRelay(), dir, base, path)
	if err == nil {
		t.Fatalf("a ref naming no entry must fail the run:\n%s", out)
	}
}

// The base decides what the decision is recomputed over, so a base this
// checkout cannot resolve leaves nothing to fingerprint — and a run that
// submitted a fingerprint taken over the wrong diff would compare a decision
// nobody made.
func TestQueueRefusesABaseItCannotResolve(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	out, err := runQueueCmd(t, newFakeRelay(), dir,
		"1111111111111111111111111111111111111111",
		queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
	if err == nil || !strings.Contains(err.Error(), "does not name a commit") {
		t.Fatalf("err = %v, want a refusal naming the base\n%s", err, out)
	}
}

// An exemptions file the base commit carries and that does not parse states no
// exemptions and does not state that either: every condition would silently
// fail closed, fingerprinting a decision that refers for reasons the file may
// well have covered. The run is refused instead.
func TestQueueRefusesExemptionsTheBaseCannotParse(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello", ".lydite/exemptions.yml": "exemptions: ["},
		map[string]string{"src/auth.go": "package src"},
	)

	relay := newFakeRelay()
	out, err := runQueueCmd(t, relay, dir, base,
		queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
	if err == nil {
		t.Fatalf("an exemptions file that does not parse must fail the run:\n%s", out)
	}
	if len(relay.requests) != 0 {
		t.Errorf("requests = %d, want nothing submitted for a decision that was never recomputed", len(relay.requests))
	}
}

// A revision the repository does not have leaves no diff to read, and a
// decision recomputed over no diff refers for nothing — which is the answer
// that merges unattended. It is refused rather than reported.
func TestQueueDecisionRefusesADiffItCannotRead(t *testing.T) {
	dir, _ := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)

	if _, err := queueDecision(context.Background(), dir, "1111111111111111111111111111111111111111"); err == nil {
		t.Fatal("a base the repository does not have must refuse rather than recompute an empty decision")
	}
}

// --relay is a string a workflow passes, so a value that composes no URL is
// caller-reachable input and not a shape only a test can make. It is refused
// where it is composed, with the origin named, rather than reaching the
// transport as a request nothing could send.
func TestQueueRefusesAnOriginThatComposesNoURL(t *testing.T) {
	if _, err := newQueueRequest(context.Background(), "https://relay.example\n", "token", queueRequest{}); err == nil {
		t.Fatal("an origin carrying a control character composes no request, and must say so")
	}

	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://token.example/idtoken?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "the-mint-token")
	relay := newFakeRelay()
	answer, err := submitQueueComparison(context.Background(), relay, "https://relay.example\n", queueRequest{})
	if err == nil {
		t.Fatalf("answer = %+v, want the refusal newQueueRequest composed", answer)
	}
	// The mint still ran — the audience is escaped, so the origin only fails
	// where it becomes a path — and nothing was submitted with the token it
	// answered.
	if len(relay.requests) != 1 {
		t.Fatalf("requests = %d, want the mint alone", len(relay.requests))
	}
	if strings.HasSuffix(relay.requests[0].URL.Path, queueRoute) {
		t.Error("a request that could not be composed reached the relay anyway")
	}
}

// An unreachable relay and an answer that stops mid-read are both "no verdict
// was published", and neither is a status code. A run that exited 0 over either
// would report an entry as carried forward on the strength of nothing.
func TestQueueFailsWhenTheSubmissionNeverCompletes(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, tc := range []struct {
		name string
		mode failMode
		want string
	}{
		{"an unreachable relay", transportFails, "submitting the comparison"},
		{"an answer that stops part way", bodyFails, "reading the relay's answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.fails = tc.mode

			out, err := runQueueCmd(t, relay, dir, base,
				queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
			if err == nil {
				t.Fatalf("%s must fail the run:\n%s", tc.name, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// The mint fails the same three ways, and a job that presented an unminted
// token would be told 401 by the relay — which says the request was refused
// rather than that this job never held a token to send.
func TestQueueFailsWhenTheMintNeverAnswers(t *testing.T) {
	dir, base := reviewRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	for _, tc := range []struct {
		name string
		mode failMode
		want string
	}{
		{"an unreachable endpoint", transportFails, "requesting an Actions OIDC token"},
		{"an answer that stops part way", bodyFails, "reading the Actions OIDC token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.tokenFails = tc.mode

			out, err := runQueueCmd(t, relay, dir, base,
				queueEvent(t, 7, "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef", "main"))
			if err == nil {
				t.Fatalf("%s must fail the run:\n%s", tc.name, out)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
			for _, req := range relay.requests {
				if strings.HasSuffix(req.URL.Path, queueRoute) {
					t.Error("the comparison was submitted without a token to authorise it")
				}
			}
		})
	}
}

// The mint endpoint is the platform's own, read from the environment, and an
// environment naming one that composes no URL is refused where it is composed:
// the audience would otherwise be appended to a string nothing can send, and
// the run would report the mint as having answered nothing rather than as
// never having been asked.
func TestTheMintRefusesAnEndpointThatComposesNoURL(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://token.example/idtoken?api-version=2.0\n")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "the-mint-token")
	relay := newFakeRelay()

	token, err := actionsIDToken(context.Background(), relay, "https://relay.example")
	if token != "" || err == nil || !strings.Contains(err.Error(), "composing the token request") {
		t.Fatalf("actionsIDToken = %q, %v; want a refusal naming the composition", token, err)
	}
	if len(relay.requests) != 0 {
		t.Errorf("requests = %d, want nothing attempted", len(relay.requests))
	}
}

// The audience is escaped into the mint query rather than concatenated, so an
// origin carrying anything a query reserves names one audience and not two.
func TestTheMintQueryEscapesTheAudience(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://token.example/idtoken?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "the-mint-token")
	relay := newFakeRelay()

	if _, err := actionsIDToken(context.Background(), relay, "https://relay.example/a&b=c"); err != nil {
		t.Fatal(err)
	}
	if got := relay.requests[0].URL.Query().Get("audience"); got != "https://relay.example/a&b=c" {
		t.Errorf("audience = %q, want the origin whole", got)
	}
	if !strings.Contains(relay.requests[0].URL.RawQuery, url.QueryEscape("https://relay.example/a&b=c")) {
		t.Errorf("raw query = %q, want the audience escaped", relay.requests[0].URL.RawQuery)
	}
}
