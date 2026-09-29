package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/flow"
	threadsflow "lydite/lydite/internal/flows/threads"
	"lydite/lydite/internal/runner"
	scmstages "lydite/lydite/internal/stages/scm"
	threadsstages "lydite/lydite/internal/stages/threads"
	truststages "lydite/lydite/internal/stages/trust"
	"lydite/lydite/internal/ui"
)

// newThreadsCmd turns the located claims a run made into review threads.
//
// It owns all three steps — read the threads already standing, compute the
// delta, and write it out — and applies the result only when asked to. The
// relay applies the identical document, so there is one implementation of
// what a fingerprint matches and what may be deleted, and two transports that
// decide nothing.
//
// The read is not the write. Fetching the prior state uses the job's own
// token on both paths, because the delta has to be computed somewhere with a
// token and the job that publishes already holds `pull-requests: write` for
// the comment's fallback — so this costs no credential that was not there.
// What the relay takes over is the write, which is the half ADR 0022 is about.
//
// `lydite publish` stays pure. Nothing about a hosting platform enters it, and
// this command exists rather than a flag on that one for exactly that reason.
func newThreadsCmd() *cobra.Command {
	var (
		reports   []string
		ops       string
		apply     bool
		eventPath string
	)
	cmd := &cobra.Command{
		Use:   "threads",
		Short: "Reconcile the pull request's review threads with this run's located findings",
		Long: "Reconcile a pull request's review threads with the located findings in one or more\n" +
			"report directories.\n\n" +
			"Every finding that reaches the change at a line or at a file becomes a thread on it;\n" +
			"the rest are the standing comment's, and no finding appears in both. The operations\n" +
			"are written to --ops whatever happens, and applied only with --apply — the relay\n" +
			"applies the same document under lydite's own identity.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			if ops == "" {
				return errors.New("no operations file: pass --ops <file>, which is written whether or not --apply is given")
			}
			return runThreads(cmd, reports, ops, eventPath, apply)
		},
	}
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory to read; repeatable")
	cmd.Flags().StringVar(&ops, "ops", "", "file to write the operations document to")
	cmd.Flags().BoolVar(&apply, "apply", false,
		"apply the operations with this job's own token, instead of leaving them for the relay")
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload naming the pull request (defaults to GITHUB_EVENT_PATH)")
	return cmd
}

// runThreads runs the threads flow and renders what it did.
//
// The findings' warnings are written whether or not a later stage failed, and
// so is every refused delete answered before one did: a warning raised before
// a failure is part of what whoever investigates the failure reads. A run that
// stopped before applying anything writes no rows, since there is no plan it
// acted on to report. A run that stopped while applying reports the plan, and
// the row for how far applying it got.
func runThreads(cmd *cobra.Command, reports []string, opsPath, eventPath string, apply bool) error {
	// Made first: a report times the run from its own creation.
	rep := ui.NewReport("threads")
	reconcile, err := threadsflow.New()
	if err != nil {
		return err
	}
	r, runErr := reconcile.Run(cmd.Context(), threadsflow.Params{
		Reports:   reports,
		OpsPath:   opsPath,
		EventPath: firstNonEmpty(eventPath, os.Getenv("GITHUB_EVENT_PATH")),
		Apply:     apply,
		Reader:    commandFindingsReader{},
	}.Inputs())

	stderr := cmd.ErrOrStderr()
	if read, err := flow.Output[threadsstages.ReadFindingsOut](r, threadsflow.StageReadFindings); err == nil {
		for _, fp := range read.Dropped {
			_, _ = fmt.Fprintf(stderr,
				"warning: %s was reported twice and one copy is dropped; one claim is one thread\n", fp)
		}
		for _, m := range read.Missing {
			_, _ = fmt.Fprintf(stderr, "warning: %s\n", m)
		}
	}
	for _, id := range answeredInstead(r, runErr) {
		_, _ = fmt.Fprintf(stderr,
			"warning: comment %d was written by another identity and cannot be deleted; answering it instead\n", id)
	}
	if runErr != nil && r.Status(threadsflow.StageTakeDown) == flow.StatusNotReached {
		return threadsError(runErr)
	}

	if err := addThreadsRows(rep, r, opsPath, runErr); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if err := rep.Write(out, false, ui.ColorEnabled(out, false)); err != nil {
		return err
	}
	if runErr != nil {
		return threadsError(runErr)
	}
	return nil
}

