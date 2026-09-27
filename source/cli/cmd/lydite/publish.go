package main

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/flow"
	publishflow "lydite/lydite/internal/flows/publish"
	"lydite/lydite/internal/runner"
	publishstages "lydite/lydite/internal/stages/publish"
	"lydite/lydite/internal/ui"
)

// newPublishCmd renders the standing pull-request comment from the documents
// one or more runs wrote.
//
// It is pure: no network, no token, and nothing about a hosting platform. What
// it emits is markdown on stdout or in a file, and posting that is a separate
// step with a separate identity. Two things follow, and both are the point. A
// developer can run it locally and read exactly what a reviewer would see. And
// refining the comment never needs a release of whatever posts it, which is
// the coupling that made every change to the human surface a two-repository
// release the last time.
func newPublishCmd() *cobra.Command {
	var (
		reports []string
		expect  []string
		out     string
		base    string
	)
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Render the standing pull-request comment from one or more report directories",
		Long: "Render the standing pull-request comment from the report documents lydite wrote.\n\n" +
			"Each --reports directory is a " + runner.ReportDir + " directory: one per job that ran a\n" +
			"lydite command, or one for every command when they shared a scan root. Nothing is\n" +
			"posted — the markdown goes to --out, and posting it is a separate step.\n\n" +
			"--expect names the commands this run was supposed to produce a report for. One that\n" +
			"reaches no --reports directory at all renders as an unmeasured section naming it,\n" +
			"rather than being absent from the comment.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(reports) == 0 {
				return errors.New("no report directories: pass --reports <dir>, once per directory")
			}
			render, err := publishflow.New()
			if err != nil {
				return err
			}
			_, err = render.Run(cmd.Context(), publishflow.Params{
				Dirs:          reports,
				Expect:        expect,
				Base:          base,
				Version:       version,
				ReadDocuments: readDocuments,
				ReadLog:       readLog,
				TailLines:     tailLines,
				Out:           out,
				Stdout:        cmd.OutOrStdout(),
			}.Inputs())
			return publishError(err)
		},
	}
	cmd.Flags().StringSliceVar(&reports, "reports", nil,
		"a "+runner.ReportDir+" directory to read; repeatable")
	cmd.Flags().StringSliceVar(&expect, "expect", nil,
		"a command this run expected a report from; repeatable, and unmeasured when none arrived")
	cmd.Flags().StringVar(&out, "out", "-", `file to write the comment to ("-" is stdout)`)
	cmd.Flags().StringVar(&base, "base", "", "commit the change was measured against, for the footer")
	return cmd
}

// publishError is a run's failure as this command reports it: the stage's own
// error, not the flow's framing of it.
func publishError(err error) error {
	var failed *flow.StageError
	if errors.As(err, &failed) {
		return failed.Err
	}
	return err
}

// buildComment is the comment the publish flow renders from dirs, run exactly
// as the command runs it with the rendered body discarded.
//
// Every error it can see means the flow is miswired rather than that a
// directory could not be read — an unreadable directory is a section of the
// comment, not a failure — so it panics rather than return an empty comment a
// caller would read as one that rendered nothing.
func buildComment(dirs []string, base string, expect ...string) ui.Comment {
	render, err := publishflow.New()
	if err != nil {
		panic(err)
	}
	r, err := render.Run(context.Background(), publishflow.Params{
		Dirs:          dirs,
		Expect:        expect,
		Base:          base,
		Version:       version,
		ReadDocuments: readDocuments,
		ReadLog:       readLog,
		TailLines:     tailLines,
		Out:           "-",
		Stdout:        io.Discard,
	}.Inputs())
	if err != nil {
		panic(err)
	}
	built, err := flow.Output[publishstages.BuildOut](r, publishflow.StageBuildComment)
	if err != nil {
		panic(err)
	}
	return built.Comment
}
