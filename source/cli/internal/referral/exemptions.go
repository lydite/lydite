// Package referral decides whether a change may merge without a human
// reading it.
//
// The model is an allowlist, and the default is to refer: a change merges
// unattended only when it matches a declared exemption, and anything that
// matches nothing is handed to a person. A denylist would only catch the
// failure modes somebody enumerated, which is a fair bet when a human reads
// the diff and a bad one once that human is what the arrangement removes.
// See docs/adr/0013-referral-not-approval.md.
//
// Two rules give the model its teeth, and both are easy to weaken by
// accident:
//
//   - Matching is all-or-nothing, and against one exemption. Every changed
//     path must be covered by a single exemption. An exemption that matched
//     when *some* path matched would let an agent staple a README tweak onto
//     a dangerous change and take the unattended path.
//   - A disqualifier vetoes any match. Disqualifiers are evidence that
//     something tried to make a verdict go away — a suppression annotation, a
//     newly skipped test — and must never be clearable by the thing that
//     produced them.
//
// Everything this package matches on is evidence read off the diff. Nothing
// an author can simply assert about their own change may earn an exemption or
// clear a disqualifier: a claim is worth exactly as much as the honesty of
// whoever made it, and this whole design exists for the case where that
// cannot be assumed. See docs/adr/0014-evidence-only-referral-matching.md.
package referral

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/config"
	"lydite/lydite/internal/pathmatch"
)

// FileName is the exemptions file, read from the scan root.
//
// It is its own file inside config.Dir rather than a section of
// config.FileName because a pull request touching it may touch nothing else,
// and that isolation must not extend to ordinary config like coverage report
// paths, which legitimately change alongside code.
const FileName = config.Dir + "/exemptions.yml"

// IsExemptionsPath reports whether a repository-root-relative path is an
// exemptions file.
//
// Matching the whole path under config.Dir, not just its base name: a bare
// base-name test would read any exemptions.yml anywhere in the tree as the
// file that decides what merges unattended. It is not anchored to the
// repository root either, because the referral diff covers the whole
// repository while lydite may be scanning a subdirectory of it — a monorepo's
// declaration lives at source/.lydite/exemptions.yml and is still the one
// that governs its own scan root.
func IsExemptionsPath(p string) bool {
	return p == FileName || strings.HasSuffix(p, "/"+FileName)
}

// InConfigDir reports whether a repository-root-relative path is a file
// lydite is configured by: anything directly inside a config.Dir directory.
//
// The whole directory, so a component declaration or an exemption set counts
// as lydite configuration alongside config.FileName. A file lydite reads to
// decide what it checks is exactly the thing a change must not quietly alter.
func InConfigDir(p string) bool { return path.Base(path.Dir(p)) == config.Dir }

// Exemption is one declared shape of change that merges unattended.
//
// It is a shape, not a set of blessed paths, and the difference shows when a
// change is covered by two exemptions but by neither alone: that is referred.
// Under the other reading the file becomes one global path allowlist, and
// adding a narrow entry silently widens every existing entry by union — at
// which point `git log` on the exemptions file stops being readable one entry
// at a time, which is the audit artefact the allowlist model exists to
// produce.
type Exemption struct {
	// Name identifies the exemption in lydite's output, so a pass says which
	// declaration let the change through.
	Name string `yaml:"name"`
	// Reason is why this shape of change is boring. It is required, because
	// this file is the entire risk model and each entry is a claim that has
	// to survive review; a diff of bare globs is not reviewable.
	Reason string `yaml:"reason"`
	// Paths are the patterns that must, between them, cover every changed
	// path. They are repository-root-relative even when lydite runs with
	// --dir pointing at a subdirectory — see Match for the syntax and why.
	Paths []string `yaml:"paths"`
}

// Covers reports whether every changed path falls under one of e's patterns.
func (e Exemption) Covers(changed []string) bool {
	for _, c := range changed {
		matched := false
		for _, p := range e.Paths {
			if pathmatch.Match(p, c) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Disqualifiers is the repo's additions to the built-in vetoes.
//
// It can only add. The built-in set is not expressible as absent, because a
// veto list that can be emptied is not a floor — and the argument for
// keeping disqualifiers in their own list rather than inside each exemption
// (every future exemption would otherwise have to remember every
// disqualifier, and forgetting one is silent) applies just as well one level
// up, to the file itself.
type Disqualifiers struct {
	// Paths veto a match when any changed path falls under one of them.
	Paths []string `yaml:"paths"`
}

// File is the parsed exemptions file. Its zero value is the correct
// day-one state: no exemptions, so everything is referred.
type File struct {
	Exemptions    []Exemption   `yaml:"exemptions"`
	Disqualifiers Disqualifiers `yaml:"disqualifiers"`
}

// Parse reads the exemptions file, rejecting anything it does not fully
// understand.
//
// Unknown keys are an error rather than being ignored, which is the same
// stance config.validateLinter takes toward a retired `linter: eslint`. If a
// future lydite grows a condition field and an older binary drops it in
// silence, the exemption widens to whatever it says without the field —
// nobody edited the file, nobody reviewed a change, and the gate quietly
// covers less than its author wrote down.
func Parse(data []byte, source string) (File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	// An empty file decodes to io.EOF, which is the correct day-one
	// state — no exemptions, everything referred — not a malformed file.
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return File{}, fmt.Errorf("parsing %s: %w", source, err)
	}
	if err := f.validate(source); err != nil {
		return File{}, err
	}
	return f, nil
}

func (f File) validate(source string) error {
	seen := map[string]bool{}
	for i, e := range f.Exemptions {
		where := fmt.Sprintf("%s: exemptions[%d]", source, i)
		if e.Name == "" {
			return fmt.Errorf("%s: name is required", where)
		}
		if seen[e.Name] {
			return fmt.Errorf("%s: duplicate name %q", where, e.Name)
		}
		seen[e.Name] = true
		if e.Reason == "" {
			return fmt.Errorf("%s (%s): reason is required — it is what makes this entry reviewable", where, e.Name)
		}
		// An exemption with no patterns covers only a change that touches
		// nothing, so it can never fire. Accepting it would leave a dead
		// entry in the one file whose contents are supposed to be the
		// complete statement of what merges unread.
		if len(e.Paths) == 0 {
			return fmt.Errorf("%s (%s): at least one path pattern is required", where, e.Name)
		}
		for _, p := range e.Paths {
			if err := pathmatch.ValidatePattern(p); err != nil {
				return fmt.Errorf("%s (%s): %w", where, e.Name, err)
			}
		}
	}
	for _, p := range f.Disqualifiers.Paths {
		if err := pathmatch.ValidatePattern(p); err != nil {
			return fmt.Errorf("%s: disqualifiers: %w", source, err)
		}
	}
	return nil
}
