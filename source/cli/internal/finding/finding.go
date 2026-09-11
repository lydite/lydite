// Package finding is the located claims lydite's gates make about the code,
// as data rather than as the prose a row renders.
//
// A finding is a located claim about the code that one edit clears. That
// definition is what decides who may emit one. The CRAP gate names a function,
// mutation names a survivor, patch coverage names a run of untested new lines
// and a scanner names its rule's site; each is one thing an author fixes. Per
// line is not a finding — a three-hundred-line untested addition is not three
// hundred claims, there is no per-line action, and neighbouring lines have no
// identity to tell them apart. Nor is an orphaned file, whose one edit is to
// the component declaration rather than to the file a reader would be pointed
// at.
//
// The package is a leaf and holds no logic of any gate's, so the report
// document and every producer can both import it.
package finding

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// Anchor is how precisely a finding can be attached to a change.
//
// It is decided where the finding is produced, from the changed-line map the
// gate already holds, and never by whatever posts it. The renderer of the
// standing comment is pure — no network, no git — so a question it cannot ask
// has to be answered before it reads the document.
type Anchor string

const (
	// AnchorNowhere is a claim that reaches the change at neither a line nor a
	// file: one in a file the change never touched, and equally one made by a
	// run that knew of no change at all — a scan over a whole repository
	// rather than over a diff. The two are one answer because nothing acts on
	// them differently; both belong in the standing comment.
	//
	// It is the zero value on purpose. A claim defaults to unanchorable and a
	// producer that can prove otherwise says so, which fails towards the
	// standing comment: a claim listed there when it could have had a line is
	// a lost precision, while one offered to a platform that then refuses it
	// is a surface that lost the claim entirely.
	AnchorNowhere Anchor = ""
	// AnchorLine is a finding on a line the change touched.
	AnchorLine Anchor = "line"
	// AnchorFile is a finding in a file the change touched, on a line it did
	// not. A hosting platform that anchors to a diff cannot reach the line,
	// and pointing at a nearby one that the change did happen to touch would
	// be the surface lying about where the problem is.
	//
	// The CRAP gate produces claims below this on purpose: a function crosses
	// the threshold because a call site grew or its test was deleted, which is
	// the blind spot the gate exists to cover, and neither edit is in the
	// function.
	AnchorFile Anchor = "file"
)

// Version prefixes every fingerprint.
//
// Changing how a fingerprint is derived orphans everything anchored to the old
// one, so the version is what makes such a change visible instead of silent: a
// reader keeps understanding every version ever emitted while a writer emits
// only the current one, which is what lets old anchors be recognised as stale
// rather than mistaken for a different finding.
const Version = "v1"

// Finding is one located claim.
//
// Every field a consumer needs is on the finding itself rather than recovered
// from the row that reported it. A row's label is prose — `gosec(cli)` — and
// parsing a component back out of it is the text-scraping this whole channel
// exists to remove.
type Finding struct {
	// Gate is what made the claim: a scanner's name, or `crap`, `mutation`,
	// `patch`.
	Gate string `json:"gate"`
	// Component is the declared component the claim is about, by name. Names
	// are unique by construction and directories are not. It is empty for a
	// check that is root-scoped rather than per component.
	Component string `json:"component,omitempty"`
	// Path is the file, relative to the scan root.
	Path string `json:"path"`
	// Line is where the claim starts, and EndLine where it ends. EndLine is
	// zero for a claim about a single line. They locate the finding for a
	// reader and take no part in its identity.
	Line    int `json:"line"`
	EndLine int `json:"end_line,omitempty"`
	// Rule is the identifier the tool gave, and is the one thing every
	// scanner agrees to report. Empty for a gate that has no rules.
	Rule string `json:"rule,omitempty"`
	// Severity is the tool's own word for it, carried unchanged. Normalising
	// five vocabularies into one is a decision of its own, and inventing a
	// scale here would put lydite's guess where a tool's statement was.
	Severity string `json:"severity,omitempty"`
	// Message is the one-line claim.
	Message string `json:"message"`
	// Detail is the tool's extended text — a call stack, a code excerpt, the
	// lines a rule matched. It is what a reader needs and what no summary
	// line has room for.
	Detail []string `json:"detail,omitempty"`
	// Site is what identifies this claim independently of where it sits in
	// the file, and each gate supplies its own: a rule id with the flagged
	// source text, a function's name with its receiver, an operator with the
	// text it replaced. It is the whole of what makes a fingerprint survive
	// an edit above it.
	//
	// Site and Ordinal travel in the document although Fingerprint is derived
	// from them and could replace both, because a fingerprint nobody can check
	// is a hash somebody has to trust. Carrying the ingredients is what lets a
	// reader see why two claims are the same one, or why they are not.
	Site string `json:"site"`
	// Ordinal separates claims whose Site is identical within one file, by
	// source order. Two identical comparisons on two lines produce two
	// mutants alike in everything a line number is excluded from, and without
	// this they are one finding reported once.
	Ordinal int `json:"ordinal"`
	// Row is the label of the report row that made this claim, written by the
	// producer that already holds both. It is the one link back, and it is a
	// label rather than a nesting: a finding still carries its own gate and
	// component, so nothing has to parse `gosec(cli)` back apart to know what
	// this is about.
	//
	// What reads it is the standing comment, which renders a failing row's
	// detail from that row's unanchored findings and counts the located ones
	// it is leaving to the review. Without it the two surfaces cannot
	// partition one row's claims between them, and the comment would repeat
	// what a thread already says on the line.
	//
	// It takes no part in the fingerprint. A row's label carries a component
	// and a gate that are already ingredients, and relabelling a row is not
	// finding something new.
	Row string `json:"row,omitempty"`
	// Anchor is how precisely this can be attached to the change.
	Anchor Anchor `json:"anchor,omitempty"`
}

