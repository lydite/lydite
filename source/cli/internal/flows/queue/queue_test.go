package queueflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/flow"
	"lydite/lydite/internal/relay"
	queuestages "lydite/lydite/internal/stages/queue"
)

const queueMintURL = "https://token.example/idtoken?api-version=2.0"

// queueDoer answers both endpoints the queue flow talks to and keeps every
// request, so what is sent is asserted rather than inferred from an error.
type queueDoer struct {
	requests []*http.Request
	bodies   []string
	// status and answer are what the relay's own endpoint replies.
	status int
	answer string
	// tokenStatus and tokenAnswer are what the mint endpoint replies.
	tokenStatus int
	tokenAnswer string
}

func (d *queueDoer) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	d.requests = append(d.requests, req)
	d.bodies = append(d.bodies, body)

	code, reply := d.tokenStatus, d.tokenAnswer
	if strings.HasSuffix(req.URL.Path, relay.MergeGroupRoute) {
		code, reply = d.status, d.answer
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(reply)),
		Header:     http.Header{},
	}, nil
}

func newQueueDoer() *queueDoer {
	return &queueDoer{
		status:      http.StatusOK,
		answer:      `{"state":"success","description":"cleared by @octocat at 1a2b3c4d5e6f"}`,
		tokenStatus: http.StatusOK,
		tokenAnswer: `{"value":"an-oidc-token"}`,
	}
}

// queueEventFile writes payload to a file and returns its path.
func queueEventFile(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "merge-group.json")
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// queueEvent writes a merge_group payload naming one entry on the given
// revision.
func queueEvent(t *testing.T, number int, headSHA, baseBranch string) string {
	t.Helper()
	return queueEventFile(t, fmt.Sprintf(`{
	  "action": "checks_requested",
	  "merge_group": {
	    "head_sha": %q,
	    "head_ref": "refs/heads/gh-readonly-queue/%s/pr-%d-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_sha": "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
	    "base_ref": "refs/heads/%s"
	  },
	  "repository": {"full_name": "vipengele/typescript"}
	}`, headSHA, baseBranch, number, baseBranch))
}

// queueFixtureRepo is a git repository on main holding baseFiles in one
// commit and headFiles in the next, checked out at the second. It returns the
// directory and the base commit.
func queueFixtureRepo(t *testing.T, baseFiles, headFiles map[string]string) (string, string) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		r := executil.RunQuiet(ctx, dir, "git", args...)
		if !r.Ok() {
			t.Fatalf("git %v: %v\n%s", args, r.Err, r.Output)
		}
		return strings.TrimSpace(r.Output)
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, body := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	write(baseFiles)
	run("add", "-A")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write(headFiles)
	run("add", "-A")
	run("commit", "-m", "head")
	return dir, base
}

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

// A run over a change no exemption covers mints a token and submits the
// comparison, and the answer the relay gave is what SubmitComparisonOut
// reads back.
func TestARunMintsAndSubmitsTheComparison(t *testing.T) {
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	headSHA := "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567"
	eventPath := queueEvent(t, 69, headSHA, "main")
	doer := newQueueDoer()

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{
		Dir:       dir,
		EventPath: eventPath,
		Base:      base,
		Relay:     "https://relay.example",
		MintURL:   queueMintURL,
		MintToken: "the-mint-token",
		Client:    doer,
	}
	result, err := f.Run(context.Background(), params.Inputs())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	out, err := flow.Output[queuestages.SubmitComparisonOut](result, StageSubmitComparison)
	if err != nil {
		t.Fatalf("reading the submit-comparison output: %v", err)
	}
	want := relay.Answer{State: "success", Description: "cleared by @octocat at 1a2b3c4d5e6f"}
	if out.Answer != want {
		t.Errorf("Answer = %+v, want %+v", out.Answer, want)
	}

	if len(doer.requests) != 2 {
		t.Fatalf("requests = %d, want the mint then the submission", len(doer.requests))
	}
	if strings.HasSuffix(doer.requests[0].URL.Path, relay.MergeGroupRoute) {
		t.Errorf("first request = %s, want the mint endpoint", doer.requests[0].URL)
	}
	if !strings.HasSuffix(doer.requests[1].URL.Path, relay.MergeGroupRoute) {
		t.Errorf("second request = %s, want the merge-group route", doer.requests[1].URL)
	}
}

// A job that was not granted id-token: write has nothing to present, and no
// request reaches the relay at all — the base resolved and the decision
// recomputed cleanly, so this is the only thing wrong.
func TestARunWithNoMintEndpointFailsBeforeAnyRequest(t *testing.T) {
	dir, base := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	eventPath := queueEvent(t, 69, "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567", "main")
	doer := newQueueDoer()

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{
		Dir:       dir,
		EventPath: eventPath,
		Base:      base,
		Relay:     "https://relay.example",
		Client:    doer,
	}
	_, err = f.Run(context.Background(), params.Inputs())
	if !errors.Is(err, relay.ErrNoMintEndpoint) {
		t.Fatalf("err = %v, want relay.ErrNoMintEndpoint", err)
	}
	if len(doer.requests) != 0 {
		t.Errorf("requests = %d, want none", len(doer.requests))
	}
}

// A base the checkout cannot resolve fails the run before the mint endpoint
// is ever reached, even though it too is missing here: resolve-base is
// declared first, and fails for its own reason first.
func TestARunWithAnUnresolvableBaseFailsBeforeAnyRequest(t *testing.T) {
	dir, _ := queueFixtureRepo(t,
		map[string]string{"README.md": "hello"},
		map[string]string{"src/auth.go": "package src"},
	)
	eventPath := queueEvent(t, 69, "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567", "main")
	doer := newQueueDoer()

	f, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := Params{
		Dir:       dir,
		EventPath: eventPath,
		Base:      "1111111111111111111111111111111111111111",
		Relay:     "https://relay.example",
		Client:    doer,
	}
	_, err = f.Run(context.Background(), params.Inputs())
	if err == nil {
		t.Fatal("Run: want an error, got nil")
	}
	var stageErr *flow.StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("Run error is %T, want *flow.StageError", err)
	}
	if stageErr.Stage != StageResolveBase {
		t.Errorf("stageErr.Stage = %q, want %q", stageErr.Stage, StageResolveBase)
	}
	if len(doer.requests) != 0 {
		t.Errorf("requests = %d, want none", len(doer.requests))
	}
}
