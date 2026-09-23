package clearance

import (
	"errors"
	"strings"
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

func exemptRequest(mutate func(*Request)) Request {
	r := clearRequest(func(r *Request) { r.Command = Parse("/lydite exempt docs-only") })
	if mutate != nil {
		mutate(&r)
	}
	return r
}

// uncovering is the answer a test hands the ladder when it should reach the
// branch that asks for one.
func uncovering(paths ...string) Uncover {
	return func() ([]string, error) { return paths, nil }
}

// unasked fails the test if the ladder derives the uncovered set at all.
// Deriving it reads the exemptions file and asks the platform what the pull
// request touched, and a request the ladder refuses without that answer must
// not pay for it.
func unasked(t *testing.T) Uncover {
	return func() ([]string, error) {
		t.Helper()
		t.Error("the uncovered set was derived for a request refused without needing it")
		return nil, nil
	}
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
		{"exempt with a shape", "/lydite exempt docs", Command{Verb: VerbExempt, Word: "exempt", Shape: "docs"}},
		// The entry has to be named by the person asking for it, and a bare
		// command is answered rather than ignored: nobody may read silence
		// as a command that was accepted.
		{"exempt with no shape", "/lydite exempt", Command{Verb: VerbUnknown, Word: "exempt"}},
		{"exempt with a separated shape", "/lydite exempt moved-sources", Command{Verb: VerbExempt, Word: "exempt", Shape: "moved-sources"}},
		// The shape is rendered into a comment lydite signs, so one carrying
		// markup is refused outright rather than escaped into a name nobody
		// asked for.
		{"exempt with markup for a shape", "/lydite exempt </summary><h3>forged", Command{Verb: VerbUnknown, Word: "exempt"}},
		{"exempt with a shape starting in a separator", "/lydite exempt -docs", Command{Verb: VerbUnknown, Word: "exempt"}},
		{"exempt with a shape of 64 characters", "/lydite exempt " + strings.Repeat("a", 64),
			Command{Verb: VerbExempt, Word: "exempt", Shape: strings.Repeat("a", 64)}},
		{"exempt with a shape of 65 characters", "/lydite exempt " + strings.Repeat("a", 65),
			Command{Verb: VerbUnknown, Word: "exempt"}},
		{"a verb we do not have", "/lydite bless docs", Command{Verb: VerbUnknown, Word: "bless"}},
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
	got := Decide(Request{Command: Parse("ship it"), CanWrite: true}, unasked(t))
	if got.Kind != KindIgnore {
		t.Fatalf("Kind = %v, want KindIgnore", got.Kind)
	}
}

// The repository is public, so anyone may comment. Push permission is the
// floor that keeps a stranger from resolving a referral, and it is read about
// the commenter rather than asserted by them.
func TestAStrangerClearsNothing(t *testing.T) {
	for _, body := range []string{"/lydite clear", "/lydite explain", "/lydite exempt docs", "/lydite nonsense"} {
		got := Decide(clearRequest(func(r *Request) {
			r.Command = Parse(body)
			r.CanWrite = false
		}), unasked(t))
		if got.Kind != KindRefuse || got.Reason != ReasonNotPermitted {
			t.Errorf("%q = %+v, want refuse/not-permitted", body, got)
		}
	}
}

func TestAReferralOnTheHeadIsCleared(t *testing.T) {
	got := Decide(clearRequest(nil), unasked(t))
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
	}), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonNotReferred {
		t.Fatalf("got %+v, want refuse/not-referred", got)
	}
}

// A run that never reached a verdict has none to override.
func TestAnErroredStatusIsNotCleared(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Status = &Status{State: StateError, CreatedAt: before}
	}), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonNotReferred {
		t.Fatalf("got %+v, want refuse/not-referred", got)
	}
}

// Nothing has decided anything about this revision, so there is nothing to
// resolve. Refusing here is also what closes the ordinary form of the race
// below: a push that lands before the comment has no verdict published yet.
func TestAHeadWithNoVerdictIsRefused(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Status = nil }), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonNoStatus {
		t.Fatalf("got %+v, want refuse/no-status", got)
	}
}

// A verdict recorded after the comment was written cannot be the verdict the
// person read, so their decision must not attach to it. Both timestamps come
// from the platform, so neither is the author's to set.
func TestAVerdictNewerThanTheCommentIsRefused(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Status = referred(after) }), unasked(t))
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
	}), unasked(t))
	if got.Kind != KindClear {
		t.Fatalf("got %+v, want KindClear", got)
	}
}

