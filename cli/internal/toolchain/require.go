package toolchain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"lydite/lydite/internal/runner"
)

// Requirement is the language toolchain one component needs, read from what
// the repository already declares.
//
// Version is deliberately not sourced from .lydite/config.yml. Every one of these
// files is already the authoritative, tool-enforced statement of the version
// — `go build` honours go.mod, rustup honours rust-toolchain.toml, npm
// honours engines.node — so a second copy in lydite's config could only ever
// agree redundantly or disagree silently. A stale duplicate is worse than no
// duplicate: it reads as authoritative. .lydite/config.yml can override (see
// config.Toolchain), which is a different thing — an explicit, local
// exception rather than a parallel source of truth.
type Requirement struct {
	// Unit is the component this was resolved for.
	Unit Unit
	// Lang is the language the toolchain is for, derived from the unit's
	// runner. It is carried here as well because every decision below is
	// taken per language, and reaching back through Unit for it reads as
	// though the two could differ.
	Lang runner.Lang
	// Version is canonical() output ("v1.26.4"), or "" when the repo pins no
	// comparable version — a `stable` rust channel, an `lts/*` .nvmrc, or no
	// manifest statement at all.
	Version string
	// Raw is what the manifest literally said, for messages. Reporting
	// "1.26.4" rather than "v1.26.4" keeps lydite's output greppable against
	// the file the reader will go look at.
	Raw string
	// Source names the file and field the version came from, so a surprising
	// requirement can be traced without guessing which of several manifests
	// won.
	Source string
	// Overridden records that .lydite/config.yml supplied the version rather than a
	// manifest. It matters for Rust: rustup selects a toolchain by reading
	// rust-toolchain.toml from the directory cargo runs in, so a version that
	// exists only in lydite's config is one rustup cannot see and has to be
	// told about explicitly.
	Overridden bool
}

// Unpinned reports whether the repo stated no comparable version, in which
// case any working toolchain of this kind satisfies it and lydite provisions
// only if none is present at all.
func (r Requirement) Unpinned() bool { return r.Version == "" }

// Unit is one component as toolchain resolution sees it: a language, rooted
// at a directory. It is what the declaration already states — a component
// names a runner, the runner implies the language, and `dir` says where that
// language's code is — so nothing here infers anything from the tree.
type Unit struct {
	// Name is the component's name, carried so a diagnostic can say which
	// component asked for a toolchain.
	Name string
	// Lang is derived from the component's runner, never declared.
	Lang runner.Lang
	// Dir is the component's directory, relative to the scan root.
	Dir string
}

