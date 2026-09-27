package scanstages

import (
	"context"
	"path/filepath"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scanlang"
	"lydite/lydite/internal/toolchain"
)

// Disposition is what a scan does with one declared component.
type Disposition string

const (
	// Unscanned is a component whose language lydite has no scanner for,
	// including a raw command that states no language at all. Nothing runs
	// over it, and the caller says so rather than dropping it: a component
	// nothing scans, dropped in silence, reads exactly like one that was
	// scanned and found clean.
	Unscanned Disposition = "unscanned"
	// Disabled is a component whose language has a scanner that
	// .lydite/config.yml leaves switched off. Nothing runs over it. Whether
	// that is worth a row is the caller's to decide from the language: Go,
	// Rust and TypeScript are on unless a repository turns them off, so one
	// of them being off is an opt-out the repository stated, while shell is
	// off until a repository turns it on, so nobody opted out.
	Disabled Disposition = "disabled"
	// Duplicate is a component whose checks would look at exactly what an
	// earlier component's already do — the same language, directory and
	// environment. Its checks are not run a second time.
	Duplicate Disposition = "duplicate"
	// Scan is a component whose language checks run.
	Scan Disposition = "scan"
)

// Planned is one declared component and what the scan does with it.
type Planned struct {
	Component component.Component
	// Lang is the language the component is scanned as: stated by its
	// runner or its own lang:, and empty for a raw command stating neither.
	Lang        runner.Lang
	Disposition Disposition

	// DuplicateOf names the earlier component whose checks already cover
	// this one. Set only for Duplicate.
	DuplicateOf string

	// Dir is the component's directory joined onto the scan root, where its
	// checks run. Set only for Scan.
	Dir string
	// Env is the environment the component's checks run and install under.
	// Set only for Scan.
	Env executil.Env
	// ToolchainKey identifies the component's resolved toolchain, keying the
	// scanners a check installs under it. Set only for Scan.
	ToolchainKey string
}

// PlanComponentsIn is the declaration, the configuration switching each
// language on or off, and the toolchains provisioning resolved for it.
type PlanComponentsIn struct {
	// Dir is the scan root each component's directory is joined onto.
	Dir         string
	File        component.File
	Config      config.Config
	Envs        toolchain.Envs
	Environment Environment
}

// PlanComponentsOut is one entry per declared component, in declaration order.
type PlanComponentsOut struct {
	Plan []Planned
}

// PlanComponents decides, for each declared component in declaration order,
// whether its checks run and under what environment. It runs nothing and
// writes nothing.
//
// Whether lydite has a scanner for the language is asked before whether the
// language is switched on. scanlang.Enabled answers false for every language
// it has no key for, so asking it first would send a language with no scanner
// down the opt-out branch, and report "nothing scans this" as "the repository
// switched this off".
//
// A component's checks are keyed by what they actually look at: the language
// they run for, the directory they run in and the environment they run under.
// component.validate enforces unique names and not unique directories, so two
// components over one root are legitimate — `lydite test` runs both suites and
// its scheduler serialises them. Their scanners would read the identical tree
// twice and report every finding twice, under two labels. Two declaring the
// same environment are one scan, carried by the first in declaration order:
// either name is honest, and declaration order does not vary between runs. Two
// declaring different environments are two builds: dropping one would scan the
// other's tree with an environment it never asked for, and a component
// declaring the CGO_ENABLED or SQLX_OFFLINE its language needs would fail on a
// build its declaration exists to make work — under the other component's
// name.
func PlanComponents(_ context.Context, in PlanComponentsIn) (PlanComponentsOut, error) {
	plan := make([]Planned, 0, len(in.File.Components))
	scanned := map[string]string{}
	for _, c := range in.File.Components {
		lang := c.ScanLang()
		entry := Planned{Component: c, Lang: lang}
		if !scanlang.Scanned(lang) {
			entry.Disposition = Unscanned
			plan = append(plan, entry)
			continue
		}
		if !scanlang.Enabled(lang, in.Config) {
			entry.Disposition = Disabled
			plan = append(plan, entry)
			continue
		}
		dir := filepath.Join(in.Dir, filepath.FromSlash(c.Dir))
		// The component's declared environment as well as its toolchain,
		// composed exactly as `lydite test` composes it: a Rust component
		// declaring SQLX_OFFLINE or a Go one declaring CGO_ENABLED needs it to
		// build at all. Install carries none of it — see executil.Env.
		tc := in.Envs.For(c.Name)
		env := executil.Env{
			Check:   in.Environment.Compose(tc, c),
			Install: tc.Environ(),
		}
		key := string(lang) + "\x00" + filepath.Clean(dir) + "\x00" + strings.Join(env.Check, "\x00")
		if by, done := scanned[key]; done {
			entry.Disposition = Duplicate
			entry.DuplicateOf = by
			plan = append(plan, entry)
			continue
		}
		scanned[key] = c.Name
		entry.Disposition = Scan
		entry.Dir = dir
		entry.Env = env
		entry.ToolchainKey = tc.Key()
		plan = append(plan, entry)
	}
	return PlanComponentsOut{Plan: plan}, nil
}
