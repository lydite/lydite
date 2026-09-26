package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/flow"
	clearanceflow "lydite/lydite/internal/flows/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	clearancestages "lydite/lydite/internal/stages/clearance"
	scmstages "lydite/lydite/internal/stages/scm"
	"lydite/lydite/internal/ui"
)

func newClearanceCmd() *cobra.Command {
	var dir string
	var eventPath string
	var statusOut string
	var base string
	var baseBranch string
	var surfacesPath string
	var noColor bool
	cmd := &cobra.Command{
		Use:           "clearance",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Answer a lydite command posted on a pull request",
		Long: `Answer a lydite command posted on a pull request.

A referral is resolved by a person, not by pushing more code, and this is
where they say so. ` + "`/lydite clear`" + ` resolves the referral standing on the
pull request's current head; ` + "`/lydite explain`" + ` restates it;
` + "`/lydite exempt <shape>`" + ` answers with a draft exemptions-file entry
covering the change's own uncovered paths, and lands nothing.

A clearance names one revision. Any push produces a new head carrying no
verdict, so the clearance does not travel with the branch.

The input is the webhook payload the platform delivers, which a workflow
writes to the path in GITHUB_EVENT_PATH.

A clearance records two statuses on the head by either route: ` + clearance.ClearanceContext + `,
which says who cleared the revision, and ` + clearance.Context + ` resolved to success,
which is the gate a merge waits on.

The ` + clearance.ClearanceContext + ` description carries the fingerprint of the decision
that was cleared, which is what lets ` + "`clearance queue`" + ` carry the clearance onto a
merge-queue entry the same decision still holds for. Computing it reads the
checkout this runs against, which has to be the revision being cleared; a
clearance given anywhere else records no fingerprint and says so, and the
queue entry goes back to a person.

--surfaces reads a comparison ` + "`review compare`" + ` already made instead of running it
here. A component's own comparison executes its own code — a Rust crate's
build.rs, a proc-macro — so the job that records the clearance with a
credential should not also be the job that ran it: compute in one job with
none, clear and record in another that never runs the change's own code.

--status-out <file> renders them as documents instead of posting them, for a
step that posts them: the ` + clearance.ClearanceContext + ` status at <file>, and the
` + clearance.Context + ` status at the sibling <file> with .referral before its
extension. Posting here is the path for a repository that has not adopted the
reusable workflows. The two routes are alternatives, not a ladder. A comment
that clears nothing writes no document.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClearance(cmd.Context(), cmd, clearanceOptions{
				dir:        dir,
				eventPath:  eventPath,
				statusOut:  statusOut,
				base:       base,
				baseBranch: baseBranch,
				surfaces:   surfacesPath,
				noColor:    noColor,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload to answer (defaults to GITHUB_EVENT_PATH)")
	// The base the cleared decision is recomputed against, the same two flags
	// review resolves its own with: a fingerprint is taken over a decision, and
	// a decision is taken over a diff, which needs the revision that diff is
	// read against.
	cmd.Flags().StringVar(&base, "base", "auto",
		`commit the cleared decision is recomputed against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().StringVar(&surfacesPath, "surfaces", "",
		"read a comparison 'review compare' already made instead of running it here, and fingerprint the decision it feeds")
	cmd.Flags().StringVar(&statusOut, "status-out", "",
		"render the "+clearance.ClearanceContext+" status as a JSON document at this path, and the "+clearance.Context+
			" status it resolves at the .referral sibling, for another step to post instead of posting them here")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	// The queue path answers a merge_group event rather than a comment, and it
	// belongs here because what it decides is a clearance's reach: a merge
	// queue's own revision is one nobody can be asked to clear, so the question
	// is whether the clearance the pull request already holds still applies.
	cmd.AddCommand(newQueueCmd())
	return cmd
}

// clearanceOptions is what runClearance was asked for.
type clearanceOptions struct {
	dir        string
	eventPath  string
	statusOut  string
	base       string
	baseBranch string
	// surfaces names the comparison document a computing job already wrote,
	// and is empty for a run that makes the comparison itself.
	surfaces string
	noColor  bool
}

func runClearance(ctx context.Context, cmd *cobra.Command, opt clearanceOptions) error {
	report := ui.NewReport("clearance")

	eventPath := firstNonEmpty(opt.eventPath, os.Getenv("GITHUB_EVENT_PATH"))
	if eventPath == "" {
		return fmt.Errorf("clearance needs an event payload: pass --event, or run where GITHUB_EVENT_PATH is set")
	}
	// The payload contributes the comment's id and the repository it claims,
	// and nothing else: what the comment says, who wrote it and which pull
	// request it is on are resolved live by the flow.
	ref, err := forge.ReadCommentRef(eventPath)
	if err != nil {
		return err
	}
	answer, err := clearanceflow.New()
	if err != nil {
		return err
	}
	r, err := answer.Run(ctx, clearanceflow.Params{
		CommentRef:       ref,
		Dir:              opt.dir,
		Base:             opt.base,
		BaseBranch:       opt.baseBranch,
		SurfacesDocument: opt.surfaces,
		Toolchains:       commandToolchains{cmd},
		Progress:         cmd.ErrOrStderr(),
		Version:          version,
		TargetURL:        runURL(),
		StatusOut:        opt.statusOut,
	}.Inputs())
	logClearance(cmd, r)
	if err != nil {
		return clearanceError(err)
	}
	row, err := clearanceRow(r)
	if err != nil {
		return err
	}
	return writeReport(cmd, report, opt.noColor, row)
}

// logClearance writes to the job log what the run had to say there, whether or
// not it went on to fail: a warning raised before a failure is part of what
// whoever investigates the failure reads.
//
// A failed derivation of the uncovered set goes to the process's own stderr;
// what the fingerprint could not take goes to the command's. A reply that was
// not posted is named once per reply, and fails nothing — the decision it
// answers already stands.
func logClearance(cmd *cobra.Command, r *flow.Result) {
	if decided, err := flow.Output[clearancestages.DecideOut](r, clearanceflow.StageDecide); err == nil {
		for _, warning := range decided.Warnings {
			_, _ = fmt.Fprintln(os.Stderr, warning)
		}
	}
	if taken, err := flow.Output[clearancestages.FingerprintOut](r, clearanceflow.StageFingerprint); err == nil {
		for _, warning := range taken.Warnings {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), warning)
		}
	}
	for _, failed := range r.Errors() {
		_, _ = fmt.Fprintf(os.Stderr, "lydite: the decision was recorded but the reply was not posted: %v\n", failed.Err)
	}
}