func TestNamingAnotherRevisionIsRefused(t *testing.T) {
	other := "0a885730000000000000000000000000000000ff"
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite clear " + other)
	}), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonStaleSHA {
		t.Fatalf("got %+v, want refuse/stale-sha", got)
	}
}

func TestAnAbbreviatedRevisionNamesTheHead(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite clear " + head[:8])
	}), unasked(t))
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
		}), unasked(t))
		if got.Kind != KindRefuse || got.Reason != ReasonStaleSHA {
			t.Errorf("%q = %+v, want refuse/stale-sha", named, got)
		}
	}
}

func TestAChangeThatAlreadyMergesIsNotClearedAgain(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Status = &Status{State: StateSuccess, CreatedAt: before}
	}), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonAlreadyPassing {
		t.Fatalf("got %+v, want refuse/already-passing", got)
	}
}

// A mistyped verb is answered rather than ignored: silence is
// indistinguishable from a broken workflow, and nobody must be able to read a
// typo as a clearance.
func TestAnUnknownVerbIsAnswered(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) { r.Command = Parse("/lydite clera") }), unasked(t))
	if got.Kind != KindRefuse || got.Reason != ReasonUnknownVerb {
		t.Fatalf("got %+v, want refuse/unknown-verb", got)
	}
}

func TestExplainNeedsNoStandingVerdict(t *testing.T) {
	got := Decide(clearRequest(func(r *Request) {
		r.Command = Parse("/lydite explain")
		r.Status = nil
	}), unasked(t))
	if got.Kind != KindExplain || got.SHA != head {
		t.Fatalf("got %+v, want explain at the head", got)
	}
}

func TestAReferredChangeWithUncoveredPathsGetsAProposal(t *testing.T) {
	got := Decide(exemptRequest(nil), uncovering("docs/one.md"))
	if got.Kind != KindExempt {
		t.Fatalf("Kind = %v, want KindExempt", got.Kind)
	}
	if got.Name != "docs-only" {
		t.Errorf("Name = %q, want the shape the commenter named", got.Name)
	}
	if len(got.Paths) != 1 || got.Paths[0] != "docs/one.md" {
		t.Errorf("Paths = %v, want the change's own uncovered set", got.Paths)
	}
}

// There is no path list that would make such a change exempt: proposing its
// full set would be an entry duplicating others and widened by their union,
// and proposing an empty one would not parse.
func TestAChangeWithNothingUncoveredGetsNoProposal(t *testing.T) {
	got := Decide(exemptRequest(nil), uncovering())
	if got.Kind != KindRefuse || got.Reason != ReasonNothingToPropose {
		t.Fatalf("got %+v, want refuse/nothing-to-propose", got)
	}
}

// An uncovered set that could not be answered is refused, not proposed over
// and not swallowed: the commenter reads why, because the one thing nobody
// may conclude from silence is that their change was cleared.
func TestAnUnanswerableUncoveredSetIsRefused(t *testing.T) {
	failing := func() ([]string, error) { return nil, errors.New("the platform would not say") }
	got := Decide(exemptRequest(nil), failing)
	if got.Kind != KindRefuse || got.Reason != ReasonCouldNotDerive {
		t.Fatalf("got %+v, want refuse/could-not-derive", got)
	}
}

// An exemption is proposed for a referral and nothing else: a change that
// already merges has none, and a gate or an errored run is not one.
func TestExemptRefusesEveryStatusButAReferral(t *testing.T) {
	for _, tc := range []struct {
		state State
		want  Reason
	}{
		{StateSuccess, ReasonAlreadyPassing},
		{StateFailure, ReasonNotReferred},
		{StateError, ReasonNotReferred},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			got := Decide(exemptRequest(func(r *Request) {
				r.Status = &Status{State: tc.state, CreatedAt: before}
			}), unasked(t))
			if got.Kind != KindRefuse || got.Reason != tc.want {
				t.Fatalf("got %+v, want refuse/%s", got, tc.want)
			}
		})
	}
}

