// Package forge talks to the hosting platform: the commit statuses a
// referral is published as, the permission that decides whose word resolves
// one, and the comments both are explained in.
//
// It is deliberately small and hand-rolled over net/http rather than a
// generated client. lydite's dependency set is part of its argument — every
// tool it runs is pinned to a manifest something can age out — and a client
// covering six calls is cheaper to audit than one covering the platform.
//
// Nothing here decides anything. What a status means, and which comment may
// change one, belong to internal/clearance, so that the rules are testable
// without a network and this file stays a transport.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// DefaultBaseURL is the public API root. It is a field on Client rather than
// a constant so tests can point at a local server, and so an enterprise host
// is a configuration rather than a fork.
const DefaultBaseURL = "https://api.github.com"

// Client is an authenticated handle on one hosting platform.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client with the timeouts a CI step wants: bounded, so a
// hanging platform fails the step rather than holding a runner until the job
// limit.
//
// The API root comes from GITHUB_API_URL when the platform sets it, which is
// what makes this work unchanged against an enterprise host rather than only
// against the public one.
func New(token string) *Client {
	base := os.Getenv("GITHUB_API_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		BaseURL: strings.TrimSuffix(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Repo is an owner and a name, kept together because every call needs both
// and a single "owner/name" string invites splitting it in six places.
type Repo struct {
	Owner string
	Name  string
}

// ParseRepo reads the "owner/name" form the platform puts in the
// environment.
func ParseRepo(s string) (Repo, error) {
	owner, name, ok := strings.Cut(s, "/")
	if !ok || owner == "" || name == "" {
		return Repo{}, fmt.Errorf("%q is not in owner/name form", s)
	}
	return Repo{Owner: owner, Name: name}, nil
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// do performs one request and decodes a JSON response into out, which may be
// nil when the body is not wanted.
//
// A non-2xx response carries the platform's own message, which names the
// missing scope or the protected resource. Reporting only the status code
// would leave a reader guessing at exactly the failures that are worth
// reading — a token without `statuses: write` is the most likely thing to go
// wrong here and says so plainly.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: readMessage(resp.Body)}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// APIError is a non-2xx response, kept as a type so a caller can distinguish
// "absent" from "went wrong" without matching on message text.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("github: %d", e.Status)
	}
	return fmt.Sprintf("github: %d: %s", e.Status, e.Body)
}

// NotFound reports whether err is a 404. A missing resource is routinely an
// answer rather than a failure — a commit with no status yet, a pull request
// with no comment from lydite.
func NotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

func readMessage(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<10))
	if err != nil {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Message != "" {
		return payload.Message
	}
	return strings.TrimSpace(string(raw))
}

func escape(s string) string { return url.PathEscape(s) }
