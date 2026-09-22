package clearance

import (
	"strings"
	"time"
)

// Context is the commit status a referral is published under. It is the
// record: a referral stands on this context at one commit, nothing else
// stores it, and it is the status a clearance is a decision about.
const Context = "lydite/referral"

// ClearanceContext is the commit status a clearance is published under.
//
// A clearance is its own status, written beside Context rather than instead
// of it; both routes a clearance takes also resolve Context to success, since
// Context is the gate a merge waits on and ClearanceContext only records who
// decided. They are separate statuses because they are written under
// different authority: a job that may
// publish a verdict may not clear one, and a job that may clear may not
// publish a verdict. One context both could write is authority neither can
// be scoped to — the relay trusts each allowlisted workflow ref for exactly
// one of these two names.
const ClearanceContext = "lydite/clearance"

// State is a commit status state.
type State string

const (
	// StatePending is a standing referral, waiting on a person.
	StatePending State = "pending"
	// StateSuccess is a change that may merge — exempt, or cleared.
	StateSuccess State = "success"
	// StateFailure is the isolation gate. It is the author's to clear by
	// splitting the change, and no comment resolves it.
	StateFailure State = "failure"
	// StateError is a run that could not reach a verdict.
	StateError State = "error"
)

// Status is one commit status already standing on a revision.
type Status struct {
	State       State
	Description string
	// CreatedAt is when the platform recorded it. Compared against a
	// comment's own timestamp, and both are the platform's rather than
	// anyone's to set, which is what makes the comparison evidence.
	CreatedAt time.Time
}

// Request is everything a decision is made from.
type Request struct {
	Command Command
	// HeadSHA is the pull request's head at the moment the request is
	// resolved, which is not necessarily the head the comment was written
	// against.
	HeadSHA string
	// CanWrite reports whether the commenter has push permission,
	// established from the platform rather than from the comment.
	CanWrite bool
	// Status is the referral status standing on HeadSHA, nil when none is.
	Status *Status
	// CommentAt is when the comment was recorded.
	CommentAt time.Time
}

// Uncover answers which of the change's paths no declared exemption covers.
//
// It is a function rather than a field on Request because answering it needs
// the exemptions file and a list of what the pull request touched, while this
// package is a pure function of a payload and the statuses standing on a
// commit — cmd/lydite is where the two meet. Decide calls it from the one
// branch that needs the answer and nowhere else, so a command refused for
// permission, a stale revision, a missing verdict or a moved head never pays
// for the read and the API call behind it.
type Uncover func() ([]string, error)

// Kind is what the caller should do.
type Kind int

const (
	// KindIgnore is a comment that does not address lydite.
	KindIgnore Kind = iota
	// KindClear publishes success on the named revision.
	KindClear
	// KindExplain restates the standing verdict.
	KindExplain
	// KindExempt answers with a proposed exemptions-file entry, and changes
	// nothing. Landing it is a pull request somebody opens.
	KindExempt
	// KindRefuse answers without changing any status.
	KindRefuse
)

// Reason says why a request was refused. It is a value rather than a
// sentence so that wording lives in the rendering layer, where every other
// string a person reads is already decided.
type Reason string

const (
	// ReasonNotPermitted is a commenter without push permission.
	ReasonNotPermitted Reason = "not-permitted"
	// ReasonUnknownVerb is a comment addressed to lydite naming no verb
	// this surface has.
	ReasonUnknownVerb Reason = "unknown-verb"
	// ReasonStaleSHA is a clearance naming a revision that is no longer the
	// head.
	ReasonStaleSHA Reason = "stale-sha"
	// ReasonNoStatus is a head no referral has been published for. Nothing
	// has decided anything yet, so there is nothing to resolve.
	ReasonNoStatus Reason = "no-status"
	// ReasonHeadMoved is a status published after the comment was written,
	// so the person cannot have read it.
	ReasonHeadMoved Reason = "head-moved"
	// ReasonNotReferred is the isolation gate, which a comment does not
	// resolve.
	ReasonNotReferred Reason = "not-referred"
	// ReasonAlreadyPassing is a revision that already merges unattended.
	ReasonAlreadyPassing Reason = "already-passing"
	// ReasonNothingToPropose is a referred change every one of whose paths
	// some declared exemption already covers, so there is nothing left to
	// name: no path list would add coverage. Which of the things that can
	// refer it anyway is doing so is not knowable from here — the paths may
	// split across exemptions that together cover them where no single one
	// does, or a disqualifier this computation never sees may be vetoing
	// the match. Either way there is no entry to propose.
	ReasonNothingToPropose Reason = "nothing-to-propose"
	// ReasonCouldNotDerive is an exempt request whose uncovered set could
	// not be answered at all — an unreadable exemptions file, a changed-path
	// list the platform would not hand over whole. It is refused out loud
	// rather than left as an error nobody replies to: a command that fails
	// in silence is indistinguishable from one that was accepted.
	ReasonCouldNotDerive Reason = "could-not-derive"
)

