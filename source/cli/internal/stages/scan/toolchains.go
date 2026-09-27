package scanstages

import (
	"context"
	"errors"
	"fmt"
	"io"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/orphan"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/scanlang"
	"lydite/lydite/internal/toolchain"
)

// Toolchains provisions each scan unit's language toolchain at the version
// its own directory declares. Provisioning is supplied by the caller — the
// CLI's adapter wraps internal/toolchain.Ensure with the diagnostics writer
// and the overrides .lydite/config.yml states — so this stage itself
// downloads and installs nothing, and a test can supply a fake in its place.
type Toolchains interface {
	Ensure(ctx context.Context, dir string, cfg config.Config, units []toolchain.Unit) (toolchain.Envs, error)
}

// Environment composes a component's declared environment onto its
// provisioned toolchain, and reports what a declaration alone contributed —
// the two questions plan-components and run-checks each need answered, to
// compose a check's environment and to warn about it without ever printing a
// value.
type Environment interface {
	// Compose is a component's checks environment: the toolchain's own
	// PATH and variables, with the component's declared ones layered on.
	Compose(tc *toolchain.Env, c component.Component) []string
	// Declared is a component's own declaration alone, split into the PATH
	// entries it names and every other variable.
	Declared(c component.Component) (dirs, vars []string)
}

// ProvisionToolchainsIn is the declaration and configuration provisioning
// reads its units from.
type ProvisionToolchainsIn struct {
	Dir        string
	File       component.File
	Config     config.Config
	Toolchains Toolchains
}

// ProvisionToolchainsOut is the environment every later stage's checks run
// under, keyed by component name.
type ProvisionToolchainsOut struct {
	Envs toolchain.Envs
}

// ProvisionToolchains makes each scanned component's language toolchain
// available at the version its own directory declares, before anything
// shells out to cargo, go or biome. lydite pins every tool it runs, and this
// is the toolchain it runs them with.
func ProvisionToolchains(ctx context.Context, in ProvisionToolchainsIn) (ProvisionToolchainsOut, error) {
	envs, err := in.Toolchains.Ensure(ctx, in.Dir, in.Config, scanUnits(in.File, in.Config))
	if err != nil {
		return ProvisionToolchainsOut{}, err
	}
	return ProvisionToolchainsOut{Envs: envs}, nil
}

// scanUnits is what each declared component needs a toolchain for: a
// component whose scanned language has no scanner, or whose language is
// switched off in .lydite/config.yml, needs nothing provisioned — provisioning
// one would download a compiler nothing is going to invoke. The language is
// the one the component is scanned as, so a command component stating
// `lang: go` is provisioned the Go toolchain its checks run under, and one
// stating no language needs nothing.
func scanUnits(file component.File, cfg config.Config) []toolchain.Unit {
	var out []toolchain.Unit
	for _, c := range file.Components {
		lang := c.ScanLang()
		if lang == "" || !scanlang.Enabled(lang, cfg) {
			continue
		}
		out = append(out, toolchain.Unit{Name: c.Name, Lang: lang, Dir: c.Dir})
	}
	return out
}

// anyLanguageDeclared reports whether some component states a language its
// source is scanned as, by its runner or its own lang:.
func anyLanguageDeclared(file component.File) bool {
	for _, c := range file.Components {
		if c.ScanLang() != "" {
			return true
		}
	}
	return false
}

// WarnUnscannedIn is the declaration WarnUnscanned checks against the scan
// root's own source, and the writer its warning reaches.
type WarnUnscannedIn struct {
	Dir    string
	File   component.File
	Config config.Config
	// Diagnostics is where the warning is written, at the moment it is
	// found — never returned for a caller to print later, which is what
	// keeps it interleaved with a check's own streamed output in the order
	// the two arise.
	Diagnostics io.Writer
}

// WarnUnscannedOut carries the gaps this stage found, for a test to read —
// nothing in production reads them: the warning already written to
// Diagnostics is this stage's only effect.
type WarnUnscannedOut struct {
	Gaps []orphan.Gap
}

// WarnUnscanned names, on Diagnostics, source no declared component's checks
// reach — the hole the orphan gate cannot close, because that gate asks
// whether a component *contains* a file rather than whether a scanner runs
// over its language.
//
// A warning and not a row, and never an error: what a repository should do
// about it is declare a component or write the exclude, which is `lydite
// test`'s gate to demand. A git failure is said out loud too, with one
// exception — a repository whose every component declares a raw command has
// no source in a language lydite knows by construction, so git listing no
// source at all is its ordinary state rather than something worth a warning
// on every run.
func WarnUnscanned(ctx context.Context, in WarnUnscannedIn) (WarnUnscannedOut, error) {
	gaps, err := orphan.Unscanned(ctx, in.Dir, in.File, func(l runner.Lang) bool { return scanlang.Enabled(l, in.Config) })
	if err != nil {
		if errors.Is(err, orphan.ErrNoRepository) || (errors.Is(err, orphan.ErrNoFiles) && !anyLanguageDeclared(in.File)) {
			return WarnUnscannedOut{}, nil
		}
		_, _ = fmt.Fprintf(in.Diagnostics, "warning: could not check what no component scans (%v)\n", err)
		return WarnUnscannedOut{}, nil
	}
	for _, g := range gaps {
		// One example and a count, not the list: the reader needs to know
		// which declaration is missing, and a repository mid-migration would
		// otherwise print hundreds of paths ahead of its own report.
		_, _ = fmt.Fprintf(in.Diagnostics, "warning: %d %s file(s) are under no component that checks them, so nothing scans them (e.g. %s) — declare a component for them, or exclude them in %s\n",
			len(g.Files), g.Lang, g.Files[0], component.FileName)
	}
	return WarnUnscannedOut{Gaps: gaps}, nil
}
