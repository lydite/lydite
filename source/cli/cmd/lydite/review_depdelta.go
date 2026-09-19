package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/depdelta"
	"lydite/lydite/internal/golang"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/ui"
)

// gateDependencies labels each manifest's verdict in the report.
const gateDependencies = "dependencies"

// manifestDelta is what the change did to one dependency manifest, or why that
// could not be said.
//
// The two are one type because they are one answer to one question, read by
// both the disqualifier and the version condition. A delta that was never
// built and a delta that moved nothing must never be the same value: the first
// is a manifest lydite could not read, and the second is a manifest it read.
type manifestDelta struct {
	// Path is the manifest, from the repository root.
	Path string
	// Delta is the comparison, valid only when Unmeasured is empty.
	Delta depdelta.Delta
	// Unmeasured is what stopped the comparison, empty when it stands.
	Unmeasured string
}

// measureDependencies compares the packages every dependency manifest the
// change touches pins at the merge-base against the ones it pins at HEAD.
//
// Which paths are manifests is sniffed from the diff, never from a component
// declaration: the question is "does this change touch a manifest, and of what
// kind", which the changed paths answer completely, and reading a component's
// declared lockfile instead would miss a manifest no component declared.
//
// Measuring is separate from reporting because the answer decides two things
// in two places — the disqualifier the rows carry, and whether a conditional
// exemption's versions all moved a little — and one manifest read twice is two
// answers to one question.
func measureDependencies(ctx context.Context, dir, base string, paths []string) []manifestDelta {
	var out []manifestDelta
	seen := map[string]bool{}
	for _, p := range paths {
		m := depdelta.Detect(p)
		// Both sides of a rename are changed paths, and a manifest renamed
		// onto itself would otherwise be compared and reported twice.
		if m == depdelta.ManifestNone || seen[p] {
			continue
		}
		seen[p] = true

		if !m.Readable() {
			out = append(out, manifestDelta{Path: p, Unmeasured: fmt.Sprintf("no dependency reader for %s", m.Ecosystem())})
			continue
		}
		baseSet, err := dependenciesAt(ctx, dir, base, p, m, shortSHA(base))
		if err != nil {
			out = append(out, manifestDelta{Path: p, Unmeasured: err.Error()})
			continue
		}
		headSet, err := dependenciesAt(ctx, dir, "HEAD", p, m, "HEAD")
		if err != nil {
			out = append(out, manifestDelta{Path: p, Unmeasured: err.Error()})
			continue
		}
		out = append(out, manifestDelta{Path: p, Delta: depdelta.Compare(baseSet, headSet)})
	}
	return out
}

// addDependencyRows folds each manifest's comparison into the report and into
// the decision.
//
// A package at HEAD that the merge-base did not pin refers, direct or
// transitive alike, and no version move on a name already there does. A
// removal never refers on its own: it is the opposite of new untrusted code
// arriving, and referring it would send every dependency cleanup to a person
// in exchange for nothing. See docs/adr/0047.
//
// A manifest whose set could not be built at either side refers too, under its
// own kind. An ecosystem with no reader and a lockfile that did not parse are
// both states in which lydite cannot say whether a dependency was added, and
// reporting that as "it added none" is the one direction the comparison exists
// to avoid.
func addDependencyRows(report *ui.Report, d *referral.Decision, deltas []manifestDelta, base string) {
	for _, m := range deltas {
		if m.Unmeasured != "" {
			refer(d, referral.Disqualification{
				Kind:     referral.DisqualificationDependencyDeltaUnmeasured,
				Path:     m.Path,
				Evidence: m.Path + ": " + m.Unmeasured,
			})
			continue
		}
		if len(m.Delta.Added) > 0 {
			refer(d, referral.Disqualification{
				Kind:     referral.DisqualificationDependencyAdded,
				Path:     m.Path,
				Evidence: fmt.Sprintf("%s pins %s", m.Path, strings.Join(capped(m.Delta.Added), ", ")),
			})
			continue
		}
		// A manifest that was compared and added nothing says so under its own
		// name: a manifest missing from the report is indistinguishable from
		// one nothing was measured over.
		report.Add(ui.Row{
			Status: ui.StatusPass,
			Label:  gateDependencies + "(" + m.Path + ")",
			Value:  "no package added against " + shortSHA(base),
		})
	}
}

// versionsPatchAndMinor reports whether every version pair in every manifest
// the change touches moved by a patch or a minor.
//
// A manifest that could not be measured fails it, for the reason it also
// disqualifies: lydite cannot say how far a version moved in a file it could
// not read, so it does not say it moved a little. A change touching no
// manifest at all has no pair to disqualify it and passes this half, leaving
// the licence and SCA evidence to decide.
func versionsPatchAndMinor(deltas []manifestDelta) bool {
	for _, m := range deltas {
		if m.Unmeasured != "" || !m.Delta.PatchOrMinorEligible() {
			return false
		}
	}
	return true
}

// scaGateForLang names the advisory check a declared component's language
// runs. A language absent from this map, TypeScript among them, runs no
// advisory check at all, so its dependency evidence is its licence row
// alone — requiring one would make the condition unsatisfiable for every
// repository that has a component in that language.
//
// Keyed by declaration rather than by a gate in the document, so a component
// whose language was switched off in .lydite/config.yml — and which therefore
// carries no row of any kind — is still known to need one: nothing in the
// document could have told this apart from a component that never existed.
var scaGateForLang = map[runner.Lang]string{
	runner.Go:   golang.GateGovulncheck,
	runner.Rust: rust.GateAudit,
}