// Requirements resolves what each unit needs, one Requirement per unit in the
// same order. A unit naming a language lydite provisions no toolchain for is
// skipped rather than given an unpinned requirement, so nothing probes the
// machine on its behalf.
//
// Resolution is per unit and not per repository, which is what makes a
// monorepo answerable: a workspace declaring `engines.node: >=22` and a tools
// package pinning 18 are two components with two answers, where a single pass
// over every package.json has to pick one of them by a rule neither package
// stated.
//
// A manifest that can't be read or parsed yields an unpinned requirement
// rather than an error: lydite's job here is to make the toolchain more
// likely to be right, and refusing to scan a repo because its .nvmrc is
// malformed would be a worse outcome than scanning it with whatever is on
// PATH.
func Requirements(root string, units []Unit, cfg Overrides) ([]Requirement, error) {
	var out []Requirement
	for _, u := range units {
		dir := filepath.Join(root, filepath.FromSlash(u.Dir))
		var req Requirement
		var err error
		switch u.Lang {
		case runner.Go:
			req, err = goRequirement(root, dir)
		case runner.Rust:
			req, err = rustRequirement(root, dir)
		case runner.TypeScript:
			req, err = nodeRequirement(root, dir)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		if override := cfg.For(u.Lang); override != "" {
			v := canonical(override)
			// A version lydite cannot parse is a hard config error, not a
			// silent downgrade. Assigning it unconditionally would set
			// Version to "" and discard what the manifest correctly said —
			// turning a typo like "1.26.x" into "any toolchain will do", and
			// then reporting "no version declared" about a repo whose go.mod
			// declares one. That reads as lydite being broken rather than as
			// the config being wrong.
			//
			// Rust is exempt: rustup channels are legitimately non-numeric
			// ("stable", "nightly", "1.96-x86_64-unknown-linux-gnu"), and
			// rustup is the authority on which of those are real, not
			// lydite. Such an override stays unpinned and is handed through
			// verbatim.
			if v == "" && u.Lang != runner.Rust {
				return nil, fmt.Errorf(
					"toolchain.%s in .lydite/config.yml: %q is not a version lydite can compare against an installed toolchain",
					overrideKey(u.Lang), override)
			}
			req.Version = v
			req.Raw = override
			req.Source = "toolchain." + overrideKey(u.Lang) + " in .lydite/config.yml"
			req.Overridden = true
		}
		req.Unit = u
		out = append(out, req)
	}
	return out, nil
}

// overrideKey names a language as it is spelled under `toolchain:` in
// .lydite/config.yml. TypeScript's key is `node`, because what is being overridden
// is the Node runtime, not the TypeScript compiler — the language's name and
// its toolchain's name are the one place these diverge.
func overrideKey(e runner.Lang) string {
	if e == runner.TypeScript {
		return "node"
	}
	return string(e)
}

// goRequirement reads the `go` and `toolchain` directives from the module at
// the component's own directory, and returns the higher of the two.
//
// Both directives matter and they mean different things: `go` is the minimum
// language version the module's source requires, `toolchain` names a specific
// toolchain to run. Taking the max of the two gives the one toolchain that can
// build the module.
//
// The directory is the component's, which for a `go-test` component is its
// module root — `go list -m` answers with the enclosing module, so a component
// declared anywhere else is already reported as unmeasurable by
// internal/coverage. A directory with no go.mod yields an unpinned
// requirement: the repository named no floor, so anything present will do.
func goRequirement(root, dir string) (Requirement, error) {
	req := Requirement{Lang: runner.Go}
	path := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(path) // #nosec G304 -- path is a declared component directory, not user input
	if err != nil {
		return req, nil
	}
	// modfile is golang.org/x/mod's own parser, already a direct dependency
	// (cmd/lydite/update.go uses x/mod/semver). Hand-rolling a line sniff
	// would be reimplementing a parser that ships in the module graph and
	// that Go itself uses.
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return req, nil
	}
	for _, raw := range []string{goDirective(f), toolchainDirective(f)} {
		if v := canonical(raw); maxVersion(req.Version, v) != req.Version {
			req.Version, req.Raw = v, strings.TrimPrefix(raw, "go")
			req.Source = relSource(root, dir, "go.mod")
		}
	}
	return req, nil
}

// relSource names a manifest the way a reader will go looking for it: its
// path relative to the scan root, so a diagnostic points at a file rather
// than at a machine-specific absolute path.
func relSource(root, dir, name string) string {
	rel, err := filepath.Rel(root, filepath.Join(dir, name))
	if err != nil {
		return name
	}
	return filepath.ToSlash(rel)
}

func goDirective(f *modfile.File) string {
	if f.Go == nil {
		return ""
	}
	return f.Go.Version
}

func toolchainDirective(f *modfile.File) string {
	if f.Toolchain == nil {
		return ""
	}
	return f.Toolchain.Name
}

// rustRequirement reads the channel from the component directory's
// rust-toolchain.toml, or the legacy bare rust-toolchain file beside it.
//
// This extends, rather than contradicts, internal/rust's stance that the
// toolchain version is the target repo's responsibility via its own
// rust-toolchain.toml. It still is — lydite reads that file rather than
// overriding it, and only makes sure the channel it names is installed
// instead of leaving rustup to fetch it lazily in the middle of a check.
//
// Reading it at the component's directory is what makes lydite's answer and
// rustup's the same answer: rustup selects a toolchain from the directory
// cargo runs in, and that is this directory.
func rustRequirement(root, dir string) (Requirement, error) {
	req := Requirement{Lang: runner.Rust}
	raw, source := rustChannel(root, dir)
	if raw == "" {
		return req, nil
	}
	// A named channel ("stable", "nightly") canonicalises to "" — record it
	// as the raw requirement so messages can name it, but leave Version
	// empty so it compares as unpinned.
	req.Raw, req.Source = raw, source
	req.Version = canonical(raw)
	return req, nil
}

