// Package mutationstages holds the stages `lydite mutation` runs: loading the
// declaration, provisioning each component's toolchain, resolving the base the
// mutants come from, selecting what the change could have broken, scoping the
// change, running every selected component's mutants, and recording what
// became of them. Named apart from internal/mutation so a flow definition can
// import both without renaming either.
//
// Every stage is a plain function of its own In. What a stage learns about a
// component it returns as data — an OutcomeKind and the facts that outcome
// carries — and never as a report row: which row an outcome becomes is the
// command's to decide. What a component's own lifecycle already decided a row
// for (a plan that could not be made, a preparation, a service or a setup
// command that failed) arrives here as an opaque error from Lifecycle, and is
// handed back unread.
package mutationstages

import (
	"context"
	"io"

	"lydite/lydite/internal/affected"
	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scheduler"
	"lydite/lydite/internal/toolchain"
)

// Toolchains provisions each component's language toolchain, and returns one
// environment per component.
type Toolchains interface {
	Ensure(ctx context.Context, dir string, cfg config.Config, components []component.Component) (toolchain.Envs, error)
}

// AffectedFunc selects, from the declaration in file, the components the
// change against base could have broken.
type AffectedFunc func(ctx context.Context, dir string, file component.File, base string) (affected.Result, error)

// Shape answers what a component's declaration implies: how its suite is
// invoked, which language it runs in, the environment a command of its runs
// under, whether it declares a suite at all, and which changed lines its own
// coverage could speak for.
//
// One answer for every command that asks, which is why it is injected rather
// than derived here: a copy of any of these would agree with the coverage gate
// and `lydite test` only until one of them changed.
type Shape interface {
	// Invocation is the component's suite in one variant.
	Invocation(c component.Component, v runner.Variant) (runner.Invocation, error)
	// Lang is the language the component's runner implies, and empty for a
	// raw command.
	Lang(c component.Component) runner.Lang
	// Env is the environment a command of the component's runs under: its
	// toolchain, its declared variables, and the directories inv needs on
	// PATH.
	Env(tc *toolchain.Env, c component.Component, inv runner.Invocation) []string
	// NoSuite reports whether the component declares no suite at all.
	NoSuite(c component.Component) bool
	// Scope is the part of changed the component's own coverage could
	// contain: under its directory, and written in its language.
	Scope(changed map[string][]int, c component.Component) map[string][]int
}

// Lifecycle is everything around a component's suite that the command owns:
// its plan and its log, its report path, its preparation, its services, and
// its setup and teardown commands.
//
// Each method names the component it acts on, and every error it returns is
// opaque here: it is a failure whose row the command has already decided, and
// RunMutants hands it back as a KindBlocked outcome, or as a TeardownErr,
// without reading it.
type Lifecycle interface {
	// Plan opens each selected component's log and plans it, one Planned per
	// component and in selected's order.
	Plan(ctx context.Context, root string, selected []component.Component, stream bool) []Planned
	// Close closes every log Plan opened, flushing whatever mirrors them.
	Close()
	// ClearReport removes the coverage report a component's instrumented
	// run is about to write, so what is read back is that run's.
	ClearReport(dir, report string) error
	// Prepare puts in place what inv's runner needs in dir. root bounds any
	// walk upward from dir, and is empty for a directory inside a copy of the
	// scan root rather than inside the scan root itself.
	Prepare(ctx context.Context, name string, inv runner.Invocation, dir, root string, cfg config.Config, tc *toolchain.Env) error
	// StartServices brings the component's services up. stop tears them
	// down, and is never nil when err is.
	StartServices(ctx context.Context, name string) (stop func(), err error)
	// RunCommands runs the component's setup or teardown commands, kind
	// naming which, in order and stopping at the first that fails.
	RunCommands(ctx context.Context, name, dir, kind string, cmds []string, tc *toolchain.Env) error
}

// Planned is one component as Lifecycle.Plan planned it.
type Planned struct {
	Component component.Component
	// Log is where the component's commands write.
	Log io.Writer
	// LogRel is the log's path relative to the scan root, and empty when
	// there is no file behind Log.
	LogRel string
	// Ready reports whether the component can be scheduled at all. One that
	// cannot is never started, and NotReady is why.
	Ready    bool
	NotReady error
	// Ports are the host ports the component's services publish.
	Ports []int
	// Item is what the scheduler locks on for the component.
	Item scheduler.Item
}
