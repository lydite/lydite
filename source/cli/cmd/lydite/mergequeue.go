package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/flow"
	queueflow "lydite/lydite/internal/flows/queue"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/relay"
	queuestages "lydite/lydite/internal/stages/queue"
	"lydite/lydite/internal/ui"
)

// queueRoute is the relay endpoint a merge-queue entry's comparison is
// submitted to.
const queueRoute = relay.MergeGroupRoute

// queueRequest is what the queue-time path submits to the relay.
type queueRequest = relay.MergeGroupRequest

// queueAnswer is what the relay answers: the status it wrote, or why it wrote
// none.
type queueAnswer = relay.Answer

// doer is the transport this path talks to the relay and to the Actions token
// endpoint through.
type doer = relay.Doer

// newQueueCmd answers a merge_group event.
//
// A merge queue replays the change onto a fresh base, so the revision it builds
// is one no clearance names and none can: the ref carries no comment surface.
// This recomputes the decision against the queue's own tree and the base
// branch's current exemptions, fingerprints the reasons it refers on, and asks
// the relay to compare that against the fingerprint the originating pull
// request's clearance recorded.
func newQueueCmd() *cobra.Command {
	var dir, eventPath, relayOrigin, base string
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
				relay:     relayOrigin,
				base:      base,
				noColor:   noColor,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&eventPath, "event", "", "merge_group payload to answer (defaults to GITHUB_EVENT_PATH)")
	cmd.Flags().StringVar(&relayOrigin, "relay", "", "origin of the relay the comparison is submitted to")
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

	carry, err := queueflow.New()
	if err != nil {
		return err
	}
	// The mint endpoint and the token authorising it are passed through empty
	// or not: a job without id-token: write is refused by the mint stage, after
	// the payload, the base and the decision have each had their chance to
	// fail first.
	r, err := carry.Run(ctx, queueflow.Params{
		Dir:       opt.dir,
		EventPath: eventPath,
		Base:      opt.base,
		Relay:     opt.relay,
		MintURL:   os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"),
		MintToken: os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN"),
		Client:    client,
	}.Inputs())
	if err != nil {
		return queueError(err)
	}
	rows, err := queueRows(r)
	if err != nil {
		return err
	}
	return writeReport(cmd, report, opt.noColor, rows...)
}

// queueError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it.
func queueError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// queueRows are the rows a run that published a verdict reports: the entry and
// the base it was replayed onto, what the fingerprint was taken over, and what
// the relay answered.
func queueRows(r *flow.Result) ([]ui.Row, error) {
	entry, err := flow.Output[queuestages.LoadEventOut](r, queueflow.StageLoadEvent)
	if err != nil {
		return nil, err
	}
	base, err := flow.Output[queuestages.ResolveBaseOut](r, queueflow.StageResolveBase)
	if err != nil {
		return nil, err
	}
	recomputed, err := flow.Output[queuestages.RecomputeDecisionOut](r, queueflow.StageRecomputeDecision)
	if err != nil {
		return nil, err
	}
	submitted, err := flow.Output[queuestages.SubmitComparisonOut](r, queueflow.StageSubmitComparison)
	if err != nil {
		return nil, err
	}
	return []ui.Row{{
		Status: ui.StatusContext,
		Label:  "merge group",
		Value:  fmt.Sprintf("#%d at %s, replayed onto %s", entry.PullRequest, shortSHA(entry.HeadSHA), shortSHA(base.BaseSHA)),
	}, {
		Status: ui.StatusContext,
		Label:  "fingerprint",
		Value:  fmt.Sprintf("%s over %s", recomputed.Fingerprint, queueReasons(recomputed.Decision)),
	}, queueRow(submitted.Answer)}, nil
}

// queueDecision is the decision the queue flow's recompute-decision stage
// renders for dir against baseSHA, so a caller asserting on it asserts on the
// stage the command runs.
func queueDecision(ctx context.Context, dir, baseSHA string) (referral.Decision, error) {
	out, err := queuestages.RecomputeDecision(ctx, queuestages.RecomputeDecisionIn{Dir: dir, BaseSHA: baseSHA})
	return out.Decision, err
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
