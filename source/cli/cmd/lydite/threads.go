package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/threads"
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

func runThreads(cmd *cobra.Command, reports []string, opsPath, eventPath string, apply bool) error {
	target, err := resolveTarget("threads", eventPath)
	if err != nil {
		return err
	}
	rep := ui.NewReport("threads")

	found, missing := readFindings(reports)
	located, dropped := threads.Dedup(threads.Located(found))
	for _, fp := range dropped {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %s was reported twice and one copy is dropped; one claim is one thread\n", fp)
	}
	for _, m := range missing {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", m)
	}
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "findings",
		Value: fmt.Sprintf("%d located of %d, %d duplicate(s) dropped", len(located), len(found), len(dropped))})

	ctx := cmd.Context()
	standing, err := target.Client.ReviewComments(ctx, target.Repo, target.Number)
	if err != nil {
		return err
	}
	grouped := threads.Threads(standing)
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "threads",
		Value: fmt.Sprintf("%d standing on %s#%d", len(grouped), target.Repo, target.Number)})

	document := threads.Delta(located, grouped, target.Number, target.SHA)
	if err := writeOps(opsPath, document); err != nil {
		return err
	}
	rep.Add(ui.Row{Status: ui.StatusContext, Label: "plan",
		Value: fmt.Sprintf("%d to open, %d to close, %d to answer — written to %s",
			len(document.Create), len(document.Delete), len(document.Reply), opsPath)})

	var applyErr error
	if apply {
		applyErr = applyOps(ctx, target, document, rep, cmd.ErrOrStderr())
	}

	out := cmd.OutOrStdout()
	if err := rep.Write(out, false, ui.ColorEnabled(out, false)); err != nil {
		return err
	}
	return applyErr
}

// readFindings collects every located claim the named directories hold.
//
// A directory that could not be read is named and does not stop the run, for
// the standing comment's reason: the claims that did arrive are still worth
// putting on their lines, and a missing input is already rendered as a section
// saying so in the comment the same run publishes.
func readFindings(dirs []string) (found []finding.Finding, missing []string) {
	for _, dir := range dirs {
		docs, err := readDocuments(dir)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s holds no findings: %v", dir, err))
			continue
		}
		for _, doc := range docs {
			found = append(found, doc.Findings...)
		}
	}
	return found, missing
}

// writeOps puts the operations where the caller asked for them.
//
// Always, and never into .lydite-reports/. The document is lydite's own wire
// between the delta and whatever applies it, and nothing about it is promised
// to a consumer — `lydite test plan` is the precedent for a command that
// reaches no verdict writing no report document.
func writeOps(path string, document threads.Ops) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// applyOps performs the operations with the job's own token.
//
// This is the `github-token` fallback, and it is a designed path rather than a
// stopgap: a consumer who has installed nothing still gets its threads, from
// `github-actions[bot]`, and the only thing they lose is whose name is on
// them.
//
// A review the platform refuses fails the run, naming how many claims reached
// no surface. Never a silent green — the claims are still in the terminal, the
// job log and the uploaded report directory, and a run that quietly posted
// nothing is indistinguishable from a change with nothing wrong with it.
//
// A refused delete is not a failure. The platform will not let one identity
// delete another's comment, so the thread is left standing with the reply the
// document already carries for that case — the same path a thread somebody
// else spoke in takes.
func applyOps(ctx context.Context, target publishTarget, document threads.Ops, rep *ui.Report, stderr io.Writer) error {
	var refused int
	for _, del := range document.Delete {
		err := target.Client.DeleteReviewComment(ctx, target.Repo, del.Comment)
		if err == nil {
			continue
		}
		if !forge.Forbidden(err) {
			return err
		}
		refused++
		_, _ = fmt.Fprintf(stderr, "warning: comment %d was written by another identity and cannot be deleted; answering it instead\n", del.Comment)
		if err := target.Client.ReplyToReviewComment(ctx, target.Repo, target.Number, del.Comment, del.Refused); err != nil {
			return err
		}
	}
	for _, reply := range document.Reply {
		if err := target.Client.ReplyToReviewComment(ctx, target.Repo, target.Number, reply.Comment, reply.Body); err != nil {
			return err
		}
	}
	if err := target.Client.CreateReview(ctx, target.Repo, target.Number, document.Head, document.Create); err != nil {
		rep.Add(ui.Row{Status: ui.StatusFail, Label: "review",
			Value:  fmt.Sprintf("refused — %d located finding(s) reached no surface", len(document.Create)),
			Detail: []string{err.Error(), "they are in this job's log and in the uploaded " + runner.ReportDir + " directory"}})
		return fmt.Errorf("%d located finding(s) reached no surface: %w", len(document.Create), err)
	}
	value := fmt.Sprintf("%d opened, %d closed, %d answered",
		len(document.Create), len(document.Delete)-refused, len(document.Reply)+refused)
	if refused > 0 {
		value += fmt.Sprintf(" — %d delete(s) refused by the platform", refused)
	}
	rep.Add(ui.Row{Status: ui.StatusPass, Label: "applied", Value: value})
	return nil
}