// The proposal passes the same ladder a clearance does. There is no shortcut
// for it being the verb that changes nothing: one derived from a head the
// commenter never read names the wrong paths.
func TestProposingIsRefusedByEveryReasonAClearanceIs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Request)
		want   Reason
	}{
		{"without permission", func(r *Request) { r.CanWrite = false }, ReasonNotPermitted},
		{"with no verdict", func(r *Request) { r.Status = nil }, ReasonNoStatus},
		{"against a newer verdict", func(r *Request) { r.Status = referred(after) }, ReasonHeadMoved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(exemptRequest(tc.mutate), unasked(t))
			if got.Kind != KindRefuse || got.Reason != tc.want {
				t.Fatalf("got %+v, want refuse/%s", got, tc.want)
			}
		})
	}
}

// fingerprint is the shape referral.Fingerprint produces: sixteen hex
// characters, written as a literal here so this package stays a pure function
// of a description.
const fingerprint = "0f1e2d3c4b5a6978"

// The description is the whole of where a clearance's fingerprint is stored,
// so what goes in comes back out — including when the human half carries
// brackets of its own, which the closing marker's position must not be read
// from.
func TestAClearancesFingerprintComesBackOutOfItsDescription(t *testing.T) {
	for _, description := range []string{
		"cleared by @pedromvgomes at 4c2eaea",
		"cleared by @pedromvgomes at 4c2eaea [see the thread]",
	} {
		composed := WithFingerprint(description, fingerprint)
		got, ok := FingerprintIn(composed)
		if !ok || got != fingerprint {
			t.Errorf("FingerprintIn(%q) = %q, %v; want %q, true", composed, got, ok, fingerprint)
		}
	}
}

// A clearer handle long enough to spend the whole budget loses its own tail
// and never the fingerprint: the attribution is legible cut short, while a cut
// fingerprint compares unequal to the decision it was taken over — silently,
// since nothing downstream can tell a truncated value from a different one.
func TestALongClearerHandleIsCutAndTheFingerprintIsNot(t *testing.T) {
	// Longer than any login the platform issues, so the budget is exercised
	// rather than assumed from the lengths a login happens to reach.
	handle := strings.Repeat("handle", 40)
	composed := WithFingerprint("cleared by @"+handle+" at 4c2eaea", fingerprint)

	if n := len([]rune(composed)); n > DescriptionLimit {
		t.Errorf("the description is %d characters, past the platform's cap of %d: %q", n, DescriptionLimit, composed)
	}
	got, ok := FingerprintIn(composed)
	if !ok || got != fingerprint {
		t.Errorf("FingerprintIn(%q) = %q, %v; want %q, true", composed, got, ok, fingerprint)
	}
	if !strings.HasPrefix(composed, "cleared by @handle") {
		t.Errorf("the attribution was not kept: %q", composed)
	}
}

// A description with no fingerprint field is its own answer, not an empty
// fingerprint. A caller comparing against it has nothing to compare, and
// reading absence as agreement would carry a clearance forward over a decision
// nobody established was the same one.
func TestADescriptionCarryingNoFingerprintIsNotAnEmptyOne(t *testing.T) {
	for _, tc := range []struct {
		name        string
		description string
		want        string
		wantOK      bool
	}{
		{"no field at all", "cleared by @pedromvgomes at 4c2eaea", "", false},
		{"an empty field", "cleared by @pedromvgomes at 4c2eaea [fp:]", "", true},
		{"a field the platform truncated", "cleared by @pedromvgomes at 4c2eaea [fp:0f1e2d3c…", "", false},
		{"bracketed text that is not a field", "cleared by @pedromvgomes at 4c2eaea [see the thread]", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := FingerprintIn(tc.description)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("FingerprintIn(%q) = %q, %v; want %q, %v", tc.description, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// Recording no fingerprint writes no field, so a run that has none to record
// leaves a description a reader refuses rather than one it compares against an
// empty value.
func TestAnEmptyFingerprintIsNotRecordedAsAField(t *testing.T) {
	description := "cleared by @pedromvgomes at 4c2eaea"
	if got := WithFingerprint(description, ""); got != description {
		t.Errorf("WithFingerprint(%q, \"\") = %q, want it unchanged", description, got)
	}
	if got, ok := FingerprintIn(WithFingerprint(description, "")); ok {
		t.Errorf("an unrecorded fingerprint reads back as present: %q", got)
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
			if got := Decide(clearRequest(tc.mutate), unasked(t)); got.Kind == KindClear {
				t.Fatalf("%s cleared the change", tc.name)
			}
		})
	}
}
