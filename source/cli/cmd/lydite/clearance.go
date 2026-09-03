package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/ui"
)

func newClearanceCmd() *cobra.Command {
	var eventPath string
	var noColor bool
	cmd := &cobra.Command{
		Use:           "clearance",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Answer a lydite command posted on a pull request",
		Long: `Answer a lydite command posted on a pull request.

A referral is resolved by a person, not by pushing more code, and this is
where they say so. ` + "`/lydite clear`" + ` resolves the referral standing on the
pull request's current head; ` + "`/lydite explain`" + ` restates it.

A clearance names one revision. Any push produces a new head carrying no
verdict, so the clearance does not travel with the branch.

The input is the webhook payload the platform delivers, which a workflow
writes to the path in GITHUB_EVENT_PATH.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClearance(cmd.Context(), cmd, eventPath, noColor)
		},
	}
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload to answer (defaults to GITHUB_EVENT_PATH)")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	return cmd
}

func runClearance(ctx context.Context, cmd *cobra.Command, eventPath string, noColor bool) error {
	report := ui.NewReport("clearance")

	if eventPath == "" {
		eventPath = os.Getenv("GITHUB_EVENT_PATH")
	}
	if eventPath == "" {
		return fmt.Errorf("clearance needs an event payload: pass --event, or run where GITHUB_EVENT_PATH is set")
	}
	event, err := forge.LoadCommentEvent(eventPath)
	if err != nil {
		return err
	}

	// A comment on a plain issue names no revision, so there is nothing a
	// clearance could apply to.
	if !event.OnPullRequest() {
		return writeReport(cmd, report, noColor, ui.Row{
			Status: ui.StatusContext,
			Label:  "not a pull request",
			Value:  "nothing to decide",
		})
	}
	command := clearance.Parse(event.Comment.Body)
	if command.Verb == clearance.VerbNone {
		return writeReport(cmd, report, noColor, ui.Row{
			Status: ui.StatusContext,
			Label:  "not addressed to lydite",
			Value:  "ignored",
		})
	}

	token := firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return fmt.Errorf("clearance needs GITHUB_TOKEN with `statuses: write` and `pull-requests: write`")
	}
	repo, err := forge.ParseRepo(event.Repository.FullName)
	if err != nil {
		return err
	}
	client := forge.New(token)

	head, err := client.HeadSHA(ctx, repo, event.Issue.Number)
	if err != nil {
		return err
	}
	canWrite, err := client.CanWrite(ctx, repo, event.Comment.User.Login)
	if err != nil {
		return err
	}
	status, err := client.ReferralStatus(ctx, repo, head)
	if err != nil {
		return err
	}

	action := clearance.Decide(clearance.Request{
		Command:   command,
		HeadSHA:   head,
		CanWrite:  canWrite,
		Status:    status,
		CommentAt: event.Comment.CreatedAt,
	})

	row, err := applyAction(ctx, client, repo, event, head, action)
	if err != nil {
		return err
	}
	return writeReport(cmd, report, noColor, row)
}

// applyAction carries out the decision and returns the row describing it.
func applyAction(ctx context.Context, client *forge.Client, repo forge.Repo, event forge.CommentEvent, head string, action clearance.Action) (ui.Row, error) {
	switch action.Kind {
	case clearance.KindClear:
		description := fmt.Sprintf("cleared by @%s at %s", event.Comment.User.Login, shortSHA(head))
		if err := client.PublishStatus(ctx, repo, head, clearance.StateSuccess, description, runURL()); err != nil {
			return ui.Row{}, err
		}
		reply(ctx, client, repo, event.Issue.Number, ui.Comment{
			Verdict:  ui.VerdictPass,
			Headline: description,
			Version:  version,
			Base:     shortSHA(head),
		})
		return ui.Row{Status: ui.StatusPass, Label: "clearance", Value: description}, nil

	case clearance.KindExplain:
		return explain(ctx, client, repo, event, head)

	case clearance.KindRefuse:
		text := refusal(action.Reason, event.Comment.User.Login, head)
		reply(ctx, client, repo, event.Issue.Number, ui.Comment{
			Verdict:  ui.VerdictRefer,
			Headline: text,
			Version:  version,
			Base:     shortSHA(head),
		})
		return ui.Row{Status: ui.StatusRefer, Label: string(action.Reason), Value: text}, nil

	default:
		return ui.Row{Status: ui.StatusContext, Label: "no action", Value: "ignored"}, nil
	}
}

// explain restates the standing verdict without changing it.
func explain(ctx context.Context, client *forge.Client, repo forge.Repo, event forge.CommentEvent, head string) (ui.Row, error) {
	status, err := client.ReferralStatus(ctx, repo, head)
	if err != nil {
		return ui.Row{}, err
	}
	comment := ui.Comment{Version: version, Base: shortSHA(head)}
	if status == nil {
		comment.Verdict = ui.VerdictRefer
		comment.Headline = "no verdict has been published for this revision yet"
	} else {
		comment.Headline = status.Description
		switch status.State {
		case clearance.StateSuccess:
			comment.Verdict = ui.VerdictPass
		case clearance.StateFailure:
			comment.Verdict = ui.VerdictFail
		default:
			comment.Verdict = ui.VerdictRefer
		}
	}
	reply(ctx, client, repo, event.Issue.Number, comment)
	return ui.Row{Status: ui.StatusContext, Label: "explain", Value: comment.Headline}, nil
}

// refusal is what a person reads when their command changed nothing.
//
// Every one of these names the reason and a way forward. A command that
// silently does nothing is the failure this whole surface has to avoid: the
// one thing nobody may conclude from silence is that their change was
// cleared.
func refusal(reason clearance.Reason, login, head string) string {
	switch reason {
	case clearance.ReasonNotPermitted:
		return fmt.Sprintf("@%s does not have write access to this repository, so this changes nothing", login)
	case clearance.ReasonUnknownVerb:
		return "unknown command — this surface has `/lydite clear` and `/lydite explain`"
	case clearance.ReasonStaleSHA:
		return fmt.Sprintf("that revision is not the current head (%s), so nothing was cleared", shortSHA(head))
	case clearance.ReasonNoStatus:
		return fmt.Sprintf("no verdict has been published for %s yet — there is nothing to clear", shortSHA(head))
	case clearance.ReasonHeadMoved:
		return fmt.Sprintf("the head moved after this comment was written; re-issue `/lydite clear %s` to clear what is there now", shortSHA(head))
	case clearance.ReasonNotReferred:
		return "this is a failing gate, not a referral — it is cleared by splitting the change, not by a comment"
	case clearance.ReasonAlreadyPassing:
		return "this change already merges unattended; there is no referral to clear"
	default:
		return "nothing to do"
	}
}

// reply posts an answer to the conversation.
//
// A refusal or an explanation is an answer to a question somebody asked, so
// it is a new comment rather than an edit of the standing verdict. Editing
// the sticky comment would answer in a place the asker is not looking, and
// would overwrite the verdict with a reply to one person.
func reply(ctx context.Context, client *forge.Client, repo forge.Repo, number int, comment ui.Comment) {
	if err := client.CreateComment(ctx, repo, number, comment.Render()); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the decision was recorded but the reply was not posted: %v\n", err)
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
