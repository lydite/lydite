package clearance

import (
	"testing"
	"time"
)

const head = "4c2eaea1f2b3c4d5e6f708192a3b4c5d6e7f8091"

var (
	commentAt = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	before    = commentAt.Add(-time.Minute)
	after     = commentAt.Add(time.Minute)
)

func referred(at time.Time) *Status {
	return &Status{State: StatePending, CreatedAt: at}
}

func clearRequest(mutate func(*Request)) Request {
	r := Request{
		Command:   Parse("/lydite clear"),
		HeadSHA:   head,
		CanWrite:  true,
		Status:    referred(before),
		CommentAt: commentAt,
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

func TestParseReadsTheVerbAndAnOptionalRevision(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Command
	}{
		{"bare clear", "/lydite clear", Command{Verb: VerbClear, Word: "clear"}},
		{"clear with a revision", "/lydite clear " + head, Command{Verb: VerbClear, Word: "clear", SHA: head}},
		{"explain", "/lydite explain", Command{Verb: VerbExplain, Word: "explain"}},
		{"surrounding whitespace", "   /lydite   clear   ", Command{Verb: VerbClear, Word: "clear"}},
		{"trailing prose on later lines", "/lydite clear\nthanks!", Command{Verb: VerbClear, Word: "clear"}},
		{"ordinary conversation", "looks good to me", Command{Verb: VerbNone}},
		{"addressed but empty", "/lydite", Command{Verb: VerbUnknown}},
		{"a verb we do not have", "/lydite exempt docs", Command{Verb: VerbUnknown, Word: "exempt"}},
		{"not a command, merely mentions one", "you could run /lydite clear here", Command{Verb: VerbNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.body); got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// A reply that quotes an earlier comment carries that comment's text. Reading
// past the first line would let a quoted clearance resolve a referral nobody
// meant to resolve, and a bug that only ever fails open is the one shape this
// package must not have.
func TestParseIgnoresAQuotedCommandOnALaterLine(t *testing.T) {
	body := "I disagree with this.\n\n> /lydite clear\n"
	if got := Parse(body); got.Verb != VerbNone {
		t.Fatalf("Parse quoted body = %+v, want VerbNone", got)
	}
}

func TestOrdinaryConversationIsIgnored(t *testing.T) {
	got := Decide(Request{Command: Parse("ship it"), CanWrite: true})
	if got.Kind != KindIgnore {
		t.Fatalf("Kind = %v, want KindIgnore", got.Kind)
	}
}

// The repository is public, so anyone may comment. Push permission is the
// floor that keeps a stranger from resolving a referral, and it is read about
// the commenter rather than asserted by them.
func TestAStrangerClearsNothing(t *testing.T) {
	for _, body := range []string{"/lydite clear", "/lydite explain", "/lydite nonsense"} {
		got := Decide(clearRequest(func(r *Request) {
			r.Command = Parse(body)
			r.CanWrite = false
		}))
		if got.Kind != KindRefuse || got.Reason != ReasonNotPermitted {
			t.Errorf("%q = %+v, want refuse/not-permitted", body, got)
		}
	}
}

func TestAReferralOnTheHeadIsCleared(t *testing.T) {
	got := Decide(clearRequest(nil))
	if got.Kind != KindClear {
		t.Fatalf("Kind = %v, want KindClear", got.Kind)
	}
	if got.SHA != head {
		t.Errorf("SHA = %q, want the resolved head %q", got.SHA, head)
	}
}

// The isolation gate is the author's to clear by splitting the change. A
// comment resolving it would make the exemption set's history stop being the
// complete record of every widening, which is what the gate exists to protect.
func TestTheIsolationGateIsNotClearableByComment(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Status = &Status{State: StateFailure, CreatedAt: before}
	}))
	if got.Kind != KindRefuse || got.Reason != ReasonNotReferred {
		t.Fatalf("got %+v, want refuse/not-referred", got)
	}
}

// A run that never reached a verdict has none to override.
func TestAnErroredStatusIsNotCleared(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Status = &Status{State: StateError, CreatedAt: before}
	}))
	if got.Kind != KindRefuse || got.Reason != ReasonNotReferred {
		t.Fatalf("got %+v, want refuse/not-referred", got)
	}
}

