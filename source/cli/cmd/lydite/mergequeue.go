package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// queueRoute is the relay endpoint a merge-queue entry's comparison is
// submitted to.
//
// It is its own route rather than a shape of /status because the two carry
// different authority: /status writes the verdict a caller composed, and this
// submits evidence the relay decides from — it resolves the originating pull
// request from the queue ref, reads the clearance standing on that pull
// request's own head, compares, and composes the status itself. A job that
// could name the state would not need the comparison at all.
const queueRoute = "/merge-group"

// queueRequest is what the queue-time path submits.
//
// None of it is authority. The relay takes the repository from the verified
// OIDC claim, derives the pull request from the ref in that claim, and resolves
// both the queue revision and the pull request's head live before it writes —
// every field here is an assertion it has to agree with, in the discipline
// every other relay route already applies to an identifier naming what gets
// written to.
type queueRequest struct {
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

// queueAnswer is what the relay answers: the status it wrote, or why it wrote
// none.
type queueAnswer struct {
	State       string `json:"state"`
	Description string `json:"description"`
	Error       string `json:"error"`
}

// idTokenAnswer is the Actions token endpoint's own document.
type idTokenAnswer struct {
	Value string `json:"value"`
}

// doer is the transport this path talks to the relay and to the Actions token
// endpoint through.
//
// Injected rather than reached for, because what this command does past the
// recomputation is compose two requests, and a test driving them against the
// real internet covers neither.
type doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// newQueueCmd answers a merge_group event.
//
// A merge queue replays the change onto a fresh base, so the revision it builds
// is one no clearance names and none can: the ref carries no comment surface.
// This recomputes the decision against the queue's own tree and the base
// branch's current exemptions, fingerprints the reasons it refers on, and asks
// the relay to compare that against the fingerprint the originating pull
// request's clearance recorded.
func newQueueCmd() *cobra.Command {
	var dir, eventPath, relay, base string
	var noColor bool
	cmd := &cobra.Command{
		Use:           "queue",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Carry a clearance forward onto a merge-queue entry",
		Long: `Carry a clearance forward onto a merge-queue entry.

A merge queue replays the change onto the base branch's current tip, so the
revision it builds carries no ` + clearance.Context + ` and no ` + clearance.ClearanceContext + `:
a gh-readonly-queue ref belongs to no pull request, so there is no surface
` + "`/lydite clear`" + ` could even be typed at. Every referred change would be stuck.

A clearance is not a statement about a tree, though — it is a statement about a
decision. This recomputes that decision against the queue's own tree and the
exemptions the base branch declares now, fingerprints the reasons it refers on,
and submits that fingerprint to the relay, which resolves the originating pull
request from this run's own ref, reads the fingerprint the clearance recorded
there, compares the two and publishes ` + clearance.Context + ` at the queue revision:
success carrying the same attribution forward when the decision is unchanged,
and pending naming what changed when it is not. A recomputed decision that does
not refer at all has no clearance to carry, and is published success on its own
merits.

This job holds no writing token, by design: it reads the change's own diff to
recompute the decision, and the relay is what writes. --relay is therefore
required — there is no fallback to a token this job does not have.

The input is the merge_group payload the platform delivers, which a workflow
writes to the path in GITHUB_EVENT_PATH.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQueue(cmd.Context(), cmd, http.DefaultClient, queueOptions{
				dir:       dir,
				eventPath: eventPath,
				relay:     relay,
				base:      base,
				noColor:   noColor,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&eventPath, "event", "", "merge_group payload to answer (defaults to GITHUB_EVENT_PATH)")
	cmd.Flags().StringVar(&relay, "relay", "", "origin of the relay the comparison is submitted to")
	// "auto" resolves the merge-base against the branch the payload says this
	// entry is queued for, so this command needs no --base-branch of its own:
	// the event names the branch, which is the dead end a workflow input
	// evaluating to empty outside a pull_request run otherwise leaves.
	cmd.Flags().StringVar(&base, "base", "auto",
		`commit the decision is recomputed against ("auto" resolves the merge-base with the branch the entry is queued for)`)
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

// queueOptions is what runQueue was asked for.
type queueOptions struct {
	dir       string
	eventPath string
	relay     string
	base      string
	noColor   bool
}

func runQueue(ctx context.Context, cmd *cobra.Command, client doer, opt queueOptions) error {
	report := ui.NewReport("queue")

	if opt.relay == "" {
		return fmt.Errorf("queue needs --relay: this job holds no token that could publish a status, so the relay is the only route")
	}
	eventPath := firstNonEmpty(opt.eventPath, os.Getenv("GITHUB_EVENT_PATH"))
	if eventPath == "" {
		return fmt.Errorf("queue needs an event payload: pass --event, or run where GITHUB_EVENT_PATH is set")
	}
	event, err := forge.LoadMergeGroupEvent(eventPath)
	if err != nil {
		return err
	}
	if event.MergeGroup.HeadSHA == "" {
		return fmt.Errorf("%s names no merge group: this command answers a merge_group event", eventPath)
	}
	entry, err := event.QueueEntry()
	if err != nil {
		return err
	}

	// The base is resolved here rather than taken from the payload's own
	// base_sha, for the reason review resolves its own: the revision the diff
	// is read against decides what the decision is computed over, and this run
	// can establish it from the checkout it holds.
	baseSHA, err := resolveReviewBase(ctx, opt.dir, opt.base, event.BaseBranch())
	if err != nil {
		return err
	}
	decision, err := queueDecision(ctx, opt.dir, baseSHA)
	if err != nil {
		return err
	}
	fingerprint := referral.Fingerprint(decision.Uncovered, decision.Disqualifications)

	rows := []ui.Row{{
		Status: ui.StatusContext,
		Label:  "merge group",
		Value:  fmt.Sprintf("#%d at %s, replayed onto %s", entry.Number, shortSHA(event.MergeGroup.HeadSHA), shortSHA(baseSHA)),
	}, {
		Status: ui.StatusContext,
		Label:  "fingerprint",
		Value:  fmt.Sprintf("%s over %s", fingerprint, queueReasons(decision)),
	}}

	answer, err := submitQueueComparison(ctx, client, opt.relay, queueRequest{
		QueueRef:    event.MergeGroup.HeadRef,
		PullRequest: entry.Number,
		SHA:         event.MergeGroup.HeadSHA,
		BaseSHA:     baseSHA,
		Fingerprint: fingerprint,
		Referred:    decision.Referred,
	})
	if err != nil {
		return err
	}
	return writeReport(cmd, report, opt.noColor, append(rows, queueRow(answer))...)
}

// queueDecision recomputes the decision this entry renders.
//
// The exemptions come from the base commit through loadExemptionsAt, which at
// queue time is the base branch's current tip: a clearance carried forward
// under rules that have since moved would be a clearance issued under rules
// that no longer apply, and Decide already always reads the file from the
// merge-base for the neighbouring reason — a change gets no benefit from its
// own widening. If the file moved in a way that changes the recomputed set, the
// fingerprint legitimately differs and the entry re-refers.
//
// The evidence is the zero referral.Evidence, under which every condition
// fails. A `versions: patch-and-minor` exemption needs a dependency comparison
// and a scan document, neither of which this job has; passing nothing is the
// direction that refers, and an entry whose clearance was given against a
// condition met at pull-request time therefore fingerprints differently here
// and goes back to a person. Measuring it at queue time is worth doing and is
// not this path's to do — see the referral job's own --reports.
func queueDecision(ctx context.Context, dir, baseSHA string) (referral.Decision, error) {
	file, err := loadExemptionsAt(ctx, dir, baseSHA)
	if err != nil {
		return referral.Decision{}, err
	}
	change, err := referral.Changes(ctx, dir, baseSHA)
	if err != nil {
		return referral.Decision{}, err
	}
	return referral.Decide(change, file, referral.Evidence{}), nil
}

// queueReasons says what the fingerprint was taken over, so a log reading
// "pending" can be compared against the pull request's own verdict without
// re-running anything.
func queueReasons(d referral.Decision) string {
	switch {
	case !d.Referred:
		// No reasons at all. A decision that refers for nothing has a
		// fingerprint no clearance can carry, because a clearance is only ever
		// given against a referral — so the submission says the decision does
		// not refer and the relay publishes the entry on its own merits.
		return "no referral"
	default:
		return fmt.Sprintf("%d uncovered path(s), %d disqualification(s)",
			len(d.Uncovered), len(d.Disqualifications))
	}
}

// queueRow renders what the relay answered.
//
// A pending answer is not this run's failure: the entry drops out of the queue
// as any required check still pending would, and the author clears the pull
// request again. Reporting it as a failing job would say the mechanism broke
// when it worked.
func queueRow(a queueAnswer) ui.Row {
	status := ui.StatusRefer
	if a.State == string(clearance.StateSuccess) {
		status = ui.StatusPass
	}
	return ui.Row{Status: status, Label: clearance.Context, Value: firstNonEmpty(a.Description, a.State)}
}

// submitQueueComparison mints the OIDC token for the relay's own origin and
// submits the comparison.
//
// Every answer but 200 fails the run, and nothing falls back. The three-way
// sort a fallback transport needs exists to keep a silent substitution of
// identity from hiding a misconfiguration; there is no substitute here — this
// job holds no token that could publish anything — so an unreachable relay and
// a refused request are both "no verdict was published", which has to be said
// out loud rather than exited 0 over.
func submitQueueComparison(ctx context.Context, client doer, origin string, body queueRequest) (queueAnswer, error) {
	token, err := actionsIDToken(ctx, client, origin)
	if err != nil {
		return queueAnswer{}, err
	}
	req, err := newQueueRequest(ctx, origin, token, body)
	if err != nil {
		return queueAnswer{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return queueAnswer{}, fmt.Errorf("submitting the comparison to %s: %w", origin+queueRoute, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, answerLimit))
	if err != nil {
		return queueAnswer{}, fmt.Errorf("reading the relay's answer: %w", err)
	}
	var answer queueAnswer
	// A body that does not parse leaves the status code as the whole of what
	// was said, which is enough to fail on and not enough to report a verdict
	// from.
	_ = json.Unmarshal(raw, &answer)
	if res.StatusCode != http.StatusOK {
		return queueAnswer{}, fmt.Errorf("the relay answered %d and published no verdict: %s",
			res.StatusCode, firstNonEmpty(answer.Error, strings.TrimSpace(string(raw))))
	}
	if answer.State == "" {
		return queueAnswer{}, fmt.Errorf("the relay answered 200 naming no state, so nothing says what was published")
	}
	return answer, nil
}

// answerLimit bounds what is read back from either endpoint. Both answer a
// small document, and an unbounded read of a response this process did not
// author is a memory budget somebody else sets.
const answerLimit = 64 << 10

// newQueueRequest composes the submission.
//
// The repository is deliberately absent: the relay takes it from the verified
// claim, so there is nothing here that could name another one.
func newQueueRequest(ctx context.Context, origin, token string, body queueRequest) (*http.Request, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(origin, "/")+queueRoute, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("composing the request to the relay: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+token)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// actionsIDToken mints the run's own OIDC token for one audience.
//
// The audience is the relay's origin, so a token minted here cannot be replayed
// against another service — and the relay refuses one whose audience is not its
// own. The endpoint and the token that authorises the mint are the platform's,
// in the environment it sets for a job granted id-token: write; a job without
// that permission has nothing to present and is told so here rather than by a
// 401 from the relay.
func actionsIDToken(ctx context.Context, client doer, audience string) (string, error) {
	endpoint := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	request := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if endpoint == "" || request == "" {
		return "", fmt.Errorf("no Actions OIDC token can be minted here: the job needs `id-token: write`")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		endpoint+"&audience="+url.QueryEscape(audience), nil)
	if err != nil {
		return "", fmt.Errorf("composing the token request: %w", err)
	}
	req.Header.Set("authorization", "Bearer "+request)
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
