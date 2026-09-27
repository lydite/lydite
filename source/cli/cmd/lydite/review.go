package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/flow"
	reviewflow "lydite/lydite/internal/flows/review"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/reviewdecision"
	"lydite/lydite/internal/runner"
	reviewstages "lydite/lydite/internal/stages/review"
	scmstages "lydite/lydite/internal/stages/scm"
	"lydite/lydite/internal/ui"
)

func newReviewCmd() *cobra.Command {
	var dir, base, baseBranch, eventPath, surfacesPath, statusOut string
	var asJSON, noColor, doPublish bool
	var reports []string
	cmd := &cobra.Command{
		Use: "review",
		// A non-zero verdict is an answer, not a misuse of the command and
		// not a malfunction. Cobra prints usage and an "Error:" line for any
		// error a RunE returns, which would bury the report under the flag
		// list every time a gate failed. main owns error reporting.
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Decide whether this change needs a human before it merges",
		Long: `Decide whether this change needs a human before it merges.

review compares the change against the exemptions declared in ` + referral.FileName + `
and reports one of two things: the change matches a declared shape and may
merge unattended, or it is referred to a person.

A referral names no defect. With no exemptions declared, every change is
referred — including a correct one.

It runs two checks. For each component that declares api_surface, the exported
API of its Go module or its Rust crate is compared against the merge-base. A
break this change did not declare fails, and a declared one is referred. For
each dependency manifest the change touches, the packages pinned at the
merge-base and at HEAD are compared. A package the merge-base did not pin is
referred, and so is a manifest whose dependencies could not be read at all.

An exemption declaring ` + "`versions: " + referral.VersionsPatchAndMinor + "`" + ` covers a change only when every
version it moves moved by a patch or a minor, and the licence and SCA rows for
every component ran and passed in a scan document under --reports. Without
--reports that condition is never met, and such a change is referred.

--surfaces reads a comparison ` + "`review compare`" + ` already made instead of running it
here. A component's own comparison executes its own code — a Rust crate's
build.rs, a proc-macro — so the job that publishes with a credential should
not also be the job that ran it: compute in one job with none, decide and
publish in another that never runs the change's own code.

--publish --status-out <file> renders the commit status as a document instead
of posting it, for a step that posts it under lydite's App identity. Posting it
here is the path for a repository that has not adopted the reusable workflows,
and keeps every property of the status; the two are alternatives, not a ladder.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Refused before anything is measured. A flag that renders what
			// another flag decides, given on its own, would otherwise leave a
			// run that named a destination with nothing written to it and
			// nothing said about that.
			if statusOut != "" && !doPublish {
				return fmt.Errorf("%s renders the status --publish decides: pass both, or neither", statusOutFlag)
			}
			return runReviewFlow(cmd, reviewflow.Params{
				Dir:              dir,
				Base:             base,
				BaseBranch:       baseBranch,
				SurfacesDocument: surfacesPath,
				Toolchains:       commandToolchains{cmd},
				Progress:         cmd.ErrOrStderr(),
				// Resolved once, here: the title the decision reads and the
				// pull request a status names come from the same payload.
				EventPath:     firstNonEmpty(eventPath, os.Getenv("GITHUB_EVENT_PATH")),
				Reports:       reports,
				Scan:          commandScanReader{},
				TargetURL:     runURL(),
				StatusOut:     statusOut,
				Publish:       doPublish,
				PostsDirectly: doPublish && statusOut == "",
				RendersOnly:   doPublish && statusOut != "",
			}, asJSON, noColor)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&base, "base", "auto", `commit this change is measured against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	// Publishing is off by default and errors rather than skipping when the
	// platform's environment is absent, so a local review can neither post
	// by accident nor appear to have posted when it did not.
	cmd.Flags().BoolVar(&doPublish, "publish", false, "record the verdict as the "+clearance.Context+" commit status")
	// The rendered document is the whole of the write, so this needs no
	// credential: the step that posts it holds the identity, and this run
	// holds only the verdict.
	cmd.Flags().StringVar(&statusOut, "status-out", "",
		"with --publish, render the "+clearance.Context+" status as a JSON document at this path for another step to post, instead of posting it here")
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload naming the pull request (defaults to GITHUB_EVENT_PATH)")
	cmd.Flags().StringVar(&surfacesPath, "surfaces", "", "read a comparison 'review compare' already made instead of running it here, and decide from that instead")
	// Evidence only, never a gate of its own: an exemption conditioned on
	// `versions: patch-and-minor` needs a scan that ran, and a run given no
	// directory refers exactly as it does without the flag.
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory whose scan document supplies the licence and SCA evidence a conditional exemption needs; repeatable")
	cmd.AddCommand(newReviewCompareCmd())
	return cmd
}

// runReviewFlow runs the review flow and renders what it decided.
//
// The decision's warnings are written whether or not a later stage failed: a
// warning raised before a failure is part of what whoever investigates the
// failure reads. A run that failed writes no rows, no review.json and no
// report — the status it was asked to publish was not, and a report saying
// otherwise would be the only record a reader has.
func runReviewFlow(cmd *cobra.Command, p reviewflow.Params, asJSON, noColor bool) error {
	// Made first: a report times the run from its own creation.
	report := ui.NewReport("review")
	review, err := reviewflow.New()
	if err != nil {
		return err
	}
	r, err := review.Run(cmd.Context(), p.Inputs())
	if decided, outErr := flow.Output[reviewstages.DecideOut](r, reviewflow.StageDecide); outErr == nil {
		for _, warning := range decided.Warnings {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), warning)
		}
	}
	if err != nil {
		return reviewError(err, reviewRoute(p))
	}

	dirty, err := flow.Output[reviewstages.CheckDirtyOut](r, reviewflow.StageCheckDirty)
	if err != nil {
		return err
	}
	if dirty.Dirty {
		report.Add(ui.Row{
			Status: ui.StatusUnmeasured,
			Label:  "uncommitted changes",
			Value:  "not included in this verdict",
		})
	}
	decided, err := flow.Output[reviewstages.DecideOut](r, reviewflow.StageDecide)
	if err != nil {
		return err
	}
	if err := addOutcomeRows(report, decided.Result.Outcomes); err != nil {
		return err
	}

	saveDocument(p.Dir, report)

	if err := report.Write(cmd.OutOrStdout(), asJSON, ui.ColorEnabled(cmd.OutOrStdout(), noColor)); err != nil {
		return err
	}
	return report.Err()
}

