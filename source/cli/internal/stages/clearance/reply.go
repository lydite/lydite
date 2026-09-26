package clearancestages

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

// DescribeClearanceIn is who cleared which revision, and the fingerprint of
// the decision they cleared.
type DescribeClearanceIn struct {
	Login       string
	Head        string
	Fingerprint string
	// Version is lydite's own version, for the reply's footer.
	Version string
}

// DescribeClearanceOut is the clearance as the statuses carry it and as the
// reply answers with it.
type DescribeClearanceOut struct {
	// Description is the status description: who cleared what, then the
	// fingerprint, when there is one.
	Description string
	// Body is the reply to the comment that cleared it.
	Body string
}

// DescribeClearance composes a clearance's description and its reply.
func DescribeClearance(_ context.Context, in DescribeClearanceIn) (DescribeClearanceOut, error) {
	description := clearance.WithFingerprint(
		fmt.Sprintf("cleared by @%s at %s", in.Login, shortSHA(in.Head)), in.Fingerprint)
	body := ui.Comment{
		Verdict:  ui.VerdictPass,
		Headline: description,
		Version:  in.Version,
		Base:     shortSHA(in.Head),
	}.Render()
	return DescribeClearanceOut{Description: description, Body: body}, nil
}

// ComposeReplyIn is a decision that answers with a reply and nothing else,
// and what the reply says about it.
type ComposeReplyIn struct {
	Action clearance.Action
	Login  string
	Head   string
	// Status is the referral standing on Head, nil when none is. An
	// explanation restates it.
	Status *clearance.Status
	// Version is lydite's own version, for the reply's footer.
	Version string
}

// ComposeReplyOut is the reply.
type ComposeReplyOut struct {
	Body string
	// Headline is the reply's one sentence: the verdict an explanation
	// restates, a proposal's summary, or a refusal's reason and way forward.
	Headline string
}

// ComposeReply composes the reply to an explanation, a proposal or a refusal.
//
// A clearance is not one of them — its reply carries the fingerprint, which
// DescribeClearance composes — and neither is a comment nothing addressed;
// either is refused rather than answered with a reply that says the wrong
// thing.
func ComposeReply(_ context.Context, in ComposeReplyIn) (ComposeReplyOut, error) {
	comment := ui.Comment{Version: in.Version, Base: shortSHA(in.Head)}
	switch in.Action.Kind {
	case clearance.KindExplain:
		explanation(&comment, in.Status)
	case clearance.KindExempt:
		if err := proposal(&comment, in.Action); err != nil {
			return ComposeReplyOut{}, err
		}
	case clearance.KindRefuse:
		comment.Verdict = ui.VerdictRefer
		comment.Headline = refusal(in.Action.Reason, in.Login, in.Head)
	default:
		return ComposeReplyOut{}, fmt.Errorf("a decision of kind %d is not answered by a reply alone", in.Action.Kind)
	}
	return ComposeReplyOut{Body: comment.Render(), Headline: comment.Headline}, nil
}

// explanation restates the standing verdict without changing it.
func explanation(comment *ui.Comment, status *clearance.Status) {
	if status == nil {
		comment.Verdict = ui.VerdictRefer
		comment.Headline = "no verdict has been published for this revision yet"
		return
	}
	comment.Headline = status.Description
	switch status.State {
	case clearance.StateSuccess:
		comment.Verdict = ui.VerdictPass
	case clearance.StateFailure:
		comment.Verdict = ui.VerdictFail
	default:
		comment.Verdict = ui.VerdictRefer
	}
}

// proposal answers with a draft exemptions-file entry, and changes nothing.
//
// The name is the commenter's and the paths are the change's own uncovered
// set: a pattern a person typed could widen the entry past what their change
// needs covered, and an entry that reads as lydite's output is the one a
// reviewer is least likely to re-derive.
func proposal(comment *ui.Comment, action clearance.Action) error {
	lines, err := proposalYAML(action.Name, action.Paths)
	if err != nil {
		return err
	}
	comment.Verdict = ui.VerdictRefer
	comment.Headline = fmt.Sprintf("a draft entry covering the %d path(s) no declared exemption covers. "+
		"Nothing has changed and nobody has reviewed this: the referral stands until an entry "+
		"like it is merged into `%s` on the default branch, and the reason has to be answered "+
		"before it will parse.", len(action.Paths), referral.FileName)
	comment.Sections = []ui.CommentSection{{
		Status:  ui.StatusRefer,
		Title:   "proposed exemption",
		Summary: action.Name,
		Details: []ui.CommentDetail{{Lines: lines}},
	}}
	return nil
}

