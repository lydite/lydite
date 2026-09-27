package main

import (
	"context"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/config"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/toolchain"
)

// ensureToolchains is testrun.EnsureToolchains, with its diagnostics on the
// command's stderr.
func ensureToolchains(ctx context.Context, cmd *cobra.Command, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
	return testrun.EnsureToolchains(ctx, cmd.ErrOrStderr(), dir, cfg, units)
}
