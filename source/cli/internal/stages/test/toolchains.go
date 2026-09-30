package teststages

import (
	"context"
	"io"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	testrun "lydite/lydite/internal/test/run"
	"lydite/lydite/internal/toolchain"
)

// ToolchainsIn is the set whose toolchains are made present, and where.
type ToolchainsIn struct {
	Dir    string
	Config config.Config
	// Own is the set this run may execute, never the set affected selection
	// ends up choosing: a component deselected on one run and selected on the
	// next must not resolve a different toolchain, and selection is not known
	// until after the git walk it feeds.
	Own []component.Component
	// Stderr is where provisioning reports what it resolved.
	Stderr io.Writer
}

// ToolchainsOut is the environment every command run for each component
// needs.
type ToolchainsOut struct {
	Envs toolchain.Envs
}

// Toolchains makes each component's toolchain present at the version its own
// directory declares.
//
// Before any suite runs, because `lydite test` is the command that invokes
// `go test`, `cargo llvm-cov` and `npx vitest`: a runner with no Node answers
// `npx: not found` rather than provisioning one, and a runner whose ambient Go
// is fine still needs GOTOOLCHAIN pinned. It is resolved here rather than
// inherited from a `lydite scan` earlier in the same job, since the result is a
// value handed to each component's own commands, not a change to this process.
func Toolchains(ctx context.Context, in ToolchainsIn) (ToolchainsOut, error) {
	envs, err := testrun.EnsureToolchains(ctx, orDiscard(in.Stderr), in.Dir, in.Config, testrun.ComponentUnits(in.Dir, in.Own))
	if err != nil {
		return ToolchainsOut{}, err
	}
	return ToolchainsOut{Envs: envs}, nil
}
