package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The platform's mint endpoint and the token authorising a mint, as Actions
// provides them to a job granted id-token: write.
const (
	mintURL   = "https://token.example/idtoken?api-version=2.0"
	mintToken = "the-mint-token"
)

// fakeRelay answers both endpoints this package talks to and keeps every
// request, so what is sent is asserted rather than inferred from an error.
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
// say — and both leave the caller with no verdict, which is the whole of what
// has to be reported.
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
	if strings.HasSuffix(req.URL.Path, MergeGroupRoute) {
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

// A job without id-token: write has nothing to present, and is told so here
// rather than by a 401 from the relay. No token comes back either: a caller
// reading the value alongside the error would present an unminted string as a
// bearer token, which the relay would answer 401 to rather than naming the
// missing permission.
func TestTheMintRefusesAMissingEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name              string
		endpoint, request string
	}{
		{"no endpoint", "", mintToken},
		{"no token authorising the mint", mintURL, ""},
		{"neither", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			token, err := MintIDToken(context.Background(), relay, tc.endpoint, tc.request, "https://relay.example")
			if token != "" || !errors.Is(err, ErrNoMintEndpoint) {
				t.Errorf("MintIDToken = %q, %v; want no token and ErrNoMintEndpoint", token, err)
			}
			if len(relay.requests) != 0 {
				t.Errorf("requests = %d, want nothing attempted", len(relay.requests))
			}
		})
	}
}

// A mint that answers anything but a token is a run with nothing to present:
// the relay would answer 401 to an empty bearer, which says the request was
// refused rather than that the token was never minted.
func TestTheMintRefusesAnAnswerNamingNoToken(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		answer string
	}{
		{"a refusal", http.StatusForbidden, `{"error":"no"}`},
		{"a 200 naming no token", http.StatusOK, `{}`},
		{"a 200 that does not parse", http.StatusOK, `not a document`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.tokenStatus, relay.tokenAnswer = tc.status, tc.answer

			if token, err := MintIDToken(context.Background(), relay, mintURL, mintToken, "https://relay.example"); token != "" || err == nil {
				t.Errorf("MintIDToken = %q, %v; want no token and a refusal", token, err)
			}
		})
	}
}

// The mint fails short of answering in two ways, and neither may hand back a
// token: a job that presented an unminted one would be told 401 by the relay,
// which says the request was refused rather than that no token was ever held.
func TestTheMintFailsWhenItNeverAnswers(t *testing.T) {
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

			if token, err := MintIDToken(context.Background(), relay, mintURL, mintToken, "https://relay.example"); token != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("MintIDToken = %q, %v; want no token and a refusal naming %q", token, err, tc.want)
			}
		})
	}
}

