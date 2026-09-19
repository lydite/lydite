package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// verdictRecord is the whole of what describe (status.go) needs to render the
// `lydite/referral` status, serialised so a job that never computed the
// decision can still post it.
//
// review's own api_surface comparison runs a pull request's own code — a
// Rust crate's build.rs, a proc-macro — and the job that publishes the
// verdict is the one holding a credential with `statuses: write`. Splitting
// "compute the verdict" from "publish it" into two jobs is what keeps that
// credential out of a process tree that ever executes the change under
// review; see .github/workflows/lydite-pr.yml's referral and referral-publish
// jobs.
type verdictRecord struct {
	Verdict   ui.Verdict `json:"verdict"`
	Exemption string     `json:"exemption,omitempty"`
	Empty     bool       `json:"empty,omitempty"`
}

// writeVerdict records the whole of what publish (status.go) needs, at path.
func writeVerdict(path string, d referral.Decision, verdict ui.Verdict) error {
	f, err := os.Create(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return fmt.Errorf("writing the verdict record: %w", err)
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(verdictRecord{Verdict: verdict, Exemption: d.Exemption, Empty: d.Empty})
}

// readVerdict is writeVerdict's inverse.
func readVerdict(path string) (referral.Decision, ui.Verdict, error) {
	f, err := os.Open(path) // #nosec G304 -- a workflow's own artifact path, not attacker-controlled
	if err != nil {
		return referral.Decision{}, "", fmt.Errorf("reading the verdict record: %w", err)
	}
	defer func() { _ = f.Close() }()
	var v verdictRecord
	if err := json.NewDecoder(f).Decode(&v); err != nil {
		return referral.Decision{}, "", fmt.Errorf("reading the verdict record: %w", err)
	}
	return referral.Decision{Exemption: v.Exemption, Empty: v.Empty}, v.Verdict, nil
}

func newReviewPublishCmd() *cobra.Command {
	var verdictPath, eventPath string
	cmd := &cobra.Command{
		Use:           "publish",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Record a verdict review already computed as the " + clearance.Context + " commit status",
		Long: `Record a verdict review already computed as the ` + clearance.Context + ` commit status.

Nothing here is computed: --verdict names a document review --write-verdict
wrote, so this command reads it and posts, without running any comparison and
without needing GITHUB_TOKEN anywhere near the code the comparison ran over.
It exists to let the verdict be computed in a job that holds no credential and
published from a separate one that does.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if verdictPath == "" {
				return fmt.Errorf("--verdict names the document review --write-verdict wrote")
			}
			ctx := cmd.Context()
			d, verdict, err := readVerdict(verdictPath)
			if err != nil {
				return err
			}
			target, err := resolveTarget("review publish", eventPath)
			if err != nil {
				return err
			}
			if err := publish(ctx, target, d, verdict); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "posted %s for %s\n", verdict, target.SHA)
			return err
		},
	}
	cmd.Flags().StringVar(&verdictPath, "verdict", "", "the document review --write-verdict wrote")
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload naming the pull request (defaults to GITHUB_EVENT_PATH)")
	return cmd
}