// reviewRoute names the flag a publishing run's failure is reported against:
// the one that renders the status when it is rendered, and the one that
// posts it otherwise.
func reviewRoute(p reviewflow.Params) string {
	if p.RendersOnly {
		return statusOutFlag
	}
	return publishFlag
}

// reviewError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it, with a missing credential and a missing
// or foreign event each named by the flag that needed them.
//
// A repository trust refuses is reported in trust's own words. Its errors are
// untyped, and telling a missing repository from a malformed one apart would
// mean matching another package's prose.
func reviewError(err error, flag string) error {
	if errors.Is(err, scmstages.ErrNoCredential) {
		return noTokenError(flag)
	}
	var failed *flow.StageError
	if !errors.As(err, &failed) {
		return err
	}
	if failed.Stage == reviewflow.StageLoadPullRequest {
		return pullRequestError(flag, failed.Err)
	}
	return failed.Err
}

// commandScanReader reads a report directory's scan document the way every
// other command reading a report does, and hands reviewdecision its rows in
// reviewdecision's own terms.
type commandScanReader struct{}

func (commandScanReader) ReadScan(reports string) ([]reviewdecision.GateRow, error) {
	doc, err := readDocument(documentPath(reports, "scan"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", reviewdecision.ErrNoScanDocument, err)
	}
	if err != nil {
		return nil, err
	}
	rows := make([]reviewdecision.GateRow, 0, len(doc.Rows))
	for _, row := range doc.Rows {
		rows = append(rows, reviewdecision.GateRow{Label: row.Label, Passed: row.Status == ui.StatusPass})
	}
	return rows, nil
}

// listCap bounds every enumeration in the report, by the cap the decision's
// own evidence is bounded by.
const listCap = reviewdecision.ListCap

// capped truncates a list to listCap, replacing the tail with a count so the
// report never implies it showed everything.
func capped(items []string) []string {
	return reviewdecision.Capped(items)
}

// rowStatus renders each status reviewdecision decides as exactly one row
// status, so the report's verdict is folded over the same answers the
// decision's own verdict is.
var rowStatus = map[reviewdecision.Status]ui.Status{
	reviewdecision.StatusPass:      ui.StatusPass,
	reviewdecision.StatusRefer:     ui.StatusRefer,
	reviewdecision.StatusFail:      ui.StatusFail,
	reviewdecision.StatusNonVoting: ui.StatusUnmeasured,
}

// addOutcomeRows renders every outcome reviewdecision.Decide reached, in its
// own order, and decides nothing: each row's status is the outcome's, and only
// the words and how much evidence to show are chosen here.
//
// An outcome whose status or kind has no rendering here is an error rather
// than a row: a status rendered as nothing in particular would stop voting,
// and a concern left out of the report is indistinguishable from one that
// passed.
func addOutcomeRows(report *ui.Report, outcomes []reviewdecision.Outcome) error {
	for _, o := range outcomes {
		if _, ok := rowStatus[o.Status]; !ok {
			return fmt.Errorf("review: %s outcome has status %q, which no row renders", o.Kind, o.Status)
		}
	}
	for i := 0; i < len(outcomes); i++ {
		o := outcomes[i]
		status := rowStatus[o.Status]
		switch o.Kind {
		case reviewdecision.KindAPISurface:
			report.Add(apiSurfaceRow(o, status))
		case reviewdecision.KindDependencies:
			report.Add(dependencyRow(o, status))
		case reviewdecision.KindBundling:
			report.Add(bundlingRow(o, status))
		case reviewdecision.KindDisqualification:
			// A run of disqualifications is rendered together, because how
			// many of them to show is decided over the whole run.
			end := i + 1
			for end < len(outcomes) && outcomes[end].Kind == reviewdecision.KindDisqualification {
				end++
			}
			addDisqualificationRows(report, outcomes[i:end])
			i = end - 1
		case reviewdecision.KindReferral:
			report.Add(referralRow(o, status))
		default:
			return fmt.Errorf("review: outcome kind %q has no row", o.Kind)
		}
	}
	return nil
}

// bundlingRow is the exemptions file changed alongside other paths: a gate,
// not a referral, because the author clears it by splitting the change.
func bundlingRow(o reviewdecision.Outcome, status ui.Status) ui.Row {
	return ui.Row{
		Status: status,
		Label:  "exemption change not isolated",
		Value:  fmt.Sprintf("%d other path(s) in the same change", len(o.Bundled)),
		Detail: append(capped(o.Bundled),
			referral.FileName+" must be the only path a change touches, so its history is the complete record of what may merge unread",
			"split this into two pull requests"),
	}
}

// addDisqualificationRows renders a run of disqualifications, the first
// listCap of them each on its own row and the rest as a count.
//
// The count carries the status of the first disqualification it stands for,
// and the referral row after it refers whenever any disqualification does, so
// the rows left out never take their severity with them.
func addDisqualificationRows(report *ui.Report, run []reviewdecision.Outcome) {
	shown := run
	if len(shown) > listCap {
		shown = shown[:listCap]
	}
	for _, o := range shown {
		report.Add(ui.Row{Status: rowStatus[o.Status], Label: o.Disqualification.Kind, Value: o.Disqualification.Evidence})
	}
	if rest := run[len(shown):]; len(rest) > 0 {
		report.Add(ui.Row{Status: rowStatus[rest[0].Status], Label: "more disqualifiers",
			Value: fmt.Sprintf("%d not shown", len(rest))})
	}
}

// referralRow states the referral.
//
// A referral says which of the two ways it got there — nothing covered the
// change, or something covered it and a disqualifier vetoed the match — since
// those have completely different remedies, and only one of them is a remedy
// the author can apply.
func referralRow(o reviewdecision.Outcome, status ui.Status) ui.Row {
	d := o.Decision
	switch {
	case o.Status != reviewdecision.StatusPass:
		return ui.Row{
			Status: status,
			Label:  "referral",
			Value:  referralReason(d, o.Exemptions),
			Detail: referralDetail(d, o.Exemptions),
		}
	// Only a referral that passed says there was nothing to review: the
	// API-surface check can refer a change from its title or its commit
	// messages with no path in the diff at all — an empty commit, or a title
	// edited after the last push, which the `edited` trigger re-runs on — and
	// a passing "no changes" row beside the refer row that fired would
	// contradict the verdict this row states.
	case d.Empty:
		return ui.Row{Status: status, Label: "referral", Value: "no changes against the base"}
	default:
		return ui.Row{Status: status, Label: "referral", Value: fmt.Sprintf("exempt: %s", d.Exemption)}
	}
}

func referralReason(d referral.Decision, declared int) string {
	switch {
	// Decide returns before the exemption match ever runs when the diff is
	// empty, so a disqualification reaching the report here came from outside
	// that loop entirely — the API-surface check's declaration or an
	// uncomputable surface. Naming an exemption outcome would describe a step
	// that never executed.
	case d.Empty:
		return "a disqualifier reached the change with no path in the diff at all"
	case d.Exemption != "":
		return fmt.Sprintf("%s matched, then disqualified", d.Exemption)
	case len(d.Unsatisfied) > 0:
		return fmt.Sprintf("%s covers these paths, and its condition was not met", strings.Join(capped(d.Unsatisfied), ", "))
	case declared == 0:
		return "no exemptions declared"
	default:
		return "no exemption matched"
	}
}

// referralDetail is the reason, then the cause, then a runnable next step,
// per the copy rules in docs/design/tokens.md.
func referralDetail(d referral.Decision, declared int) []string {
	if d.Empty {
		return []string{
			"the disqualifier row above stands on its own evidence, not on a changed path",
			remedyFor(d.Disqualifications),
		}
	}
	if d.Exemption != "" {
		return []string{
			"a disqualifier vetoes any exemption, and cannot be cleared by the change that produced it",
			remedyFor(d.Disqualifications),
		}
	}
	var detail []string
	switch {
	// Before the uncovered list, because an exemption that covered every path
	// and failed its condition leaves nothing uncovered, and the reader would
	// otherwise be told every path is covered by more than one exemption —
	// sending them after a second declaration that is not there.
	case len(d.Unsatisfied) > 0:
		return []string{
			"a conditional exemption covers the paths, and the condition is a further test the change has to pass",
			"every dependency version must move by a patch or a minor, and the licence and SCA rows for every component must have run and passed in a scan document given to --reports",
			"ask a human to clear this change",
		}
	case len(d.Uncovered) > 0:
		detail = append(detail, fmt.Sprintf("%d path(s) covered by no exemption:", len(d.Uncovered)))
		detail = append(detail, capped(d.Uncovered)...)
	case declared > 0:
		// Every path is covered by something, but by no single exemption.
		// Saying "no exemption matched" alone would send the reader looking
		// for a path that is not there.
		detail = append(detail,
			"every path is covered, but by more than one exemption — a change must match a single declared shape")
	}
	if declared == 0 {
		detail = append(detail, fmt.Sprintf("%s declares no exemptions, so every change is referred", referral.FileName))
	}
	return append(detail, "ask a human to clear this change")
}

// remedyFor names the way out that matches what actually fired. Only some
// disqualifiers are annotations a change can drop; telling the author to
// remove one when they edited a workflow file sends them looking for
// something that is not there, and the remedy is the single actionable line
// in the whole report.
func remedyFor(ds []referral.Disqualification) string {
	annotations, declaredBreak := false, false
	for _, d := range ds {
		switch d.Kind {
		case "suppression added", "test disabled":
			annotations = true
		case referral.DisqualificationAPIBreakDeclared:
			declaredBreak = true
		}
	}
	if annotations {
		return "drop the annotation named above, or ask a human to clear this change"
	}
	// The author did write the declaration, unlike the vetoes below, but
	// dropping it does not clear an actual break — it only turns this back
	// into the undeclared-break gate. It is a way out of the referral only
	// where nothing else made this change one.
	if declaredBreak {
		return "removing the declaration does not clear a real break, only which verdict it gets — ask a human to clear this change"
	}
	return "these are not annotations a change can drop — ask a human to clear this change"
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