// refusal is what a person reads when their command changed nothing.
//
// Every one of these names the reason and a way forward. A command that
// silently does nothing is the failure this whole surface has to avoid: the
// one thing nobody may conclude from silence is that their change was
// cleared.
func refusal(reason clearance.Reason, login, head string) string {
	switch reason {
	case clearance.ReasonNotPermitted:
		return fmt.Sprintf("@%s does not have write access to this repository, so this changes nothing", login)
	case clearance.ReasonUnknownVerb:
		return "unknown command — this surface has `/lydite clear`, `/lydite explain` and `/lydite exempt <shape>`, " +
			"where a shape is up to 64 letters, digits, dots, dashes and underscores"
	case clearance.ReasonStaleSHA:
		return fmt.Sprintf("that revision is not the current head (%s), so nothing was cleared", shortSHA(head))
	case clearance.ReasonNoStatus:
		return fmt.Sprintf("no verdict has been published for %s yet — there is nothing to clear", shortSHA(head))
	case clearance.ReasonHeadMoved:
		return fmt.Sprintf("the head moved after this comment was written; re-issue `/lydite clear %s` to clear what is there now", shortSHA(head))
	case clearance.ReasonNotReferred:
		return "this is a failing gate, not a referral — it is cleared by splitting the change, not by a comment"
	case clearance.ReasonAlreadyPassing:
		return "this change already merges unattended; there is no referral to clear"
	case clearance.ReasonNothingToPropose:
		// Deliberately not naming which of the two causes this is. A
		// path-only computation cannot tell "covered between several
		// exemptions, by none alone" from "covered, and a disqualifier
		// vetoed the match anyway", and asserting the wrong one sends the
		// reader looking in the wrong place.
		return fmt.Sprintf("every path this change touches is already covered by a declared exemption, "+
			"and it is still referred — so there is no entry to propose. The standing verdict comment "+
			"says what is holding it; widening `%s` is not it", referral.FileName)
	case clearance.ReasonCouldNotDerive:
		return "lydite could not work out which paths this change touches, so there is no entry to " +
			"propose — try again, and if it keeps happening the clearance job's log says what failed"
	default:
		return "nothing to do"
	}
}

// proposalReason is the reason every generated entry carries: a question,
// never a sentence.
//
// A templated real-sounding reason reads as though somebody had thought about
// it, which defeats the requirement that somebody did. The opening literal is
// reserved, so an entry landed with this text still in it is rejected by
// referral.Parse rather than becoming a live exemption.
const proposalReason = referral.ReasonPlaceholderMarker +
	" why is a change touching only these paths safe to merge unread? " +
	"State what this entry's paths guarantee, and nothing the schema does not check."

// proposalFile and proposalEntry are the document a draft is encoded through.
//
// They mirror referral.File's and referral.Exemption's own keys rather than
// being those types, so a draft carries no disqualifiers skeleton and no
// empty condition for a reader to paste and wonder about. A key drifting out
// of step with referral's is one referral.Parse rejects as unknown, which is
// what TestTheProposedEntryDoesNotParseAsAnExemption reads the block back
// through.
type proposalFile struct {
	Exemptions []proposalEntry `yaml:"exemptions"`
}

type proposalEntry struct {
	Name   string   `yaml:"name"`
	Reason string   `yaml:"reason"`
	Paths  []string `yaml:"paths"`
}

// escapeGlob turns a real filename into the pathmatch pattern matching that
// name and nothing else.
//
// An entry's paths are patterns, and a filename is not: `app/[slug]/page.tsx`
// is an ordinary routing convention and a character class at once, so proposed
// verbatim it covers `app/s/page.tsx` and misses the file it was derived from,
// while a file named `**` proposes the pattern covering every path in the
// repository. path.Match — which pathmatch.Match calls per segment — reads a
// backslash as escaping the rune after it, so prefixing every character it
// would otherwise treat as syntax makes the segment literal. A `**` segment
// escapes to `\*\*`, which is not the string pathmatch special-cases as the
// many-segments wildcard and so stays two literal stars.
func escapeGlob(p string) string {
	var b strings.Builder
	b.Grow(len(p)) // [lydite:exclude_from_mutation][Grow only preallocates capacity; strings.Builder writes the same runes in the same order without it, so no observation of the returned string can tell the two apart — only an allocation count, which nothing here measures]
	for _, r := range p {
		switch r {
		case '\\', '*', '?', '[', ']':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// proposalYAML encodes the draft entry.
//
// The encoder is what quotes and escapes every scalar. A name or a path
// assembled into a line by hand carries whatever YAML syntax it contains into
// the document's structure — enough to close the paths sequence early and add
// a second entry, with a reason that answers itself, to something lydite posts
// under its own identity.
func proposalYAML(name string, paths []string) ([]string, error) {
	patterns := make([]string, len(paths))
	for i, p := range paths {
		patterns[i] = escapeGlob(p)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	entry := proposalEntry{Name: name, Reason: proposalReason, Paths: patterns}
	if err := enc.Encode(proposalFile{Exemptions: []proposalEntry{entry}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n"), nil
}

// shortSHA is a revision as a reader is shown it.
func shortSHA(sha string) string {
	return sha[:min(len(sha), 12)]
}