// answeredInstead is the id of every comment take-down answered rather than
// deleted, whether it finished or failed partway: a failed stage has no
// output, so the answers it made before failing are read off its error.
func answeredInstead(r *flow.Result, runErr error) []int64 {
	if took, err := flow.Output[threadsstages.TakeDownOut](r, threadsflow.StageTakeDown); err == nil {
		return took.Answered
	}
	var failed *threadsstages.TakeDownError
	if errors.As(runErr, &failed) {
		return failed.Answered
	}
	return nil
}

// addThreadsRows renders what a run that reached its plan found, what was
// standing, what it planned, and — when it applied the plan — how that went.
//
// A run that failed taking down or answering threads adds no row for applying
// the plan: the error it returns is the whole of what it has to say about it.
// A run whose new threads were refused adds a failing row naming how many
// claims reached no surface, because the job log is where a reader looks for
// them.
func addThreadsRows(rep *ui.Report, r *flow.Result, opsPath string, runErr error) error {
	read, err := flow.Output[threadsstages.ReadFindingsOut](r, threadsflow.StageReadFindings)
	if err != nil {
		return err
	}
	trusted, err := flow.Output[truststages.Out](r, threadsflow.StageInitTrust)
	if err != nil {
		return err
	}
	loaded, err := flow.Output[scmstages.LoadPullRequestOut](r, threadsflow.StageLoadPullRequest)
	if err != nil {
		return err
	}
	listed, err := flow.Output[threadsstages.ListThreadsOut](r, threadsflow.StageListThreads)
	if err != nil {
		return err
	}
	planned, err := flow.Output[threadsstages.PlanOut](r, threadsflow.StagePlan)
	if err != nil {
		return err
	}
	ops := planned.Ops

	rep.Add(ui.Row{Status: ui.StatusContext, Label: "findings",
		Value: fmt.Sprintf("%d located of %d, %d duplicate(s) dropped", len(read.Located), read.Total, len(read.Dropped))})
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "threads",
		Value: fmt.Sprintf("%d standing on %s#%d", listed.Standing, trusted.Trusted.Repository(), loaded.Ref.Number)})
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "plan",
		Value: fmt.Sprintf("%d to open, %d to close, %d to answer — written to %s",
			len(ops.Create), len(ops.Delete), len(ops.Reply), opsPath)})

	switch r.Status(threadsflow.StageOpen) {
	case flow.StatusFailed:
		var openErr *threadsstages.OpenError
		if !errors.As(runErr, &openErr) {
			return threadsError(runErr)
		}
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "review",
			Value:  fmt.Sprintf("refused — %d located finding(s) reached no surface", openErr.Lost),
			Detail: []string{openErr.Err.Error(), "they are in this job's log and in the uploaded " + runner.ReportDir + " directory"}})
	case flow.StatusSucceeded:
		took, err := flow.Output[threadsstages.TakeDownOut](r, threadsflow.StageTakeDown)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%d opened, %d closed, %d answered",
			len(ops.Create), len(ops.Delete)-took.Refused, len(ops.Reply)+took.Refused)
		if took.Refused > 0 {
			value += fmt.Sprintf(" — %d delete(s) refused by the platform", took.Refused)
		}
		rep.Add(ui.Row{Status: ui.StatusPass, Label: "applied", Value: value})
	}
	return nil
}

// threadsError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it, with a missing credential and a missing
// or foreign event each named by this command.
//
// A repository trust refuses is reported in trust's own words. Its errors are
// untyped, and telling a missing repository from a malformed one apart would
// mean matching another package's prose.
func threadsError(err error) error {
	if errors.Is(err, scmstages.ErrNoCredential) {
		return noTokenError("threads")
	}
	var failed *flow.StageError
	if !errors.As(err, &failed) {
		return err
	}
	if failed.Stage == threadsflow.StageLoadPullRequest {
		return pullRequestError("threads", failed.Err)
	}
	return failed.Err
}

// commandFindingsReader reads a report directory the way every other command
// reading a report does, and hands the flow the findings its documents carry.
type commandFindingsReader struct{}

func (commandFindingsReader) Findings(dir string) ([]finding.Finding, error) {
	docs, err := readDocuments(dir)
	if err != nil {
		return nil, err
	}
	var found []finding.Finding
	for _, doc := range docs {
		found = append(found, doc.Findings...)
	}
	return found, nil
}
