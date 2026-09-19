package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

func newReviewCmd() *cobra.Command {
	var dir, base, baseBranch, eventPath string
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
API of its Go module is compared against the merge-base. A break this change
did not declare fails, and a declared one is referred. For each dependency
manifest the change touches, the packages pinned at the merge-base and at HEAD
are compared. A package the merge-base did not pin is referred, and so is a
manifest whose dependencies could not be read at all.

An exemption declaring ` + "`versions: " + referral.VersionsPatchAndMinor + "`" + ` covers a change only when every
version it moves moved by a patch or a minor, and the licence and SCA rows for
every component ran and passed in a scan document under --reports. Without
--reports that condition is never met, and such a change is referred.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			report := ui.NewReport("review")

			baseSHA, err := resolveReviewBase(ctx, dir, base, baseBranch)
			if err != nil {
				return err
			}
			file, err := loadExemptionsAt(ctx, dir, baseSHA)
			if err != nil {
				return err
			}
			change, err := referral.Changes(ctx, dir, baseSHA)
			if err != nil {
				return err
			}

			// Measured before the decision, because a conditional exemption
			// is tested against how far the versions moved, and the rows the
			// same comparison carries are added after it.
			deltas := measureDependencies(ctx, dir, baseSHA, change.Paths)
			evidence := referral.Evidence{
				PatchAndMinor: versionsPatchAndMinor(deltas) &&
					dependencyGatesPassed(dir, reports, cmd.ErrOrStderr()),
			}

			decision := referral.Decide(change, file, evidence)
			if referral.Dirty(ctx, dir) {
				report.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "uncommitted changes",
					Value:  "not included in this verdict",
				})
			}
			// Before the decision is rendered, because a declared break and a
			// surface nothing could be compared are both referrals, and they
			// reach the report through the same disqualification the verdict
			// line is derived from.
			if err := addAPISurfaceRows(ctx, cmd, report, &decision, dir, baseSHA, eventPath); err != nil {
				return err
			}
			addDependencyRows(report, &decision, deltas, baseSHA)
			addDecisionRows(report, decision, len(file.Exemptions))

			// Published after the rows are added and from the report's own
			// verdict, so the status a machine reads and the report a person
			// reads are the same value rather than two derivations of it.
			if doPublish {
				target, err := resolveTarget("--publish", eventPath)
				if err != nil {
					return err
				}
				if err := publish(ctx, target, decision, report.Verdict()); err != nil {
					return err
				}
			}

			saveDocument(dir, report)

			if err := report.Write(cmd.OutOrStdout(), asJSON, ui.ColorEnabled(cmd.OutOrStdout(), noColor)); err != nil {
				return err
			}
			return report.Err()
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
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload naming the pull request (defaults to GITHUB_EVENT_PATH)")
	// Evidence only, never a gate of its own: an exemption conditioned on
	// `versions: patch-and-minor` needs a scan that ran, and a run given no
	// directory refers exactly as it does without the flag.
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory whose scan document supplies the licence and SCA evidence a conditional exemption needs; repeatable")
	return cmd
}

// listCap bounds every enumeration in the report.
//
// A referral on a large change can name hundreds of files, and a verdict a
// reader has to scroll past hundreds of lines to reach is one they stop
// reading. The cap is not an abbreviation of the finding — the finding is
// "this change is not exempt", which one example establishes as well as
// three hundred — it is an abbreviation of the evidence.
const listCap = 8

// capped truncates a list to listCap, replacing the tail with a count so the
// report never implies it showed everything.
func capped(items []string) []string {
	if len(items) <= listCap {
		return items
	}
	return append(items[:listCap:listCap], fmt.Sprintf("…and %d more", len(items)-listCap))
}

