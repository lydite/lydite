package recordstages

import (
	"context"
	"path/filepath"

	"lydite/lydite/internal/component"
	"lydite/lydite/internal/config"
	"lydite/lydite/internal/finding"
	"lydite/lydite/internal/licence"
	"lydite/lydite/internal/runner"
	"lydite/lydite/internal/rust"
	"lydite/lydite/internal/secrets"
	"lydite/lydite/internal/semgrep"
)

// LangEnabled reports whether a configuration leaves one language's checks
// switched on. It is `lydite scan`'s own answer, handed in so that which
// languages a scan checks is decided in one place.
type LangEnabled func(lang runner.Lang, cfg config.Config) bool

// ScannerGates is the gates a language's checks report their findings under,
// as `lydite scan` names them, handed in for the same reason LangEnabled is.
type ScannerGates func(lang runner.Lang) []string

// CountFindingsIn is the scan's claims, and the tree's statements about which
// gates applied to them.
type CountFindingsIn struct {
	// Dir is the root directory of the tree being recorded.
	Dir string
	// Declaration and Config are LoadDeclarationOut's.
	Declaration component.File
	Config      config.Config
	// Found is ReadReportsOut.Found.
	Found []finding.Finding
	// Scanned is ReadReportsOut.Scanned.
	Scanned      bool
	LangEnabled  LangEnabled
	ScannerGates ScannerGates
}

// CountFindingsOut is how many claims each scanner gate made.
type CountFindingsOut struct {
	// PerComponent is each declared component's count per gate that applies
	// to it. Nil when no scan document was read.
	PerComponent map[string]map[string]int
	// Root is each root-scoped gate's count over the repository. Nil when no
	// scan document was read, and when no root-scoped gate applied and none
	// made a claim.
	Root map[string]int
}

// CountFindings is FindingCounts as a stage: the one scalar in a recording
// that comes from `lydite scan` rather than from `lydite test`.
func CountFindings(_ context.Context, in CountFindingsIn) (CountFindingsOut, error) {
	perComponent, root := FindingCounts(in.Dir, in.Declaration, in.Config, in.Found, in.Scanned, in.LangEnabled, in.ScannerGates)
	return CountFindingsOut{PerComponent: perComponent, Root: root}, nil
}

// FindingCounts is how many claims each scanner gate made: per component, and
// over the repository for the gates that are root-scoped.
//
// The counts come from the scan documents and the GATE SET comes from the tree,
// and that split is the whole of what makes the numbers readable. A clean gate
// reports no findings at all, so a count on its own cannot tell a Go component
// gosec found nothing in from one gosec never looked at — while the
// declaration and the configuration together say exactly which gates each
// component's language implies. A gate that applies records 0; one that does
// not is not a key, which is ADR 0029's "absent is not zero" for a quantity a
// single integer cannot express.
//
// A language's gate set is not the whole answer for the licence gate, which
// runs only where a policy governs the component: the repository's own
// licence.policy.allow, or — for Rust — the component's deny.toml. Its nought
// is seeded under the same condition scan gates on, so a repository that never
// stated a policy records no licence key at all rather than the trend line of
// one whose policy ran clean on every commit.
//
// Nothing at all is returned when no scan document was read. Every applicable
// gate would otherwise record nought, which says the gate ran and found
// nothing — the one thing a recording must not invent about a scan that never
// happened.
//
// Two limits, both stated rather than worked around. A scanner that crashed —
// a component that would not build — found no claims, so it records 0 like a
// clean one; the red scan is in the document's rows and its verdict, and the
// ledger holds scalars rather than verdicts. And two components over one
// directory and environment are deliberately scanned once, under the first
// one's name, so the other records 0 for gates that ran for its twin. Reading
// the gate set from the scan's own rows would answer both and cost the thing
// this channel exists for: the rows are prose, and `gosec(cli)` parsed back
// into a gate and a component is the text-scraping findings-as-data removed.
func FindingCounts(dir string, decl component.File, cfg config.Config, found []finding.Finding, scanned bool,
	langEnabled LangEnabled, scannerGates ScannerGates) (map[string]map[string]int, map[string]int) {
	if !scanned {
		return nil, nil
	}
	policy := licence.NewPolicy(cfg.Licence.Policy.Allow)
	perComponent := map[string]map[string]int{}
	for _, c := range decl.Components {
		lang := c.ScanLang()
		// A component stating no language it is scanned as, and a language
		// switched off in .lydite/config.yml, are ones whose checks never
		// run. Neither has a gate that applies, so neither records a
		// zero that would read as a clean scan.
		if lang == "" || !langEnabled(lang, cfg) {
			continue
		}
		gates := scannerGates(lang)
		gated := licenceGated(filepath.Join(dir, c.Dir), lang, policy)
		counts := make(map[string]int, len(gates))
		for _, gate := range gates {
			if gate == licence.Gate && !gated {
				continue
			}
			counts[gate] = 0
		}
		if len(counts) == 0 {
			continue
		}
		perComponent[c.Name] = counts
	}
	// Each root-scoped gate seeds its own nought, independently of the others:
	// a gate switched off records no key, and a gate that is on records 0 even
	// when its neighbour is off. One seed shared between them would let a clean
	// gitleaks run read as a gate nobody asked for.
	var root map[string]int
	seed := func(gate string) {
		if root == nil {
			root = map[string]int{}
		}
		root[gate] = 0
	}
	if cfg.Semgrep.Enabled {
		seed(semgrep.Gate)
	}
	if cfg.Secrets.Enabled {
		seed(secrets.Gate)
	}
	for _, f := range found {
		// A root-scoped claim names no component, and nothing here invents
		// one: whichever component happens to contain the path is an
		// ownership question the declaration does not answer.
		if f.Component == "" {
			if root == nil {
				root = map[string]int{}
			}
			root[f.Gate]++
			continue
		}
		// Bounded by the declaration, exactly as a baseline entry is: a claim
		// naming a component the tree no longer declares is one nothing can
		// ever measure again. The gate key is created rather than required,
		// because a claim is itself proof that its gate ran.
		if counts, ok := perComponent[f.Component]; ok {
			counts[f.Gate]++
		}
	}
	return perComponent, root
}

// licenceGated reports whether the licence gate runs over the component in
// cdir, which is what decides whether its nought is seeded.
//
// The same condition each language's scan gates on, asked from the tree rather
// than from the scan's rows: Go and TypeScript run the gate only under the
// repository's own policy, and Rust runs it under that policy or under the
// component's own deny.toml, which is rust.PolicyFor's answer. Every other
// language declares no licence gate, so none applies.
func licenceGated(cdir string, lang runner.Lang, policy licence.Policy) bool {
	switch lang {
	case runner.Go, runner.TypeScript:
		return policy.Configured()
	case runner.Rust:
		return rust.PolicyFor(cdir, policy) != rust.PolicyFromNone
	default:
		return false
	}
}
