package threads

import (
	"encoding/json"
	"strings"
	"testing"

	"lydite/lydite/internal/finding"
)

func claim(gate, path string, line int, anchor finding.Anchor) finding.Finding {
	return finding.Finding{
		Gate: gate, Component: "cli", Path: path, Line: line,
		Message: "a claim", Site: gate + path, Row: gate + "(cli)", Anchor: anchor,
	}
}

// The token after the prefix is the fingerprint itself, so nothing has to
// derive a second identifier and the two can never disagree.
func TestTheMarkerCarriesTheFingerprintVerbatim(t *testing.T) {
	f := claim("crap", "a.go", 10, finding.AnchorFile)
	if got := FingerprintIn(Marker(f.Fingerprint())); got != f.Fingerprint() {
		t.Fatalf("round trip: got %q, want %q", got, f.Fingerprint())
	}
}

// A thread written under a fingerprint formula this binary never emitted is
// still read: it matches no current claim and so takes the cleared path, which
// is what makes a formula bump self-heal in one round of delete and repost.
func TestAnUnknownFingerprintFormulaIsStillParsed(t *testing.T) {
	if got := FingerprintIn("<!-- lydite:finding:v9:deadbeef -->\nwhatever"); got != "v9:deadbeef" {
		t.Fatalf("got %q", got)
	}
	for _, body := range []string{"no marker here", "<!-- lydite:finding: -->", "<!-- lydite:finding:not one token -->", "<!-- lydite:finding:v1:abc"} {
		if got := FingerprintIn(body); got != "" {
			t.Errorf("%q should carry no fingerprint, got %q", body, got)
		}
	}
}

// lydite is a participant in its own thread however its replies were
// authored, because every comment it writes carries the marker and the
// predicate reads that rather than a login.
func TestSoleParticipantReadsTheMarkerAndNotTheAuthor(t *testing.T) {
	root := Comment{ID: 1, Body: Marker("v1:abc") + "\nclaim"}
	lydite := Comment{ID: 2, Body: Marker("v1:abc") + "\nstill true", InReplyTo: 1}
	human := Comment{ID: 3, Body: "I disagree", InReplyTo: 1}

	if !(Thread{Root: root, Replies: []Comment{lydite}}).Sole() {
		t.Error("a thread of lydite's own comments is one lydite is alone in")
	}
	if (Thread{Root: root, Replies: []Comment{lydite, human}}).Sole() {
		t.Error("somebody spoke, so lydite is not alone")
	}
	if (Thread{Root: Comment{ID: 9, Body: "a human's thread"}}).Sole() {
		t.Error("a thread lydite never wrote is not lydite's to be alone in")
	}
}

// A reply whose root is on a page this listing never reached is dropped, so it
// cannot be mistaken for a root lydite is the sole participant in.
func TestThreadsGroupRepliesUnderTheirRoot(t *testing.T) {
	got := Threads([]Comment{
		{ID: 1, Body: "root"},
		{ID: 2, Body: "reply", InReplyTo: 1},
		{ID: 3, Body: "orphan", InReplyTo: 99},
		{ID: 4, Body: "another root"},
	})
	if len(got) != 2 {
		t.Fatalf("expected two threads, got %d", len(got))
	}
	if len(got[0].Replies) != 1 || got[0].Replies[0].ID != 2 {
		t.Errorf("the reply did not land under its root: %+v", got[0])
	}
	if len(got[1].Replies) != 0 {
		t.Errorf("the orphan was adopted: %+v", got[1])
	}
}

// Only a claim that reaches the change goes to a review; everything else is
// the standing comment's, and the partition is read off the anchor alone.
func TestOnlyLocatedClaimsReachAReview(t *testing.T) {
	got := Located([]finding.Finding{
		claim("crap", "a.go", 1, finding.AnchorNowhere),
		claim("patch", "b.go", 2, finding.AnchorFile),
		claim("mutation", "c.go", 3, finding.AnchorLine),
	})
	if len(got) != 2 || got[0].Gate != "patch" || got[1].Gate != "mutation" {
		t.Fatalf("got %+v", got)
	}
}