// Nothing has decided anything about this revision, so there is nothing to
// resolve. Refusing here is also what closes the ordinary form of the race
// below: a push that lands before the comment has no verdict published yet.
func TestAHeadWithNoVerdictIsRefused(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Status = nil }))
	if got.Kind != KindRefuse || got.Reason != ReasonNoStatus {
		t.Fatalf("got %+v, want refuse/no-status", got)
	}
}

// A verdict recorded after the comment was written cannot be the verdict the
// person read, so their decision must not attach to it. Both timestamps come
// from the platform, so neither is the author's to set.
func TestAVerdictNewerThanTheCommentIsRefused(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Status = referred(after) }))
	if got.Kind != KindRefuse || got.Reason != ReasonHeadMoved {
		t.Fatalf("got %+v, want refuse/head-moved", got)
	}
}

// Naming the revision says which one was read, which is a stronger statement
// than an ordering of timestamps — so it is honoured even when the verdict was
// published after the comment.
func TestNamingTheRevisionOverridesTheTimestampGuard(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite clear " + head)
		r.Status = referred(after)
	}))
	if got.Kind != KindClear {
		t.Fatalf("got %+v, want KindClear", got)
	}
}

func TestNamingAnotherRevisionIsRefused(t *testing.T) {
	other := "0a885730000000000000000000000000000000ff"
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite clear " + other)
	}))
	if got.Kind != KindRefuse || got.Reason != ReasonStaleSHA {
		t.Fatalf("got %+v, want refuse/stale-sha", got)
	}
}

func TestAnAbbreviatedRevisionNamesTheHead(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite clear " + head[:8])
	}))
	if got.Kind != KindClear {
		t.Fatalf("abbreviated revision: got %+v, want KindClear", got)
	}
}

// A prefix short enough to name many commits names none of them in
// particular, and the head standing in for a longer string it does not match
// would let a wrong revision be accepted.
func TestARevisionThatDoesNotNameTheHeadIsRefused(t *testing.T) {
	for _, named := range []string{head[:4], head + "ff"} {
		got := Decide(clearRequest(func(r *Request) {
			r.Command = Command{Verb: VerbClear, Word: "clear", SHA: named}
		}))
		if got.Kind != KindRefuse || got.Reason != ReasonStaleSHA {
			t.Errorf("%q = %+v, want refuse/stale-sha", named, got)
		}
	}
}

func TestAChangeThatAlreadyMergesIsNotClearedAgain(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Status = &Status{State: StateSuccess, CreatedAt: before}
	}))
	if got.Kind != KindRefuse || got.Reason != ReasonAlreadyPassing {
		t.Fatalf("got %+v, want refuse/already-passing", got)
	}
}

// A mistyped verb is answered rather than ignored: silence is
// indistinguishable from a broken workflow, and nobody must be able to read a
// typo as a clearance.
func TestAnUnknownVerbIsAnswered(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Command = Parse("/lydite clera") }))
	if got.Kind != KindRefuse || got.Reason != ReasonUnknownVerb {
		t.Fatalf("got %+v, want refuse/unknown-verb", got)
	}
}

func TestExplainNeedsNoStandingVerdict(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite explain")
		r.Status = nil
	}))
	if got.Kind != KindExplain || got.SHA != head {
		t.Fatalf("got %+v, want explain at the head", got)
	}
}

// Only one shape reaches a clearance. This walks the neighbours of the
// clearing request and asserts that changing any single one of them stops it,
// so a later refactor cannot widen the path without failing here.
func TestClearingIsTheOnlyUnrefusedPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Request)
	}{
		{"without permission", func(r *Request) { r.CanWrite = false }},
		{"with no verdict", func(r *Request) { r.Status = nil }},
		{"against a gate", func(r *Request) { r.Status = &Status{State: StateFailure, CreatedAt: before} }},
		{"against a newer verdict", func(r *Request) { r.Status = referred(after) }},
		{"naming another revision", func(r *Request) { r.Command = Command{Verb: VerbClear, SHA: "deadbeefdeadbeef"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(clearRequest(tc.mutate)); got.Kind == KindClear {
				t.Fatalf("%s cleared the change", tc.name)
			}
		})
	}
}
