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

func TestBodyFencesAFindingsDetail(t *testing.T) {
	// The lines are source from a repository lydite does not own, quoted into
	// a merge-gating comment posted under lydite's own App identity. Unfenced,
	// a pull-request author writes Markdown into it.
	body := Body(finding.Finding{
		Gate: "gosec", Path: "main.go", Line: 3, Message: "a claim",
		Detail: []string{"## not a heading", "| not | a table |"},
	})
	if !strings.Contains(body, "\n```\n## not a heading\n") {
		t.Errorf("body = %q, want the detail inside a fence", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "```") {
		t.Errorf("body = %q, want the fence closed", body)
	}
}

func TestBodyRewritesAFenceInsideTheQuotedSource(t *testing.T) {
	// A line holding a fence would otherwise close the block and hand the
	// rest of the quote back to the Markdown renderer — which is the whole of
	// what the fence was protecting against.
	body := Body(finding.Finding{
		Gate: "cargo clippy", Path: "src/lib.rs", Line: 1, Message: "a claim",
		Detail: []string{"``` then **bold**"},
	})
	if strings.Contains(body, "``` then") {
		t.Errorf("body = %q, want the inner fence rewritten", body)
	}
	if !strings.Contains(body, "''' then **bold**") {
		t.Errorf("body = %q, want the line kept with its fence rewritten", body)
	}
}

func TestBodyBoundsWhatAScannedRepositoryCanQuoteIntoAComment(t *testing.T) {
	// A platform refuses a comment over its size limit, and a refused comment
	// is no surface at all. Nothing bounds a tool's own rendering at its end.
	long := make([]string, 500)
	for i := range long {
		long[i] = strings.Repeat("x", 200)
	}
	body := Body(finding.Finding{
		Gate: "gosec", Path: "main.go", Line: 1, Message: "a claim", Detail: long,
	})
	if len(body) > detailBytes+1000 {
		t.Errorf("body is %d bytes, want it bounded near %d", len(body), detailBytes)
	}
	if !strings.Contains(body, "truncated") {
		t.Error("the quote stops short of what the tool reported and does not say so")
	}
}

func TestBodyQuotesNothingForAFindingWithNoDetail(t *testing.T) {
	// An empty fence is a black box under a claim that had nothing to add.
	body := Body(finding.Finding{Gate: "crap", Path: "a.go", Line: 1, Message: "a claim"})
	if strings.Contains(body, "```") {
		t.Errorf("body = %q, want no fence at all", body)
	}
}

func TestBodyNeutralisesTheClaimLine(t *testing.T) {
	// A tool reproduces author-written strings verbatim — clippy renders a
	// compile_error! — so the claim line carries the scanned repository's own
	// text into a merge-gating comment posted under lydite's App identity.
	// The line is prose and cannot be fenced the way the quote beneath it is.
	body := Body(finding.Finding{
		Gate: "cargo clippy", Path: "src/lib.rs", Line: 1,
		Message: `<img src=x onerror=alert(1)> & <b>bold</b>`,
	})
	if strings.Contains(body, "<img") || strings.Contains(body, "<b>") {
		t.Errorf("body = %q, want the HTML escaped — a platform renders it, which is what a beacon needs", body)
	}
	if !strings.Contains(body, "&lt;img") || !strings.Contains(body, "&amp;") {
		t.Errorf("body = %q, want the text kept with its HTML escaped", body)
	}
}

func TestBodyKeepsAForgedClaimOnOneLine(t *testing.T) {
	// Markdown's block constructs all need the start of a line, so a claim
	// that stays on one cannot forge a heading, a table, a rule — or a line
	// saying the run passed.
	body := Body(finding.Finding{
		Gate: "cargo clippy", Path: "src/lib.rs", Line: 1,
		Message: "a claim\n\n## All checks passed\n\n| x | y |",
	})
	claim := strings.SplitN(body, "\n", 2)[1]
	if strings.Contains(strings.SplitN(claim, "\n", 2)[0], "\n") {
		t.Fatal("the claim line was split")
	}
	if strings.Contains(body, "\n## All checks passed") {
		t.Errorf("body = %q, want no forged heading at the start of a line", body)
	}
}

func TestBodyBoundsTheClaimLine(t *testing.T) {
	body := Body(finding.Finding{
		Gate: "cargo clippy", Path: "src/lib.rs", Line: 1,
		Message: strings.Repeat("x", 5000),
	})
	if len(body) > messageRunes+500 {
		t.Errorf("body is %d bytes, want the claim bounded near %d", len(body), messageRunes)
	}
}

func TestBodyNeutralisesMarkdownThatReachesTheNetwork(t *testing.T) {
	// A link and an image need no HTML at all: `[t](url)` is a live hyperlink
	// and `![a](url)` fetches a remote image the moment the comment renders.
	// The text is the scanned repository's — clippy reproduces a
	// `#[deprecated(note = …)]` string into its diagnostic.
	body := Body(finding.Finding{
		Gate: "cargo clippy", Path: "src/lib.rs", Line: 1,
		Message: "![beacon](https://example.invalid/x.png) [click](https://example.invalid)",
	})
	if strings.Contains(body, "![beacon](") || strings.Contains(body, "[click](") {
		t.Errorf("body = %q, want the link and image syntax escaped", body)
	}

}

func TestBodyNeutralisesAPathTheScannedRepositoryNamed(t *testing.T) {
	// A filename is the scanned repository's text too, and reaches the same
	// comment — in the file-anchored suffix and in the moved notice.
	f := finding.Finding{
		Gate: "gosec", Path: "![beacon](https://example.invalid/x.png).go", Line: 3,
		Message: "a claim", Anchor: finding.AnchorFile,
	}
	if body := Body(f); strings.Contains(body, "![beacon](") {
		t.Errorf("Body = %q, want the path neutralised", body)
	}
	if note := moved("v1:abc", f); strings.Contains(note, "![beacon](") {
		t.Errorf("moved = %q, want the path neutralised", note)
	}
}

func TestClaimTextTakesAwayWhatRendersOrFetches(t *testing.T) {
	backslash := "\\"
	cases := []struct {
		name, in, want string
	}{
		{"plain text is untouched", "a claim about md5", "a claim about md5"},
		{"HTML is escaped", "<img src=x> & <b>", "&lt;img src=x&gt; &amp; &lt;b&gt;"},
		{
			"a link is escaped",
			"[click](http://example.invalid)",
			backslash + "[click" + backslash + "](http://example.invalid)",
		},
		{
			"an image is escaped",
			"![a](http://example.invalid)",
			backslash + "!" + backslash + "[a" + backslash + "](http://example.invalid)",
		},
		{
			// Text arriving with a backslash already in it must not be able to
			// pre-escape lydite's escape: escaping the bracket first would turn
			// `\[` into `\\[`, which CommonMark reads as an escaped backslash
			// followed by a live bracket.
			"a pre-escaped link cannot reconstitute itself",
			backslash + "[click" + backslash + "](http://example.invalid)",
			backslash + backslash + backslash + "[click" +
				backslash + backslash + backslash + "](http://example.invalid)",
		},
		{"newlines collapse, so no block construct can start a line", "a\n\n## forged", "a ## forged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimText(tc.in); got != tc.want {
				t.Errorf("claimText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClaimTextClipsAtExactlyTheCap(t *testing.T) {
	// A message at exactly the cap is kept whole; one rune over is clipped.
	// The boundary is the whole of what the cap means, so it is stated rather
	// than approached.
	at := claimText(strings.Repeat("x", messageRunes))
	if len([]rune(at)) != messageRunes || strings.HasSuffix(at, "…") {
		t.Errorf("a message at the cap was clipped: %d runes", len([]rune(at)))
	}
	over := claimText(strings.Repeat("x", messageRunes+1))
	if !strings.HasSuffix(over, "…") {
		t.Error("a message one rune over the cap was not clipped")
	}
	if len([]rune(over)) != messageRunes+1 {
		t.Errorf("clipped to %d runes, want the cap plus the ellipsis", len([]rune(over)))
	}
}

func TestWriteDetailKeepsExactlyTheLineCap(t *testing.T) {
	// detailLines lines are kept whole; the next one is what starts the
	// count. A boundary off by one either drops a line nobody was told about
	// or says "1 more" when there is none.
	exact := make([]string, detailLines)
	for i := range exact {
		exact[i] = "x"
	}
	var b strings.Builder
	writeDetail(&b, exact)
	if strings.Contains(b.String(), "truncated") {
		t.Error("a detail at exactly the line cap was truncated")
	}
	if got := strings.Count(b.String(), "\nx"); got != detailLines {
		t.Errorf("kept %d lines, want %d", got, detailLines)
	}

	var over strings.Builder
	writeDetail(&over, append(append([]string(nil), exact...), "x"))
	if !strings.Contains(over.String(), "truncated") {
		t.Error("a detail one line over the cap was not truncated")
	}
}

func TestWriteDetailCountsTheNewlineItWrites(t *testing.T) {
	// The running total counts each line plus the newline written after it.
	// A line filling the cap exactly therefore does not fit, because writing
	// it costs one byte more than the line itself — and a total that counted
	// the line alone would let the quote run past the cap by one byte per
	// line, which is what the cap exists to prevent.
	var b strings.Builder
	writeDetail(&b, []string{strings.Repeat("x", detailBytes)})
	if !strings.Contains(b.String(), "truncated") {
		t.Error("a line filling the cap was quoted whole, so its newline was not counted")
	}
	if strings.Contains(b.String(), strings.Repeat("x", detailBytes)) {
		t.Error("the line that did not fit was written anyway")
	}

	// One byte under, it fits with its newline exactly.
	var fits strings.Builder
	writeDetail(&fits, []string{strings.Repeat("x", detailBytes-1)})
	if strings.Contains(fits.String(), "truncated") {
		t.Error("a line that fits with its newline was truncated")
	}

	// And the running total is what holds the whole quote inside the cap. A
	// total that counted less per line than was written would pass the check
	// on each line and still overrun, which is the one thing the cap exists
	// to stop.
	var many strings.Builder
	lines := make([]string, detailLines+5)
	for i := range lines {
		lines[i] = strings.Repeat("x", 100)
	}
	writeDetail(&many, lines)
	quoted := many.String()
	body, _, _ := strings.Cut(strings.TrimPrefix(quoted, "\n\n"+fence+"\n"), fence)
	if len(body) > detailBytes {
		t.Errorf("the quote ran to %d bytes, want it held inside %d", len(body), detailBytes)
	}
}