// Fingerprint identifies the claim across runs.
//
// It is derived on demand and is not a field of the document. A stored
// fingerprint beside the ingredients it comes from is two statements of one
// fact, and the day they disagree the stored one wins while being wrong.
//
// It is derived here rather than by each gate so there is one implementation
// of it, for the reason there is one path matcher and one port-conflict
// predicate: a second copy agrees until one of them learns something, and the
// disagreement shows up as a duplicate anchor beside the original rather than
// as a failing test.
//
// A line number is deliberately not an ingredient. An edit anywhere above a
// finding moves its line while changing nothing about the claim, so a
// line-keyed identity reports the same finding as a new one on every push.
//
// Neither is Message nor Severity: a tool that rewords its own diagnostic, or
// reclassifies it, has not found something different. Path is an ingredient,
// so moving a file re-identifies everything in it — which is the right way
// round, because the alternative is two functions of one name in one component
// sharing an identity, and a claim attached to the wrong code is worse than
// one attached afresh.
func (f Finding) Fingerprint() string {
	h := sha256.New()
	for _, part := range []string{
		f.Gate,
		f.Component,
		f.Path,
		f.Site,
		strconv.Itoa(f.Ordinal),
	} {
		// A separator that cannot occur in any ingredient, so `ab` beside `c`
		// and `a` beside `bc` cannot hash alike.
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return Version + ":" + hex.EncodeToString(h.Sum(nil))[:16]
}

// Normalise reduces source text to what identifies it, so that reindenting a
// line or wrapping it differently does not re-identify the claim on it.
func Normalise(s string) string { return strings.Join(strings.Fields(s), " ") }

// Number assigns each finding its Ordinal, by counting how many findings with
// the same Path and Site came before it.
//
// It takes the whole set at once because an ordinal is a fact about a
// finding's neighbours rather than about the finding, so a gate emitting them
// one at a time cannot know it. The order given is the order counted, so a
// producer hands them over in source order and two runs over one tree agree.
func Number(findings []Finding) {
	seen := map[[2]string]int{}
	for i := range findings {
		key := [2]string{findings[i].Path, findings[i].Site}
		findings[i].Ordinal = seen[key]
		seen[key]++
	}
}

// Anchored raises each finding's Anchor to what the lines a change touched
// allow. A producer that holds no such map leaves every claim at
// AnchorNowhere, which is the safe default rather than an omission.
//
// The map is the one every gate already holds, and its lines are the change's
// added lines with no context around them. That matters in one direction only:
// a set narrower than what a hosting platform will accept costs a finding its
// precise anchor and never costs the run an anchor the platform refuses.
func Anchored(findings []Finding, changed map[string][]int) {
	for i := range findings {
		reach := AnchorNowhere
		if lines, ok := changed[findings[i].Path]; ok {
			reach = AnchorFile
			for _, line := range lines {
				if line >= findings[i].Line && line <= max(findings[i].Line, findings[i].EndLine) {
					reach = AnchorLine
					break
				}
			}
		}
		findings[i].Anchor = byReach[max(reach.reach(), findings[i].Anchor.reach())]
	}
}

// byReach orders the anchors, so a second pass over a map that does not cover
// a claim cannot quietly take back an anchor an earlier one established.
// Raising and never lowering is what makes the call safe to repeat, and a
// producer that anchors against the wrong map is a bug that shows up as a claim
// in the standing comment rather than as one silently mislocated.
//
// The rule is a max over that order rather than a comparison, so there is no
// boundary to shift: two passes agreeing on how far a claim reaches take the
// same path as one that reaches further.
var byReach = [...]Anchor{AnchorNowhere, AnchorFile, AnchorLine}

// reach is how far this anchor gets, as an index into byReach.
func (a Anchor) reach() int {
	switch a {
	case AnchorLine:
		return 2
	case AnchorFile:
		return 1
	default:
		return 0
	}
}