// rustChannel returns the channel string declared for one crate directory.
// rust-toolchain.toml wins over the legacy bare rust-toolchain file, matching
// rustup's own precedence.
func rustChannel(root, dir string) (channel, source string) {
	if data, err := os.ReadFile(filepath.Join(dir, "rust-toolchain.toml")); err == nil { // #nosec G304 -- dir is a declared component directory, not user input
		if c := tomlChannel(string(data)); c != "" {
			return c, relSource(root, dir, "rust-toolchain.toml")
		}
	}
	// The legacy form is the whole file: a bare channel string, no TOML.
	if data, err := os.ReadFile(filepath.Join(dir, "rust-toolchain")); err == nil { // #nosec G304 -- dir is a declared component directory, not user input
		if c := strings.TrimSpace(string(data)); c != "" && !strings.Contains(c, "\n") {
			return c, relSource(root, dir, "rust-toolchain")
		}
	}
	return "", ""
}

// tomlChannel pulls `channel = "1.96"` out of a rust-toolchain.toml.
//
// A line-level sniff, not a TOML parse, and deliberately so. The file's
// entire schema is a single [toolchain] table, `channel` is the only key
// lydite reads, and adding a TOML dependency to read one string would be the
// larger cost. A file too exotic for this yields no channel, which degrades
// to unpinned rather than to a wrong answer.
func tomlChannel(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if before, _, found := strings.Cut(line, "#"); found {
			line = strings.TrimSpace(before)
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "channel" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// packageJSONEngines is the subset of package.json this package reads.
type packageJSONEngines struct {
	Engines struct {
		Node string `json:"node"`
	} `json:"engines"`
}

// nodeRequirement reads the Node version a component needs from its own
// `engines.node` and the .nvmrc beside it, falling back to the scan root's
// .nvmrc when the component states neither.
//
// engines.node is a range rather than a version, so only its lower bound is
// meaningful here — see minimumOfRange for why a floor is all this needs. The
// higher of the two the component states wins, because they are different
// statements rather than a precedence order: `">=18"` is the floor the package
// supports and a .nvmrc of `22` is the version it is developed on, and taking
// the first of them found would run the suite on 18 in a repository that pins
// 22. The scan root's .nvmrc is a last resort because a monorepo
// conventionally keeps one at the top; a component that states its own version
// is never overruled by it.
func nodeRequirement(root, dir string) (Requirement, error) {
	req := Requirement{Lang: runner.TypeScript}
	consider := func(raw, source string) {
		if raw == "" {
			return
		}
		v := minimumOfRange(raw)
		// The first statement found is recorded even when it carries no
		// comparable version, so a message can name what the component
		// actually wrote; a later one that does compare higher replaces it.
		if req.Raw == "" {
			req.Raw, req.Source, req.Version = raw, source, v
			return
		}
		if maxVersion(req.Version, v) != req.Version {
			req.Raw, req.Source, req.Version = raw, source, v
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil { // #nosec G304 -- dir is a declared component directory, not user input
		var pkg packageJSONEngines
		if json.Unmarshal(data, &pkg) == nil {
			consider(pkg.Engines.Node, relSource(root, dir, "package.json")+" (engines.node)")
		}
	}
	consider(nvmrc(dir), relSource(root, dir, ".nvmrc"))
	// On a comparable version, not merely on having said something: an
	// `engines.node` of "*" states no floor, so it cannot be the answer when
	// the scan root's .nvmrc names one.
	if req.Version != "" {
		return req, nil
	}
	consider(nvmrc(root), ".nvmrc")
	return req, nil
}

// nvmrc reads a .nvmrc, whose entire content is the version or alias.
func nvmrc(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".nvmrc")) // #nosec G304 -- dir is a declared component directory, not user input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
