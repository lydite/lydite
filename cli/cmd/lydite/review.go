package main

import (
	"context"
	"fmt"
	"path"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/gitstate"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

func newReviewCmd() *cobra.Command {
	var dir, base string
	var asJSON, noColor bool
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

review runs no check. It compares the change against the exemptions declared
in ` + referral.FileName + ` and reports one of two things: the change matches a
declared shape and may merge unattended, or it is referred to a person.

A referral names no defect. With no exemptions declared, every change is
referred — including a correct one.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			report := ui.NewReport("review")

			baseSHA, err := resolveReviewBase(ctx, dir, base)
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

			if referral.Dirty(ctx, dir) {
				report.Add(ui.Row{
					Status: ui.StatusUnmeasured,
					Label:  "uncommitted changes",
					Value:  "not included in this verdict",
				})
			}
			addDecisionRows(report, referral.Decide(change, file), len(file.Exemptions))

			if err := report.Write(cmd.OutOrStdout(), asJSON, ui.ColorEnabled(cmd.OutOrStdout(), noColor)); err != nil {
				return err
			}
			return report.Err()
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&base, "base", "auto", `commit this change is measured against ("auto" resolves the merge-base with origin/main)`)
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the machine-readable report instead of the terminal one")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
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
	case d.Empty:
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
	case d.Exemption != "":
		return fmt.Sprintf("%s matched, then disqualified", d.Exemption)
	case declared == 0:
		return "no exemptions declared"
	default:
		return "no exemption matched"
	}
}

// referralDetail is the reason, then the cause, then a runnable next step,
// per the copy rules in docs/design/tokens.md.
func referralDetail(d referral.Decision, declared int) []string {
	if d.Exemption != "" {
		return []string{
			"a disqualifier vetoes any exemption, and cannot be cleared by the change that produced it",
			"remove the annotation above, or ask a human to clear this change",
		}
	}
	var detail []string
	switch {
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

// resolveReviewBase turns --base into a commit.
//
// "auto" reuses the merge-base internal/gitstate already resolves for the
// coverage gate, so a change's scan, coverage and referral all agree on what
// "this change" means. An unresolvable "auto" is an error rather than a
// silent fallback: guessing a base would silently change which paths the
// verdict was computed from, and a shallow checkout is a fixable
// misconfiguration.
func resolveReviewBase(ctx context.Context, dir, base string) (string, error) {
	if base != "auto" {
		return base, nil
	}
	baseSHA, err := gitstate.BaseSHA(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("--base auto: %w (a full-history checkout is required — set fetch-depth: 0)", err)
	}
	return baseSHA, nil
}

// loadExemptionsAt reads the exemptions file out of the base commit, never
// out of the working tree.
//
// A change that widens the gate must get no benefit from its own widening.
// Reading the file from the branch would let one pull request declare itself
// exempt, which is the entire attack this ordering exists to remove. A
// missing file is the day-one state, not an error — it simply declares no
// exemptions, so everything is referred.
func loadExemptionsAt(ctx context.Context, dir, base string) (referral.File, error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return referral.File{}, err
	}
	repoPath := path.Join(prefix, referral.FileName)
	spec := base + ":" + repoPath
	r := executil.RunQuiet(ctx, dir, "git", "show", spec)
	if !r.Ok() {
		return referral.File{}, nil
	}
	return referral.Parse([]byte(r.Output), repoPath+" at "+shortSHA(base))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