// addDecisionRows turns a decision into the report's rows.
//
// A referral says which of the two ways it got there — nothing covered the
// change, or something covered it and a disqualifier vetoed the match — since
// those have completely different remedies, and only one of them is a remedy
// the author can apply.
func addDecisionRows(report *ui.Report, d referral.Decision, declared int) {
	// The one failing row this command has, and the only thing it reports
	// that the author clears by doing more work rather than by fetching a
	// human — which is what makes it a gate and not a referral.
	if len(d.Bundled) > 0 {
		report.Add(ui.Row{
			Status: ui.StatusFail,
			Label:  "exemption change not isolated",
			Value:  fmt.Sprintf("%d other path(s) in the same change", len(d.Bundled)),
			Detail: append(capped(d.Bundled),
				referral.FileName+" must be the only path a change touches, so its history is the complete record of what may merge unread",
				"split this into two pull requests"),
		})
	}

	shown := d.Disqualifications
	if len(shown) > listCap {
		shown = shown[:listCap]
	}
	for _, dq := range shown {
		report.Add(ui.Row{Status: ui.StatusRefer, Label: dq.Kind, Value: dq.Evidence})
	}
	if rest := len(d.Disqualifications) - len(shown); rest > 0 {
		report.Add(ui.Row{Status: ui.StatusRefer, Label: "more disqualifiers",
			Value: fmt.Sprintf("%d not shown", rest)})
	}

	switch {
	// Empty alone is not enough: the API-surface check can refer a change
	// from its title or its commit messages with no path in the diff at all
	// — an empty commit, or a title edited after the last push, which the
	// `edited` trigger now re-runs on. Reading d.Empty on its own here would
	// print a passing "no changes" row beside a refer row that just fired,
	// contradicting the verdict this row states.
	case d.Empty && !d.Referred:
		report.Add(ui.Row{
			Status: ui.StatusPass,
			Label:  "referral",
			Value:  "no changes against the base",
		})
	case !d.Referred:
		report.Add(ui.Row{
			Status: ui.StatusPass,
			Label:  "referral",
			Value:  fmt.Sprintf("exempt: %s", d.Exemption),
		})
	default:
		report.Add(ui.Row{
			Status: ui.StatusRefer,
			Label:  "referral",
			Value:  referralReason(d, declared),
			Detail: referralDetail(d, declared),
		})
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

// resolveReviewBase turns --base into a full commit SHA, and refuses
// anything that is not one.
//
// "auto" reuses the merge-base internal/gitstate already resolves for the
// coverage gate, so a change's scan, coverage and referral all agree on what
// "this change" means. An unresolvable "auto" is an error rather than a
// silent fallback: guessing a base would silently change which paths the
// verdict was computed from, and a shallow checkout is a fixable
// misconfiguration.
//
// Every base is then resolved through `git rev-parse --verify` and required
// to be an ancestor of HEAD, and the resolved SHA — never the caller's
// string — is what reaches git afterwards. Three separate failures ride on
// this, and each one turns the gate into a rubber stamp rather than a
// blocker:
//
//   - An empty base makes the range read "..HEAD", which git resolves to an
//     empty diff. No changed paths means nothing to cover and no line to
//     scan, so the run passes with every disqualifier silent.
//   - An empty base also makes the exemptions spec read ":<path>", which git
//     resolves against the *index* — handing the branch control of the
//     allowlist the merge-base read exists to deny it.
//   - A base beginning with "-" lands in an argv position git reads as an
//     option, so "--base=--output=/tmp/x" is an arbitrary file write.
//
// A base that is not an ancestor of HEAD is refused for the same reason:
// the diff would describe something other than what this branch introduces.
func resolveReviewBase(ctx context.Context, dir, base, baseBranch string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("--base is empty: name a commit, or use \"auto\" to resolve the merge-base with the base branch")
	}
	if base == "auto" {
		baseSHA, err := gitstate.ResolveBaseSHA(ctx, dir, baseBranch)
		if err != nil {
			return "", fmt.Errorf("--base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
		}
		base = baseSHA
	}
	// --end-of-options stops a value beginning with "-" from being read as
	// an option, which is what makes verifying it here sufficient to protect
	// every later invocation.
	rev := executil.RunQuiet(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "--end-of-options", base+"^{commit}")
	resolved := strings.TrimSpace(rev.Output)
	if !rev.Ok() || resolved == "" {
		return "", fmt.Errorf("--base %q does not name a commit", base)
	}
	if r := executil.RunQuiet(ctx, dir, "git", "merge-base", "--is-ancestor", resolved, "HEAD"); !r.Ok() {
		return "", fmt.Errorf("--base %s is not an ancestor of HEAD, so the diff would not describe this branch", resolved[:12])
	}
	return resolved, nil
}

// loadExemptionsAt reads the exemptions file out of the base commit, never
// out of the working tree.
//
// A change that widens the gate must get no benefit from its own widening.
// Reading the file from the branch would let one pull request declare itself
// exempt, which is the entire attack this ordering exists to remove.
//
// An absent file is the day-one state and not an error: it declares no
// exemptions, so everything is referred. A file that exists and cannot be
// read is a different thing entirely, and showAtRevision keeps the two apart.
func loadExemptionsAt(ctx context.Context, dir, base string) (referral.File, error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return referral.File{}, err
	}
	repoPath := path.Join(prefix, referral.FileName)
	content, present, err := showAtRevision(ctx, dir, base, repoPath)
	if err != nil {
		return referral.File{}, err
	}
	if !present {
		return referral.File{}, nil
	}
	return referral.Parse(content, repoPath+" at "+shortSHA(base))
}

// showAtRevision reads a repository-root-relative path out of a commit,
// never out of the working tree, and says separately whether the commit has
// the path at all.
//
// The two questions are asked with two commands — `cat-file -e` answers "is
// it there", and only then does `show` read it. Collapsing them would make a
// broken read indistinguishable from a path the commit never had, and every
// caller here treats those differently: an absent exemptions file declares no
// exemptions, an absent manifest states no dependencies, and a read that
// failed states nothing at all.
func showAtRevision(ctx context.Context, dir, rev, repoPath string) ([]byte, bool, error) {
	spec := rev + ":" + repoPath
	if r := executil.RunQuiet(ctx, dir, "git", "cat-file", "-e", spec); !r.Ok() {
		return nil, false, nil
	}
	r := executil.RunQuiet(ctx, dir, "git", "show", spec)
	if !r.Ok() {
		return nil, true, fmt.Errorf("reading %s at %s: %w: %s",
			repoPath, shortSHA(rev), r.Err, strings.TrimSpace(r.Stderr))
	}
	return []byte(r.Output), true, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