// A document a local run wrote has no fold at all, so two shards' copies of
// one claim arrive here and only one may become a thread.
func TestDedupKeepsTheFirstUnderEachFingerprint(t *testing.T) {
	f := claim("crap", "a.go", 10, finding.AnchorFile)
	second := f
	second.Line = 40
	kept, dropped := Dedup([]finding.Finding{f, second, claim("patch", "b.go", 1, finding.AnchorLine)})
	if len(kept) != 2 || kept[0].Line != 10 {
		t.Fatalf("the first occurrence did not win: %+v", kept)
	}
	if len(dropped) != 1 || dropped[0] != f.Fingerprint() {
		t.Fatalf("the drop was not named: %+v", dropped)
	}
}

// A claim with no thread gets one, on the line when the change touches it and
// on the file when it does not.
func TestAClaimWithNoThreadIsOpened(t *testing.T) {
	line := claim("mutation", "a.go", 12, finding.AnchorLine)
	file := claim("crap", "b.go", 400, finding.AnchorFile)
	ops := Delta([]finding.Finding{line, file}, nil, 42, "abc123")

	if ops.Version != Version || ops.PullRequest != 42 || ops.Head != "abc123" {
		t.Fatalf("the document does not name the run: %+v", ops)
	}
	if len(ops.Create) != 2 {
		t.Fatalf("expected two creates, got %+v", ops.Create)
	}
	if ops.Create[0].Subject != "line" || ops.Create[0].Line != 12 {
		t.Errorf("a line claim did not anchor to its line: %+v", ops.Create[0])
	}
	if ops.Create[1].Subject != "file" || ops.Create[1].Line != 0 {
		t.Errorf("a file claim must carry no line: %+v", ops.Create[1])
	}
	if !strings.HasPrefix(ops.Create[0].Body, Marker(line.Fingerprint())) {
		t.Errorf("the body does not open with the marker: %q", ops.Create[0].Body)
	}
}

// A thread already standing for a claim is left exactly as it is: reposting it
// on every push is the churn the fingerprint exists to prevent.
func TestAStandingThreadIsLeftAlone(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	ops := Delta([]finding.Finding{f}, []Thread{{Root: Comment{ID: 7, Body: Body(f), Line: 12}}}, 42, "abc")
	if len(ops.Create) != 0 || len(ops.Reply) != 0 || len(ops.Delete) != 0 {
		t.Fatalf("nothing should have happened: %+v", ops)
	}
}

// A cleared claim takes its thread with it when lydite is alone in it, and
// the root goes last so a refusal partway through never leaves a headless run
// of replies the platform shows under nothing.
func TestAClearedClaimDeletesItsOwnThread(t *testing.T) {
	gone := claim("mutation", "a.go", 12, finding.AnchorLine)
	thread := Thread{
		Root: Comment{ID: 7, Body: Body(gone)},
		Replies: []Comment{
			{ID: 8, Body: Marker(gone.Fingerprint()) + "\nstill here", InReplyTo: 7},
			{ID: 9, Body: Marker(gone.Fingerprint()) + "\nand here", InReplyTo: 7},
		},
	}
	ops := Delta(nil, []Thread{thread}, 42, "abc")
	if len(ops.Delete) != 3 {
		t.Fatalf("every comment in the thread goes: %+v", ops.Delete)
	}
	if ops.Delete[2].Comment != 7 {
		t.Fatalf("the root must go last: %+v", ops.Delete)
	}
	if ops.Delete[2].Refused == "" {
		t.Error("the root's delete carries what to say when the platform refuses it")
	}
	if len(ops.Reply) != 0 {
		t.Errorf("a deleted thread needs no reply: %+v", ops.Reply)
	}
}

// Somebody else's words are worth more than a tidy conversation, so a thread
// they spoke in is answered and left standing.
func TestAClearedClaimRepliesWhereSomebodyElseSpoke(t *testing.T) {
	gone := claim("mutation", "a.go", 12, finding.AnchorLine)
	thread := Thread{
		Root:    Comment{ID: 7, Body: Body(gone)},
		Replies: []Comment{{ID: 8, Body: "not convinced", InReplyTo: 7}},
	}
	ops := Delta(nil, []Thread{thread}, 42, "abc")
	if len(ops.Delete) != 0 {
		t.Fatalf("their thread is not lydite's to delete: %+v", ops.Delete)
	}
	if len(ops.Reply) != 1 || ops.Reply[0].Comment != 7 {
		t.Fatalf("the thread was left saying nothing: %+v", ops.Reply)
	}
}

