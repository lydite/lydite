package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/nodedeps"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/toolchain"
	"lydite/lydite/internal/ui"
)

// Prepare puts what the runner needs in place, and reports a failing row
// rather than letting the suite start without it.
//
// A JavaScript suite run without its node_modules fails at import, naming the
// tests rather than the absent dependencies, and a Rust one without its pinned
// runner fails with `no such command` — the same misattribution a suite run
// without its database produces, and the same reason to stop first.
//
// root is the tree dir was declared under — the scan root for every caller
// but one: the mutation worker's closure passes "" instead, because its dir
// sits inside a copy of the scan root and not the scan root itself.
func Prepare(ctx context.Context, inv runner.Invocation, dir, root, label string, c component.Component, cfg config.Config, tc *toolchain.Env, out Output) (ui.Row, bool) {
	// Two environments, because two different people's software gets
	// installed here: the repository's dependencies with what the repository
	// declared, and lydite's pinned runners with lydite's toolchain alone.
	env := executil.Env{Check: ChildEnv(tc, c, inv), Install: tc.Environ()}
	if row, ok := refusedManager(dir, root, label, c, cfg, out); !ok {
		return row, false
	}
	r, ok := runner.Lookup(c.Runner)
	if !ok {
		return prepareCommand(ctx, dir, root, label, c, cfg, env, out)
	}
	if r.Prepare == nil {
		return ui.Row{}, true
	}
	if err := r.Prepare(ctx, inv, dir, root, cfg.TypeScript.Install, env, out.W); err != nil {
		row := Failure(label, out.Rel, err.Error(), "not prepared", "")
		if r.Lang == runner.TypeScript {
			row.Detail = append(row.Detail, installHint)
		}
		return row, false
	}
	return ui.Row{}, true
}

// refusedManager fails a component whose node install would run under a
// package manager lydite refuses (see nodedeps.Refusal), before any install
// starts.
//
// The refusal is the component's own: nothing was provisioned for the pin, and
// every other component in the run carries on. toolchain.enabled: false lifts
// it, because provisioning is then the machine's to arrange and the install
// runs whatever manager is on PATH.
func refusedManager(dir, root, label string, c component.Component, cfg config.Config, out Output) (ui.Row, bool) {
	if !cfg.Toolchain.Enabled || !installsNodeDeps(dir, c) {
		return ui.Row{}, true
	}
	if err := nodedeps.Refusal(dir, root); err != nil {
		row := Failure(label, out.Rel, err.Error(), "not prepared", "")
		row.Detail = append(row.Detail, refusalHint)
		return row, false
	}
	return ui.Row{}, true
}

// refusalHint is the way out of a refused package manager pin.
const refusalHint = "Pin a supported release in packageManager, or set toolchain.enabled: false in " + config.FileName + " to install with the package manager on PATH."

// prepareCommand installs the node dependencies of a component that names no
// runner, so that its command runs over a workspace lydite installed.
//
// A package manager asked to run a script in a workspace it finds uninstalled
// installs the whole workspace itself, unasked and uncoordinated. That install
// writes the same node_modules tree as the one lydite runs for every other
// component resolving that root, and two of them over one tree is a rename
// race whose loser fails inside a package manager rather than in anything
// lydite reports. Going through nodedeps makes the two one install, because
// coalescing there is keyed on the root and knows nothing about runners.
func prepareCommand(ctx context.Context, dir, root, label string, c component.Component, cfg config.Config, env executil.Env, out Output) (ui.Row, bool) {
	if !installsNodeDeps(dir, c) {
		return ui.Row{}, true
	}
	if err := nodedeps.Install(ctx, dir, root, cfg.TypeScript.Install, env.Check, out.W); err != nil {
		row := Failure(label, out.Rel, err.Error(), "not prepared", "")
		row.Detail = append(row.Detail, installHint)
		return row, false
	}
	return ui.Row{}, true
}

// installsNodeDeps reports whether lydite installs this component's node
// dependencies before its suite runs.
//
// A runner answers for its own language, and only the JavaScript ones install
// anything. A component declaring a raw command names no runner and therefore
// no language, and "under a JavaScript workspace root" answers the question
// wrongly on its own: a Go or Rust component sitting beside that root's
// packages resolves the same root, and handing it a node install it never
// asked for is slower and stranger than the race not installing it avoids. A
// package.json in the component's own directory is what makes it one of that
// workspace's packages.
func installsNodeDeps(dir string, c component.Component) bool {
	if r, ok := runner.Lookup(c.Runner); ok {
		return r.Prepare != nil && r.Lang == runner.TypeScript
	}
	if len(c.Command) == 0 {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "package.json"))
	return err == nil && !info.IsDir()
}

// installHint is the way out of a failed install: the repository says how its
// dependencies go in when detection does not match.
const installHint = "Set typescript.install in " + config.FileName + " if this component's install replaces detection entirely, or add a setup: line if it just needs one more step."

// installLabel is how every row about one component's install is named.
//
// Labelled the way TestLabel is, and for the same reason: a component called
// `install` must not be able to take this row, and a consumer keying rows by
// label would silently lose one of the two.
func installLabel(name string) string { return "install(" + name + ")" }

// installNote is the row a component's install takes when there is
// something to say about where or whether it runs, and false for one whose
// dependencies lydite installs the ordinary way, with nothing to add.
//
// A typescript.install override is one of those things: it may resolve a
// workspace root the same way detection does and run there, coalesced with
// every sibling that resolves the same root, or it may find no such root and
// run alone in the component's own directory. Either is a fact about the
// tree a reader of the report cannot get from the command itself, so this
// names where the override is going to run — not whether it succeeds. This
// runs before the install does; a separate failure row covers the install
// actually failing.
//
// Resolving no root, with no override configured, is not an install that
// succeeded either. No package manager ran, so whatever node_modules the
// tree already held is what the suite imports from — and a run saying
// nothing about it reads exactly like one that installed the workspace it
// was pointed at.
//
// Neither row is a failure: a component whose dependencies are in place by
// some other means passes, and these rows claim only what lydite did or did
// not do about them.
func installNote(root string, c component.Component, cfg config.Config) (ui.Row, bool) {
	dir := filepath.Join(root, filepath.FromSlash(c.Dir))
	if !installsNodeDeps(dir, c) {
		return ui.Row{}, false
	}
	if cfg.TypeScript.Install != "" {
		if wsRoot, ok := nodedeps.WorkspaceRoot(dir, root); ok {
			return ui.Row{
				Status: ui.StatusContext,
				Label:  installLabel(c.Name),
				Value:  "override at workspace root",
				Detail: []string{
					"typescript.install (" + cfg.TypeScript.Install + ") runs at " + wsRoot + ", coalesced with every sibling resolving the same root",
				},
			}, true
		}
		return ui.Row{
			Status: ui.StatusContext,
			Label:  installLabel(c.Name),
			Value:  "override in " + c.Dir,
			Detail: []string{
				"typescript.install (" + cfg.TypeScript.Install + ") runs in " + dir + ", shared with no other component",
			},
		}, true
	}
	if _, ok := nodedeps.WorkspaceRoot(dir, root); ok {
		return ui.Row{}, false
	}
	return ui.Row{
		Status: ui.StatusUnmeasured,
		Label:  installLabel(c.Name),
		Value:  "not installed",
		Detail: []string{
			"no single " + strings.Join(nodedeps.Managers(), "/") + " lockfile between " + c.Dir + " and the scan root, so no install ran",
			installHint,
		},
	}, true
}