// scaGateFor is the same pairing, keyed by a gate the document itself
// reports rather than by a declaration.
//
// review does not otherwise load .lydite/components.yml, and a fixture or a
// caller that never declares one still has to be held to the same rule: a
// component the document shows running `gosec` for is a Go component whether
// or not anything declared it, and one `govulncheck` row missing from beside
// it is exactly the gap the declared check exists to close for the case
// where a declaration does exist.
var scaGateFor = map[string]string{
	golang.GateGosec: golang.GateGovulncheck,
	rust.GateClippy:  rust.GateAudit,
}

// dependencyGatesPassed reports whether the scan documents in the given report
// directories show the licence gate and the advisory check passing for every
// declared component.
//
// Evidence about advisories is evidence about advisories and nothing else: the
// bump that introduces a copyleft dependency is precisely a lockfile-only
// change with a clean SCA run, which is why the licence gate has to have run
// and passed rather than merely not failed. A component whose licence row is
// missing, unmeasured, not configured or failing takes the whole condition
// with it — and so does a component the document carries no row for at all,
// which is what a language switched off in .lydite/config.yml, or a scan that
// did not run, looks like. Checking only the components a document happens to
// name would let either read as clean.
//
// No directory, no scan document in the ones given, or a directory that will
// not be read are all false. The flag can only ever supply evidence, so the
// absence of evidence is the absence of the exemption, which is the verdict a
// run without it already reaches.
func dependencyGatesPassed(dir string, dirs []string, warn io.Writer) bool {
	file, err := component.Load(dir)
	if err != nil {
		_, _ = fmt.Fprintf(warn, "warning: no licence or SCA evidence: %v\n", err)
		return false
	}

	// component -> gate -> status.
	byComponent := map[string]map[string]ui.Status{}
	read := false
	for _, d := range dirs {
		doc, err := readDocument(documentPath(d, "scan"))
		switch {
		case err == nil:
		// A directory holding no scan document is an ordinary shape — a job
		// that ran the suite and not the scan writes one of the others — and
		// says nothing a reader must act on. A document that is there and
		// will not parse is the opposite.
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			_, _ = fmt.Fprintf(warn, "warning: no licence or SCA evidence came from %s: %v\n", d, err)
			continue
		}
		read = true
		for _, row := range doc.Rows {
			gate, name, ok := splitGateLabel(row.Label)
			if !ok {
				continue
			}
			gates := byComponent[name]
			if gates == nil {
				gates = map[string]ui.Status{}
				byComponent[name] = gates
			}
			// Two documents naming one component keep the worse of the two
			// answers: a gate that passed in one job and failed in another
			// failed.
			if prev, seen := gates[gate]; !seen || prev == ui.StatusPass {
				gates[gate] = row.Status
			}
		}
	}
	if !read {
		return false
	}

	// The names to check are the union of what was declared and what the
	// document named: a declared component absent from the document entirely
	// — the disabled-language case — has to be checked against a nil gate
	// map rather than skipped, and a component the document names without
	// any declaration behind it (every fixture in this repository's own
	// tests, and any caller that runs review with no .lydite/components.yml
	// at all) still has to hold to the rule.
	declaredLang := map[string]runner.Lang{}
	names := map[string]bool{}
	for _, c := range file.Components {
		declaredLang[c.Name] = c.Lang()
		names[c.Name] = true
	}
	for name := range byComponent {
		names[name] = true
	}
	if len(names) == 0 {
		return false
	}
	for name := range names {
		gates := byComponent[name]
		if gates[licence.Gate] != ui.StatusPass {
			return false
		}
		if sca, ok := scaGateForLang[declaredLang[name]]; ok && gates[sca] != ui.StatusPass {
			return false
		}
		for gate := range gates {
			if sca, ok := scaGateFor[gate]; ok && gates[sca] != ui.StatusPass {
				return false
			}
		}
	}
	return true
}

// splitGateLabel takes a row label apart into the gate and the component it
// ran for — `licence(cli)` is the licence gate over the component named cli.
//
// A row that carries no component, like the scan's own summary, is not one
// this attributes: a gate with no component behind it answers for nothing in
// particular, and folding it under an empty name would invent a component the
// declaration never had.
func splitGateLabel(label string) (gate, component string, ok bool) {
	open := strings.LastIndex(label, "(")
	if open <= 0 || !strings.HasSuffix(label, ")") {
		return "", "", false
	}
	component = label[open+1 : len(label)-1]
	if component == "" {
		return "", "", false
	}
	return label[:open], component, true
}

// dependenciesAt is the set of packages a manifest pins at one revision.
//
// A revision that does not have the path pins nothing, which is what a
// manifest this change introduces means: every package in it is at HEAD and at
// no base, so every one of them is added. A revision that has it and cannot
// read it is the error, and refers.
//
// Both sides are read out of a commit rather than off disk, because the
// verdict has to be the one CI reaches: referral scores committed state, and a
// working tree with an uncommitted lockfile edit must not change the answer.
func dependenciesAt(ctx context.Context, dir, rev, repoPath string, m depdelta.Manifest, where string) (depdelta.Set, error) {
	content, present, err := showAtRevision(ctx, dir, rev, repoPath)
	if err != nil {
		return depdelta.Set{}, err
	}
	if !present {
		return depdelta.Set{}, nil
	}
	return depdelta.Extract(m, content, repoPath+" at "+where)
}