// The mint is a GET carrying the mint token as its bearer, against the
// platform's endpoint with its own query kept and the audience added to it.
func TestTheMintIsAuthorisedByTheMintToken(t *testing.T) {
	relay := newFakeRelay()

	token, err := MintIDToken(context.Background(), relay, mintURL, mintToken, "https://relay.example")
	if err != nil {
		t.Fatal(err)
	}
	if token != "an-oidc-token" {
		t.Errorf("token = %q, want the value the endpoint answered", token)
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
}

// An endpoint that composes no URL is refused where it is composed: the
// audience would otherwise be appended to a string nothing can send, and the
// caller would report the mint as having answered nothing rather than as never
// having been asked.
func TestTheMintRefusesAnEndpointThatComposesNoURL(t *testing.T) {
	relay := newFakeRelay()

	token, err := MintIDToken(context.Background(), relay, mintURL+"\n", mintToken, "https://relay.example")
	if token != "" || err == nil || !strings.Contains(err.Error(), "composing the token request") {
		t.Fatalf("MintIDToken = %q, %v; want a refusal naming the composition", token, err)
	}
	if len(relay.requests) != 0 {
		t.Errorf("requests = %d, want nothing attempted", len(relay.requests))
	}
}

// The audience is escaped into the mint query rather than concatenated, so an
// origin carrying anything a query reserves names one audience and not two.
func TestTheMintQueryEscapesTheAudience(t *testing.T) {
	relay := newFakeRelay()

	if _, err := MintIDToken(context.Background(), relay, mintURL, mintToken, "https://relay.example/a&b=c"); err != nil {
		t.Fatal(err)
	}
	if got := relay.requests[0].URL.Query().Get("audience"); got != "https://relay.example/a&b=c" {
		t.Errorf("audience = %q, want the origin whole", got)
	}
	if !strings.Contains(relay.requests[0].URL.RawQuery, url.QueryEscape("https://relay.example/a&b=c")) {
		t.Errorf("raw query = %q, want the audience escaped", relay.requests[0].URL.RawQuery)
	}
}

// The route and the origin are composed rather than assembled by hand at the
// call site, so a trailing slash on the configured origin does not become a
// path the relay has no route for.
func TestTheSubmissionURLToleratesATrailingSlash(t *testing.T) {
	req, err := newMergeGroupRequest(context.Background(), "https://relay.example/", "token", MergeGroupRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.String(); got != "https://relay.example"+MergeGroupRoute {
		t.Errorf("url = %q", got)
	}
}

// The body is the relay's contract, byte for byte: its field names, their
// order, and a false Referred spelled out rather than omitted — the relay reads
// an absent field as a submission that did not say, which is the comparison.
func TestTheSubmissionBodyIsTheRelaysContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  MergeGroupRequest
		want string
	}{
		{
			"a referred entry",
			MergeGroupRequest{
				QueueRef:    "refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
				PullRequest: 69,
				SHA:         "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",
				BaseSHA:     "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
				Fingerprint: "sha256:0f1e2d3c",
				Referred:    true,
			},
			`{"queue_ref":"refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",` +
				`"pull_request":69,"sha":"9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",` +
				`"base_sha":"1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d","fingerprint":"sha256:0f1e2d3c","referred":true}`,
		},
		{
			"an entry referring for nothing",
			MergeGroupRequest{
				QueueRef:    "refs/heads/gh-readonly-queue/main/pr-12-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
				PullRequest: 12,
				SHA:         "add1add1add1add1add1add1add1add1add1add1",
				BaseSHA:     "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
				Fingerprint: "sha256:e3b0c442",
			},
			`{"queue_ref":"refs/heads/gh-readonly-queue/main/pr-12-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",` +
				`"pull_request":12,"sha":"add1add1add1add1add1add1add1add1add1add1",` +
				`"base_sha":"1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d","fingerprint":"sha256:e3b0c442","referred":false}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()

			answer, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if answer.State != "success" || answer.Description != "cleared by @octocat at 1a2b3c4d5e6f" {
				t.Errorf("answer = %+v, want what the relay answered", answer)
			}
			if len(relay.bodies) != 1 {
				t.Fatalf("requests = %d, want the submission alone", len(relay.bodies))
			}
			if relay.bodies[0] != tc.want {
				t.Errorf("body =\n%s\nwant\n%s", relay.bodies[0], tc.want)
			}
		})
	}
}

// The submission is a POST to the route, carrying the token it was given as
// its bearer, so a token minted for the relay's own audience is what the relay
// verifies.
func TestTheSubmissionIsAuthorisedWithTheTokenItWasGiven(t *testing.T) {
	relay := newFakeRelay()

	if _, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", MergeGroupRequest{}); err != nil {
		t.Fatal(err)
	}
	submission := relay.requests[0]
	if submission.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", submission.Method)
	}
	if got := submission.URL.String(); got != "https://relay.example"+MergeGroupRoute {
		t.Errorf("url = %q, want the relay's merge-group route", got)
	}
	if got := submission.Header.Get("authorization"); got != "Bearer an-oidc-token" {
		t.Errorf("authorization = %q, want the token it was given", got)
	}
	if got := submission.Header.Get("content-type"); got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

// Nothing falls back, because there is nothing to fall back to: the queue job
// holds no token that could publish a status. A refused submission is "no
// verdict was published", and the error carries the answer's own status code
// and the relay's own reason.
func TestTheSubmissionFailsWhenTheRelayPublishesNothing(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusConflict, http.StatusBadGateway} {
		relay := newFakeRelay()
		relay.status = code
		relay.answer = `{"error":"refused"}`

		answer, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", MergeGroupRequest{})
		if err == nil {
			t.Fatalf("a %d answer must fail, got %+v", code, answer)
		}
		want := fmt.Sprintf("the relay answered %d and published no verdict: refused", code)
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err, want)
		}
		if answer != (Answer{}) {
			t.Errorf("answer = %+v alongside a refusal, want none", answer)
		}
	}
}

// An answer that does not parse leaves its own text as the whole of what the
// relay said, so the error reports that rather than nothing.
func TestTheSubmissionReportsAnUnparsedRefusalVerbatim(t *testing.T) {
	relay := newFakeRelay()
	relay.status = http.StatusBadGateway
	relay.answer = "  upstream went away\n"

	_, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", MergeGroupRequest{})
	if err == nil || err.Error() != "the relay answered 502 and published no verdict: upstream went away" {
		t.Errorf("error = %v, want the answer's own text", err)
	}
}

// A 200 naming no state says nothing about what was published, and a caller
// that reported it as a verdict would be a green step nobody wrote.
func TestTheSubmissionFailsOnAnAnswerNamingNoState(t *testing.T) {
	relay := newFakeRelay()
	relay.answer = `{"description":"something"}`

	answer, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", MergeGroupRequest{})
	if err == nil || err.Error() != "the relay answered 200 naming no state, so nothing says what was published" {
		t.Fatalf("SubmitMergeGroup = %+v, %v; want a refusal naming the missing state", answer, err)
	}
}

// An unreachable relay and an answer that stops mid-read are both "no verdict
// was published", and neither is a status code.
func TestTheSubmissionFailsWhenItNeverCompletes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode failMode
		want string
	}{
		{"an unreachable relay", transportFails, "submitting the comparison to https://relay.example" + MergeGroupRoute},
		{"an answer that stops part way", bodyFails, "reading the relay's answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.fails = tc.mode

			_, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example", "an-oidc-token", MergeGroupRequest{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// An origin is a string a workflow passes, so one that composes no URL is
// caller-reachable input. It is refused where it is composed rather than
// reaching the transport as a request nothing could send.
func TestTheSubmissionRefusesAnOriginThatComposesNoURL(t *testing.T) {
	relay := newFakeRelay()

	answer, err := SubmitMergeGroup(context.Background(), relay, "https://relay.example\n", "an-oidc-token", MergeGroupRequest{})
	if err == nil || !strings.Contains(err.Error(), "composing the request to the relay") {
		t.Fatalf("SubmitMergeGroup = %+v, %v; want a refusal naming the composition", answer, err)
	}
	if len(relay.requests) != 0 {
		t.Errorf("requests = %d, want nothing attempted", len(relay.requests))
	}
}
