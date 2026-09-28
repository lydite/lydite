package queuestages

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"lydite/lydite/internal/relay"
)

const queueMintURL = "https://token.example/idtoken?api-version=2.0"

// A job that was not granted id-token: write has nothing to present, and is
// told so before anything is requested.
func TestMintTokenRefusesAMissingEndpoint(t *testing.T) {
	for name, in := range map[string]MintTokenIn{
		"no endpoint":   {MintToken: "the-mint-token"},
		"no mint token": {MintURL: queueMintURL},
		"neither":       {},
	} {
		t.Run(name, func(t *testing.T) {
			doer := newQueueDoer()
			in.Client = doer
			in.Relay = "https://relay.example"

			_, err := MintToken(context.Background(), in)
			if !errors.Is(err, relay.ErrNoMintEndpoint) {
				t.Fatalf("err = %v, want relay.ErrNoMintEndpoint", err)
			}
			if err.Error() != relay.ErrNoMintEndpoint.Error() {
				t.Errorf("err = %q, want it unwrapped: %q", err, relay.ErrNoMintEndpoint)
			}
			if len(doer.requests) != 0 {
				t.Errorf("requests = %d, want none", len(doer.requests))
			}
		})
	}
}

// The token is minted for the relay's own origin, authorised by the mint token.
func TestMintTokenMintsForTheRelaysOrigin(t *testing.T) {
	doer := newQueueDoer()

	out, err := MintToken(context.Background(), MintTokenIn{
		Client:    doer,
		MintURL:   queueMintURL,
		MintToken: "the-mint-token",
		Relay:     "https://relay.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Token != "an-oidc-token" {
		t.Errorf("Token = %q, want the minted token", out.Token)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("requests = %d, want the mint alone", len(doer.requests))
	}
	mint := doer.requests[0]
	if got := mint.URL.Query().Get("audience"); got != "https://relay.example" {
		t.Errorf("audience = %q, want the relay's own origin", got)
	}
	if got := mint.Header.Get("authorization"); got != "Bearer the-mint-token" {
		t.Errorf("authorization = %q, want the mint token", got)
	}
}

// A mint endpoint that answers anything but 200 mints nothing.
func TestMintTokenFailsWhenTheEndpointRefuses(t *testing.T) {
	doer := newQueueDoer()
	doer.tokenStatus = http.StatusForbidden

	_, err := MintToken(context.Background(), MintTokenIn{
		Client: doer, MintURL: queueMintURL, MintToken: "the-mint-token", Relay: "https://relay.example",
	})
	if want := "the Actions token endpoint answered 403"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func submitIn(doer *queueDoer) SubmitComparisonIn {
	return SubmitComparisonIn{
		Client:      doer,
		Relay:       "https://relay.example",
		Token:       "an-oidc-token",
		HeadRef:     "refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",
		PullRequest: 69,
		HeadSHA:     "9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",
		BaseSHA:     "0123456789abcdef0123456789abcdef01234567",
		Fingerprint: "sha256:abc",
		Referred:    true,
	}
}

// The body is the relay's contract, byte for byte, and the answer the relay
// gave is what comes back.
func TestSubmitComparisonSubmitsTheRelaysContract(t *testing.T) {
	doer := newQueueDoer()

	out, err := SubmitComparison(context.Background(), submitIn(doer))
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("requests = %d, want the submission alone", len(doer.requests))
	}
	want := `{"queue_ref":"refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",` +
		`"pull_request":69,"sha":"9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",` +
		`"base_sha":"0123456789abcdef0123456789abcdef01234567","fingerprint":"sha256:abc","referred":true}`
	if doer.bodies[0] != want {
		t.Errorf("body =\n%s\nwant\n%s", doer.bodies[0], want)
	}
	req := doer.requests[0]
	if req.Method != http.MethodPost || req.URL.String() != "https://relay.example"+relay.MergeGroupRoute {
		t.Errorf("request = %s %s, want a POST to the relay's queue route", req.Method, req.URL)
	}
	if got := req.Header.Get("authorization"); got != "Bearer an-oidc-token" {
		t.Errorf("authorization = %q, want the minted token", got)
	}
	wantAnswer := relay.Answer{State: "success", Description: "cleared by @octocat at 1a2b3c4d5e6f"}
	if out.Answer != wantAnswer {
		t.Errorf("Answer = %+v, want %+v", out.Answer, wantAnswer)
	}
}

// An unreferred decision is spelled out in the body, not left absent: the
// relay reads an absent field as a submission that did not say.
func TestSubmitComparisonSaysWhenTheDecisionDoesNotRefer(t *testing.T) {
	doer := newQueueDoer()
	in := submitIn(doer)
	in.Referred = false

	if _, err := SubmitComparison(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	want := `{"queue_ref":"refs/heads/gh-readonly-queue/main/pr-69-1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d",` +
		`"pull_request":69,"sha":"9f3b1c2d4e5f60718293a4b5c6d7e8f901234567",` +
		`"base_sha":"0123456789abcdef0123456789abcdef01234567","fingerprint":"sha256:abc","referred":false}`
	if doer.bodies[0] != want {
		t.Errorf("body =\n%s\nwant\n%s", doer.bodies[0], want)
	}
}

// Every answer but 200 publishes no verdict, and says so.
func TestSubmitComparisonFailsWhenTheRelayPublishesNothing(t *testing.T) {
	doer := newQueueDoer()
	doer.status = http.StatusForbidden
	doer.answer = `{"error":"refused"}`

	_, err := SubmitComparison(context.Background(), submitIn(doer))
	if want := "the relay answered 403 and published no verdict: refused"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A 200 naming no state says nothing about what was published.
func TestSubmitComparisonFailsOnAnAnswerNamingNoState(t *testing.T) {
	doer := newQueueDoer()
	doer.answer = `{}`

	_, err := SubmitComparison(context.Background(), submitIn(doer))
	if want := "the relay answered 200 naming no state, so nothing says what was published"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