// An outdated thread is collapsed behind the platform's "show outdated"
// toggle, so a blocking thread becomes one the author cannot see. It is
// reopened on the line the claim is on now.
func TestAnOutdatedThreadIsMovedToTheCurrentLine(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	ops := Delta([]finding.Finding{f},
		[]Thread{{Root: Comment{ID: 7, Body: Body(f), Outdated: true}}}, 42, "abc")
	if len(ops.Delete) != 1 || ops.Delete[0].Comment != 7 {
		t.Fatalf("the outdated thread was not taken down: %+v", ops.Delete)
	}
	if len(ops.Create) != 1 || ops.Create[0].Line != 12 {
		t.Fatalf("it was not reopened where the claim is: %+v", ops.Create)
	}
}

// The same rule governs the other branch: a thread somebody spoke in stays
// where it is, outdated, and is told where the claim went.
func TestAnOutdatedThreadSomebodySpokeInStaysPut(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	thread := Thread{
		Root:    Comment{ID: 7, Body: Body(f), Outdated: true},
		Replies: []Comment{{ID: 8, Body: "why?", InReplyTo: 7}},
	}
	ops := Delta([]finding.Finding{f}, []Thread{thread}, 42, "abc")
	if len(ops.Delete) != 0 || len(ops.Create) != 0 {
		t.Fatalf("their thread was moved anyway: %+v", ops)
	}
	if len(ops.Reply) != 1 || !strings.Contains(ops.Reply[0].Body, "a.go:12") {
		t.Fatalf("the reply does not say where the claim is: %+v", ops.Reply)
	}
}

// A thread lydite never wrote carries no marker, and nothing here is entitled
// to touch it.
func TestAThreadLyditeDidNotWriteIsNeverTouched(t *testing.T) {
	ops := Delta(nil, []Thread{{Root: Comment{ID: 7, Body: "a human's review comment"}}}, 42, "abc")
	if len(ops.Delete) != 0 || len(ops.Reply) != 0 || len(ops.Create) != 0 {
		t.Fatalf("got %+v", ops)
	}
}

// The document is lydite's own wire, and the keys are what the relay and the
// CLI both read it by.
func TestTheOpsDocumentKeys(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	ops := Delta([]finding.Finding{f}, []Thread{{Root: Comment{ID: 7, Body: Marker("v1:stale")}}}, 42, "abc")
	raw, err := json.Marshal(ops)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"version", "pull_request", "head", "create", "delete"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("the document is missing %q: %s", key, raw)
		}
	}
	create, ok := doc["create"].([]any)
	if !ok || len(create) != 1 {
		t.Fatalf("expected one create: %s", raw)
	}
	entry, ok := create[0].(map[string]any)
	if !ok {
		t.Fatalf("expected an object: %s", raw)
	}
	for _, key := range []string{"fingerprint", "path", "line", "subject", "body"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("a create is missing %q: %s", key, raw)
		}
	}
}

// A file-anchored claim names the line it is really about in its prose, since
// the thread cannot sit on it.
func TestAFileAnchoredBodyNamesTheLineItCannotReach(t *testing.T) {
	body := Body(claim("crap", "internal/a.go", 400, finding.AnchorFile))
	if !strings.Contains(body, "internal/a.go:400") {
		t.Fatalf("got %q", body)
	}
	if !strings.Contains(body, "crap(cli)") {
		t.Fatalf("the body does not name the row that reported it: %q", body)
	}
	if !strings.Contains(body, "a claim") {
		t.Fatalf("the body does not say what the claim is: %q", body)
	}
}

// A document written by a lydite that recorded no row still says which gate
// spoke, which is the part a reader on the line needs.
func TestABodyWithNoRowFallsBackToTheGate(t *testing.T) {
	f := claim("crap", "a.go", 4, finding.AnchorLine)
	f.Row = ""
	if !strings.Contains(Body(f), "crap(cli)") {
		t.Fatalf("got %q", Body(f))
	}
	f.Component = ""
	if !strings.Contains(Body(f), "**crap**") {
		t.Fatalf("got %q", Body(f))
	}
}

