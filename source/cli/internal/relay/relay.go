// Package relay is the client for lydite's pr-relay: it mints a GitHub Actions
// OIDC token for the relay's own audience, and submits a merge-group comparison
// to the relay under that token.
//
// It is transport and protocol only. Where the mint endpoint comes from, what
// the comparison is computed over, and how an answer is rendered are the
// caller's; this package composes the requests, reads the answers, and refuses
// anything that is not an answer it can report.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MergeGroupRoute is the relay endpoint a merge-queue entry's comparison is
// submitted to.
//
// It is its own route rather than a shape of /status because the two carry
// different authority: /status writes the verdict a caller composed, and this
// submits evidence the relay decides from — it resolves the originating pull
// request from the queue ref, reads the clearance standing on that pull
// request's own head, compares, and composes the status itself. A job that
// could name the state would not need the comparison at all.
const MergeGroupRoute = "/merge-group"

// ErrNoMintEndpoint is MintIDToken's refusal when it is given no endpoint or no
// token authorising the mint: a job that was not granted `id-token: write` has
// nothing to present, and is told so here rather than by a 401 from the relay.
var ErrNoMintEndpoint = errors.New("no Actions OIDC token can be minted here: the job needs `id-token: write`")

// answerLimit bounds what is read back from either endpoint. Both answer a
// small document, and an unbounded read of a response this process did not
// author is a memory budget somebody else sets.
const answerLimit = 64 << 10

// Doer is the transport the relay and the Actions token endpoint are talked to
// through.
//
// Injected rather than reached for, because what this package does is compose
// two requests, and a test driving them against the real internet covers
// neither.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// MergeGroupRequest is what the queue-time path submits.
//
// None of it is authority. The relay takes the repository from the verified
// OIDC claim, derives the pull request from the ref in that claim, and resolves
// both the queue revision and the pull request's head live before it writes —
// every field here is an assertion it has to agree with, in the discipline
// every other relay route already applies to an identifier naming what gets
// written to.
type MergeGroupRequest struct {
	// QueueRef and PullRequest name the entry, and must agree with the ref the
	// OIDC claim carries. They are sent so a disagreement is refusable rather
	// than invisible.
	QueueRef    string `json:"queue_ref"`
	PullRequest int    `json:"pull_request"`
	// SHA is the queue revision the verdict is published against.
	SHA string `json:"sha"`
	// BaseSHA is the revision the decision was recomputed against, for the
	// description a person reads. The relay never measures anything against
	// it.
	BaseSHA string `json:"base_sha"`
	// Fingerprint is referral.Fingerprint over the reasons the recomputed
	// decision refers on, and the whole of what the relay compares.
	Fingerprint string `json:"fingerprint"`
	// Referred is whether the recomputed decision refers at all. False means
	// there is nothing to compare: a clearance is only ever given against a
	// referral, so an entry that refers for nothing has none to carry forward
	// and the relay publishes success on its own merits. The decision is
	// recomputed over the queue's own tree against the base tip, so this speaks
	// for every change the queue commit holds and not only this entry's.
	Referred bool `json:"referred"`
}

// Answer is what the relay answers: the status it wrote, or why it wrote none.
type Answer struct {
	State       string `json:"state"`
	Description string `json:"description"`
	Error       string `json:"error"`
}

// idTokenAnswer is the Actions token endpoint's own document.
type idTokenAnswer struct {
	Value string `json:"value"`
}

// MintIDToken mints the run's own OIDC token for one audience.
//
// The audience is the relay's origin, so a token minted here cannot be replayed
// against another service — and the relay refuses one whose audience is not its
// own. requestURL and requestToken are the platform's mint endpoint and the
// token that authorises the mint, which it provides only to a job granted
// id-token: write; either one empty answers ErrNoMintEndpoint and attempts
// nothing.
func MintIDToken(ctx context.Context, client Doer, requestURL, requestToken, audience string) (string, error) {
	if requestURL == "" || requestToken == "" {
		return "", ErrNoMintEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, // #nosec G704 -- requestURL is the platform's own mint endpoint, provided for a job granted `id-token: write`, not pull-request content
		requestURL+"&audience="+url.QueryEscape(audience), nil)
	if err != nil {
		return "", fmt.Errorf("composing the token request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+requestToken)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting an Actions OIDC token: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, answerLimit))
	if err != nil {
		return "", fmt.Errorf("reading the Actions OIDC token: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the Actions token endpoint answered %d", res.StatusCode)
	}
	var answer idTokenAnswer
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Value == "" {
		return "", fmt.Errorf("the Actions token endpoint answered no token")
	}
	return answer.Value, nil
}

// SubmitMergeGroup submits the comparison to the relay at origin, authorised by
// token — the OIDC token MintIDToken minted for that origin.
//
// Every answer but 200 is an error, and so is a 200 naming no state. The
// three-way sort a fallback transport needs exists to keep a silent
// substitution of identity from hiding a misconfiguration; there is no
// substitute here — the queue job holds no token that could publish anything —
// so an unreachable relay and a refused request are both "no verdict was
// published", which has to be said out loud rather than exited 0 over.
func SubmitMergeGroup(ctx context.Context, client Doer, origin, token string, body MergeGroupRequest) (Answer, error) {
	req, err := newMergeGroupRequest(ctx, origin, token, body)
	if err != nil {
		return Answer{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("submitting the comparison to %s: %w", origin+MergeGroupRoute, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, answerLimit))
	if err != nil {
		return Answer{}, fmt.Errorf("reading the relay's answer: %w", err)
	}
	var answer Answer
	// A body that does not parse leaves the status code as the whole of what
	// was said, which is enough to fail on and not enough to report a verdict
	// from.
	_ = json.Unmarshal(raw, &answer)
	if res.StatusCode != http.StatusOK {
		return Answer{}, fmt.Errorf("the relay answered %d and published no verdict: %s",
			res.StatusCode, firstNonEmpty(answer.Error, strings.TrimSpace(string(raw))))
	}
	if answer.State == "" {
		return Answer{}, fmt.Errorf("the relay answered 200 naming no state, so nothing says what was published")
	}
	return answer, nil
}

// newMergeGroupRequest composes the submission.
//
// The repository is deliberately absent: the relay takes it from the verified
// claim, so there is nothing here that could name another one.
func newMergeGroupRequest(ctx context.Context, origin, token string, body MergeGroupRequest) (*http.Request, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(origin, "/")+MergeGroupRoute, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("composing the request to the relay: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// firstNonEmpty returns the first of vals that is not empty, or "" when every
// one is.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