// clearanceError is a run's failure as this command reports it: the stage's
// own error, not the flow's framing of it, and a run holding no credential
// named by the permissions a workflow has to grant.
func clearanceError(err error) error {
	if errors.Is(err, scmstages.ErrNoCredential) {
		return errors.New("clearance needs GITHUB_TOKEN with `statuses: write` and `pull-requests: write`")
	}
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// clearanceRow is the one row describing what the run did.
//
// A clearance names the description its statuses carry; a reply names its
// headline, except a proposal, which names what it proposed — the draft
// itself is the reply's to show, not the log's.
func clearanceRow(r *flow.Result) (ui.Row, error) {
	parsed, err := flow.Output[clearancestages.ParseCommandOut](r, clearanceflow.StageParseCommand)
	if err != nil {
		return ui.Row{}, err
	}
	// A comment on a plain issue names no revision, so there is nothing a
	// clearance could apply to.
	if !parsed.OnPullRequest {
		return ui.Row{Status: ui.StatusContext, Label: "not a pull request", Value: "nothing to decide"}, nil
	}
	if !parsed.Addressed {
		return ui.Row{Status: ui.StatusContext, Label: "not addressed to lydite", Value: "ignored"}, nil
	}
	decided, err := flow.Output[clearancestages.DecideOut](r, clearanceflow.StageDecide)
	if err != nil {
		return ui.Row{}, err
	}
	action := decided.Action
	switch action.Kind {
	case clearance.KindClear:
		described, err := flow.Output[clearancestages.DescribeClearanceOut](r, clearanceflow.StageDescribeClearance)
		if err != nil {
			return ui.Row{}, err
		}
		return ui.Row{Status: ui.StatusPass, Label: "clearance", Value: described.Description}, nil
	case clearance.KindExempt:
		return ui.Row{
			Status: ui.StatusRefer,
			Label:  "exempt",
			Value:  fmt.Sprintf("proposed %q over %d path(s)", action.Name, len(action.Paths)),
		}, nil
	case clearance.KindExplain, clearance.KindRefuse:
		replied, err := flow.Output[clearancestages.ComposeReplyOut](r, clearanceflow.StageComposeReply)
		if err != nil {
			return ui.Row{}, err
		}
		if action.Kind == clearance.KindExplain {
			return ui.Row{Status: ui.StatusContext, Label: "explain", Value: replied.Headline}, nil
		}
		return ui.Row{Status: ui.StatusRefer, Label: string(action.Reason), Value: replied.Headline}, nil
	default:
		return ui.Row{Status: ui.StatusContext, Label: "no action", Value: "ignored"}, nil
	}
}

// writeReport renders what happened, and never turns a row into an exit
// code.
//
// This command answers a comment; answering one is its whole job, and it
// succeeded whether or not the answer was "nothing changed". What a change's
// verdict is remains the commit status's to say, so a refused clearance is
// amber in the log and leaves the standing referral exactly as it was.
// Exiting non-zero here would report a stranger's mistyped comment as a
// broken pipeline.
func writeReport(cmd *cobra.Command, report *ui.Report, noColor bool, rows ...ui.Row) error {
	for _, row := range rows {
		report.Add(row)
	}
	out := cmd.OutOrStdout()
	return report.Write(out, false, ui.ColorEnabled(out, noColor))
}