// The platform's quote-reply copies the raw markdown of the comment it
// answers, marker and all. A marker anywhere in the body would read that as
// lydite's own comment, and the thread would be deleted with the reviewer's
// words in it.
func TestAQuotedReplyIsNotLyditeSpeaking(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	quoted := "> " + Marker(f.Fingerprint()) + "\n> **mutation(cli)** — a claim\n\nwhy?"
	if got := FingerprintIn(quoted); got != "" {
		t.Fatalf("a quoted marker carried a fingerprint: %q", got)
	}
	thread := Thread{
		Root:    Comment{ID: 7, Body: Body(f)},
		Replies: []Comment{{ID: 8, Body: quoted, InReplyTo: 7}},
	}
	if thread.Sole() {
		t.Fatal("a reviewer who quoted the thread is still in it")
	}
	if ops := Delta(nil, []Thread{thread}, 42, "abc"); len(ops.Delete) != 0 {
		t.Fatalf("their words were taken down with it: %+v", ops.Delete)
	}
}

// Nothing ever takes down a thread somebody else spoke in, so a reply lydite
// has already made must not be made again: otherwise the same sentence lands
// on every run for as long as the pull request is open.
func TestAThreadIsAnsweredOnceAndNotOnEveryRun(t *testing.T) {
	gone := claim("mutation", "a.go", 12, finding.AnchorLine)
	said := cleared(gone.Fingerprint())
	thread := Thread{
		Root: Comment{ID: 7, Body: Body(gone)},
		Replies: []Comment{
			{ID: 8, Body: "not convinced", InReplyTo: 7},
			{ID: 9, Body: said, InReplyTo: 7},
		},
	}
	if ops := Delta(nil, []Thread{thread}, 42, "abc"); len(ops.Reply) != 0 {
		t.Fatalf("lydite said it twice: %+v", ops.Reply)
	}
}

// The same rule for a thread that has come adrift: it is told once where the
// claim went, and told again only when the answer has changed.
func TestAnOutdatedThreadIsToldWhereTheClaimWentOnce(t *testing.T) {
	f := claim("mutation", "a.go", 12, finding.AnchorLine)
	thread := Thread{
		Root: Comment{ID: 7, Body: Body(f), Outdated: true},
		Replies: []Comment{
			{ID: 8, Body: "why?", InReplyTo: 7},
			{ID: 9, Body: moved(f.Fingerprint(), f), InReplyTo: 7},
		},
	}
	if ops := Delta([]finding.Finding{f}, []Thread{thread}, 42, "abc"); len(ops.Reply) != 0 {
		t.Fatalf("lydite said it twice: %+v", ops.Reply)
	}
	f.Line = 40
	ops := Delta([]finding.Finding{f}, []Thread{thread}, 42, "abc")
	if len(ops.Reply) != 1 || !strings.Contains(ops.Reply[0].Body, "a.go:40") {
		t.Fatalf("the thread was not told the claim had moved again: %+v", ops.Reply)
	}
}

// A refusal is refused for the whole thread at once, so one operation in it
// carries what to say. A body on each would answer one thread as many times
// as it has comments.
func TestOnlyTheRootsDeleteCarriesWhatToSayIfItIsRefused(t *testing.T) {
	gone := claim("mutation", "a.go", 12, finding.AnchorLine)
	thread := Thread{
		Root: Comment{ID: 7, Body: Body(gone)},
		Replies: []Comment{
			{ID: 8, Body: Marker(gone.Fingerprint()) + "\nstill here", InReplyTo: 7},
		},
	}
	ops := Delta(nil, []Thread{thread}, 42, "abc")
	if len(ops.Delete) != 2 {
		t.Fatalf("got %+v", ops.Delete)
	}
	if ops.Delete[0].Refused != "" {
		t.Errorf("a reply's delete must carry no body: %+v", ops.Delete[0])
	}
	if ops.Delete[1].Refused == "" {
		t.Errorf("the root's delete carries what to say: %+v", ops.Delete[1])
	}
}
