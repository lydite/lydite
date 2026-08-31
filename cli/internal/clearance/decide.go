package clearance

import (
	"strings"
	"time"
)

// Context is the commit status a referral is published under. It is the
// record: a clearance is a state change on this context at one commit, and
// nothing else stores it.
const Context = "lydite/referral"

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

// Kind is what the caller should do.
type Kind int

const (
	// KindIgnore is a comment that does not address lydite.
	KindIgnore Kind = iota
	// KindClear publishes success on the named revision.
	KindClear
	// KindExplain restates the standing verdict.
	KindExplain
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
)

// Action is the decision.
type Action struct {
	Kind Kind
	// SHA is the revision a clearance applies to.
	SHA string
	// Reason is set when Kind is KindRefuse.
	Reason Reason
}

// Decide answers one request.
//
// It fails closed at every branch: the only path to KindClear is a referral
// standing on the resolved head, published before the comment that clears
// it, from someone with push permission. Every other shape is refused and
// says which, because a clearance that quietly does not happen is
// indistinguishable from one that did.
func Decide(r Request) Action {
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