// Action is the decision.
type Action struct {
	Kind Kind
	// SHA is the revision a clearance applies to.
	SHA string
	// Reason is set when Kind is KindRefuse.
	Reason Reason
	// Name and Paths are the proposed entry, set when Kind is KindExempt.
	// Name is the commenter's; Paths is derived from the change, and is
	// the whole of what the entry would cover.
	Name  string
	Paths []string
}

// Decide answers one request.
//
// It fails closed at every branch: the only path to KindClear is a referral
// standing on the resolved head, published before the comment that clears
// it, from someone with push permission. Every other shape is refused and
// says which, because a clearance that quietly does not happen is
// indistinguishable from one that did.
//
// KindExempt passes the same ladder, and there is no shortcut for it being
// the verb that changes nothing: a proposal derived from a head the commenter
// never read names the wrong paths, which is clearing code nobody saw
// delivered as a suggestion. uncover is consulted past the last of those
// gates and nowhere earlier, so the ladder is what decides whether the work
// behind it happens at all.
func Decide(r Request, uncover Uncover) Action {
	if r.Command.Verb == VerbNone {
		return Action{Kind: KindIgnore}
	}
	// Permission gates the whole surface rather than the clearing verb
	// alone. Explaining is harmless in itself, but it makes lydite write a
	// comment on demand, and an endpoint that posts on any stranger's word
	// is one worth not having.
	if !r.CanWrite {
		return Action{Kind: KindRefuse, Reason: ReasonNotPermitted}
	}
	if r.Command.Verb == VerbUnknown {
		return Action{Kind: KindRefuse, Reason: ReasonUnknownVerb}
	}
	if r.Command.Verb == VerbExplain {
		return Action{Kind: KindExplain, SHA: r.HeadSHA}
	}

	// A named revision is checked before anything is read about the head.
	// It is the author saying which revision they read, and disagreeing
	// with the head means the answer would be about something else.
	if r.Command.SHA != "" && !namesSHA(r.Command.SHA, r.HeadSHA) {
		return Action{Kind: KindRefuse, Reason: ReasonStaleSHA}
	}
	if r.Status == nil {
		return Action{Kind: KindRefuse, Reason: ReasonNoStatus}
	}
	// A verdict recorded after the comment was written is one the person
	// cannot have read, so clearing it would attach their decision to code
	// they never saw. Both timestamps come from the platform.
	//
	// An explicitly named revision is exempt from this: naming the revision
	// is a stronger statement about what was read than an ordering of
	// timestamps, and it has already been checked against the head above.
	if r.Command.SHA == "" && r.Status.CreatedAt.After(r.CommentAt) {
		return Action{Kind: KindRefuse, Reason: ReasonHeadMoved}
	}
	switch r.Status.State {
	case StatePending:
		if r.Command.Verb == VerbExempt {
			uncovered, err := uncover()
			if err != nil {
				return Action{Kind: KindRefuse, Reason: ReasonCouldNotDerive}
			}
			// Nothing to propose is its own answer rather than an empty
			// entry: every changed path is already covered by something,
			// so no path list would make this change exempt.
			if len(uncovered) == 0 {
				return Action{Kind: KindRefuse, Reason: ReasonNothingToPropose}
			}
			return Action{Kind: KindExempt, SHA: r.HeadSHA, Name: r.Command.Shape, Paths: uncovered}
		}
		return Action{Kind: KindClear, SHA: r.HeadSHA}
	case StateSuccess:
		return Action{Kind: KindRefuse, Reason: ReasonAlreadyPassing}
	default:
		// A failure is the isolation gate and a comment does not resolve
		// it; an error never reached a verdict, so there is none to
		// override. Neither is a referral, and only a referral is
		// clearable.
		return Action{Kind: KindRefuse, Reason: ReasonNotReferred}
	}
}

// namesSHA reports whether what the author typed names the resolved head,
// accepting the abbreviations git and the platform both display.
//
// The test is one-directional on purpose: a prefix of the head is accepted,
// and the head being a prefix of what was typed is not. Anything else would
// let a shorter string stand in for a longer one it does not name. Seven
// characters is the floor because git's own default abbreviation is seven,
// and a shorter prefix names too many commits to be a revision.
func namesSHA(named, head string) bool {
	if len(named) < 7 || len(named) > len(head) {
		return false
	}
	return strings.EqualFold(head[:len(named)], named)
}
