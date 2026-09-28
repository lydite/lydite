package reviewdecision

import "lydite/lydite/internal/referral"

// Status is one outcome's own answer, and the only thing a verdict is folded
// from. It is this package's type rather than a row status because deciding
// which concern fails, which refers and which passes is the decision, and
// choosing how a row renders it is the caller's.
type Status string

const (
	// StatusPass is a concern that was decided and found nothing.
	StatusPass Status = "pass"
	// StatusRefer is a concern only a person can clear.
	StatusRefer Status = "refer"
	// StatusFail is a concern the author clears by doing more work.
	StatusFail Status = "fail"
	// StatusNonVoting is a concern reported beside the verdict and never
	// folded into it — something a reader has to know that the verdict was
	// not computed over.
	StatusNonVoting Status = "non-voting"
)

// Verdict is the review's whole answer, folded from its outcomes. Its values
// spell the same words a report's verdict does.
type Verdict string

const (
	// VerdictPass is a change that may merge unattended.
	VerdictPass Verdict = "pass"
	// VerdictRefer is a change a person has to clear.
	VerdictRefer Verdict = "refer"
	// VerdictFail is a change its author has more work to do on.
	VerdictFail Verdict = "fail"
)

// Kind names the concern an outcome decided, and which of Outcome's fields
// carry its data.
type Kind string

const (
	// KindAPISurface is one opted-in component's public-API comparison that
	// could be computed; its data is Surface and Base. A comparison that could
	// not be computed is a disqualification instead, which already names the
	// component and why.
	KindAPISurface Kind = "api-surface"
	// KindDependencies is one manifest that was compared and added nothing;
	// its data is Manifest and Base. A manifest that added a package, or could
	// not be measured, is a disqualification instead.
	KindDependencies Kind = "dependencies"
	// KindBundling is the exemptions file changed alongside other paths; its
	// data is Bundled.
	KindBundling Kind = "bundling"
	// KindDisqualification is one veto the change tripped; its data is
	// Disqualification.
	KindDisqualification Kind = "disqualification"
	// KindReferral is the referral itself; its data is Decision and
	// Exemptions.
	KindReferral Kind = "referral"
)

// Outcome is one concern the review decided: what was decided, its status,
// and the values it was decided from. Only the fields its Kind names are set.
type Outcome struct {
	Kind   Kind
	Status Status
	// Base is the commit the concern was measured against.
	Base string
	// Surface is the comparison an api-surface outcome reports.
	Surface SurfaceComparison
	// Manifest is the manifest a dependencies outcome reports.
	Manifest ManifestDelta
	// Bundled is every path changed beside the exemptions file.
	Bundled []string
	// Disqualification is the veto a disqualification outcome reports.
	Disqualification referral.Disqualification
	// Decision is the referral, every disqualification already folded in.
	Decision referral.Decision
	// Exemptions is how many exemptions the base declares, which is what tells
	// "nothing is declared" apart from "nothing declared matched".
	Exemptions int
}

// outcomes is the review as a reader meets it: each API-surface comparison,
// each dependency delta measured to add nothing, the bundling check, each
// disqualification, then the referral.
//
// Three verdicts for a computed surface, and which one a break gets is decided
// by the declaration alone (see docs/adr/0040): no break passes, a declared
// break refers, and an undeclared one fails — a gate the author clears by
// restoring the API or declaring the break. Bundling fails for the same reason:
// splitting the change is work the author can do.
//
// The referral refers whenever any disqualification is present, whatever
// Referred says, so a caller that shows only some of the disqualifications
// still shows a referral carrying the severity of the ones it left out.
func outcomes(base, declared string, surfaces []SurfaceComparison, deltas []ManifestDelta, d referral.Decision, exemptions int) []Outcome {
	var out []Outcome
	for _, res := range surfaces {
		if res.Uncomputable != "" {
			continue
		}
		status := StatusFail
		switch {
		case len(res.Findings) == 0:
			status = StatusPass
		case declared != "":
			status = StatusRefer
		}
		out = append(out, Outcome{Kind: KindAPISurface, Status: status, Base: base, Surface: res})
	}
	for _, m := range deltas {
		if m.Unmeasured != "" || len(m.Delta.Added) > 0 {
			continue
		}
		out = append(out, Outcome{Kind: KindDependencies, Status: StatusPass, Base: base, Manifest: m})
	}
	if len(d.Bundled) > 0 {
		out = append(out, Outcome{Kind: KindBundling, Status: StatusFail, Bundled: d.Bundled})
	}
	for _, dq := range d.Disqualifications {
		out = append(out, Outcome{Kind: KindDisqualification, Status: StatusRefer, Disqualification: dq})
	}
	referred := StatusPass
	if d.Referred || len(d.Disqualifications) > 0 {
		referred = StatusRefer
	}
	return append(out, Outcome{Kind: KindReferral, Status: referred, Decision: d, Exemptions: exemptions})
}

// fold is the worst status any outcome reached. A failure outranks a referral
// because it is the author's to clear: fetching a person for a change that is
// not finished yet wastes the person. A non-voting outcome moves nothing.
func fold(outcomes []Outcome) Verdict {
	verdict := VerdictPass
	for _, o := range outcomes {
		switch o.Status {
		case StatusFail:
			return VerdictFail
		case StatusRefer:
			verdict = VerdictRefer
		}
	}
	return verdict
}
