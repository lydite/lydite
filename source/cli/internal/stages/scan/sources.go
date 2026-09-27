// Package scanstages holds the stages `lydite scan` is built from: reading
// the configuration and the component declaration, resolving the diff base a
// finding is anchored against and the lines the change touched, provisioning
// each scanned component's language toolchain, warning about source no
// declared component covers, planning and running each component's language
// checks and licence gate, and running the two root-scoped scanners, Semgrep
// and gitleaks.
//
// Every stage is a plain function of its own In. Nothing here reads the
// process environment: whatever a stage needs from it, such as whether a
// Semgrep token is set, arrives as a field its caller supplies.
package scanstages

import (
	"context"
	"fmt"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
)

// LoadConfigIn names the scan root LoadConfig reads the configuration from.
type LoadConfigIn struct {
	Dir string
}

// LoadConfigOut is the configuration in force, and the switches and values a
// flow's conditions and bindings read off it.
//
// SemgrepEnabled and SecretsEnabled restate two fields of Config as top-level
// bools, and SemgrepConfig restates one as a top-level string, because a flow
// condition or binding reaches only a top-level field of an Out and cannot
// reach into Config for them.
type LoadConfigOut struct {
	Config         config.Config
	SemgrepEnabled bool
	SecretsEnabled bool
	SemgrepConfig  string
}

// LoadConfig reads .lydite/config.yml from the scan root, merged onto the
// defaults. A missing file is the defaults, not an error; a file that does not
// parse or validate is this stage's error.
func LoadConfig(_ context.Context, in LoadConfigIn) (LoadConfigOut, error) {
	cfg, err := config.Load(in.Dir)
	if err != nil {
		return LoadConfigOut{}, err
	}
	return LoadConfigOut{
		Config:         cfg,
		SemgrepEnabled: cfg.Semgrep.Enabled,
		SecretsEnabled: cfg.Secrets.Enabled,
		SemgrepConfig:  cfg.Semgrep.Config,
	}, nil
}

// LoadComponentsIn names the scan root LoadComponents reads the declaration
// from.
type LoadComponentsIn struct {
	Dir string
}

// LoadComponentsOut is the declaration, holding at least one component.
type LoadComponentsOut struct {
	File component.File
}

// LoadComponents reads the component declaration from the scan root, and
// refuses one that declares nothing.
//
// An error rather than a row, because there is nothing to report on: scan runs
// the checks each component's language implies, so a repository that declares
// none would be scanned by nothing at all while the job stayed green — a
// security scan that silently stopped. Declaring a component is work the author
// can do, and naming the file is what makes it one step.
func LoadComponents(_ context.Context, in LoadComponentsIn) (LoadComponentsOut, error) {
	file, err := component.Load(in.Dir)
	if err != nil {
		return LoadComponentsOut{}, err
	}
	if len(file.Components) == 0 {
		return LoadComponentsOut{}, fmt.Errorf("no components declared in %s: scan runs the checks each component's language implies, so declare what this repository builds",
			filepath.Join(in.Dir, filepath.FromSlash(component.FileName)))
	}
	return LoadComponentsOut{File: file}, nil
}
