package main

import (
	"context"

	"github.com/spf13/cobra"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/toolchain"
)

// ensureToolchains makes each unit's language toolchain available at the
// version its own directory declares, and returns the environment every
// command run for that component needs.
//
// Called by both `scan` and `test`, and in both cases before any tool runs.
// Wiring it into the two command entry points rather than into each internal
// package keeps it to one call site per command, and means the resolution
// happens once per invocation rather than once per component that shares a
// requirement.
//
// The environment is returned rather than applied to this process. Components
// run concurrently, so a toolchain written into the process environment is one
// every other component inherits — which is the whole of what "one Node
// version per repository" was.
//
// Diagnostics go to stderr. They are preparation notes, not gate results, and
// stdout carries the report — under --json, a document a stray line would make
// unparseable.
func ensureToolchains(ctx context.Context, cmd *cobra.Command, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error) {
	return toolchain.Ensure(ctx, dir, units, toolchainOverrides(cfg), cmd.ErrOrStderr())
}

// toolchainOverrides maps .lydite/config.yml onto the toolchain package's input.
// The mapping is explicit rather than internal/toolchain importing
// internal/config, so config stays a leaf that every other package can depend
// on without a cycle.
func toolchainOverrides(cfg config.Config) toolchain.Overrides {
	return toolchain.Overrides{
		Disabled: !cfg.Toolchain.Enabled,
		Go:       cfg.Toolchain.Go,
		Rust:     cfg.Toolchain.Rust,
		Node:     cfg.Toolchain.Node,
	}
}
