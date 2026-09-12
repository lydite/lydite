// Package threads turns the located claims a run made into the review threads
// that carry them, as a document a transport applies.
//
// It is the second half of the surface. The standing comment carries what is
// true of the change; a thread carries what is true of a line, and a reader
// clears one by editing the code under it. What decides which claims go where
// is the anchor a producer already decided (see internal/finding): the comment
// takes the ones that reach the change nowhere, the review takes the rest, and
// no claim appears in both.
//
// Nothing here talks to a hosting platform. Delta reads the claims and the
// threads already standing, and answers with the operations that reconcile
// them; applying those is a transport's job, and there are two of them — the
// relay, under lydite's own identity, and `lydite threads --apply` under the
// workflow's token. One delta implementation and two dumb transports is the
// whole shape: matching fingerprints and deciding what may be deleted are
// lydite's vocabulary, and a second copy of them in a Worker is a copy one
// release behind forever.
package threads

import (
	"fmt"
	"strings"

	"lydite/lydite/internal/finding"
)

// Version is the ops document's own. It is lydite's private wire between the
// command that computes a delta and whatever applies it, both of which ship
// together — so a reader that does not recognise the version refuses the whole
// document rather than applying the half it understands.
const Version = 1

// markerPrefix opens the HTML comment every thread lydite writes begins with.
//
// Matching on the marker and never on the author, for ui.Marker's reason: the
// author is whoever's token posted it, and ADR 0022 makes that change in both
// directions — a repository that installs the app hands over from
// `github-actions[bot]`, and one that removes it hands back.
const markerPrefix = "<!-- lydite:finding:"

const markerSuffix = " -->"

// Marker is the token that identifies which finding a thread is about.
//
// The token is the fingerprint itself, version prefix included, rather than a
// second identifier derived beside it: two ids for one claim is two things to
// keep in step, and the one nobody reads is the one that drifts.
func Marker(fingerprint string) string {
	return markerPrefix + fingerprint + markerSuffix
}

// FingerprintIn returns the token the marker opening a body carries, or "".
//
// It accepts any token and returns it verbatim, including one from a
// fingerprint formula this binary has never emitted. That is what makes a
// formula bump self-heal: a thread written under the old formula matches no
// current finding, so it takes the path a cleared finding takes — deleted
// where lydite is alone in it — and the new claim is posted fresh. No
// migration, and nothing to recognise a version by.
//
// The marker has to be the first thing in the body, which is the rule the
// standing comment is found by too. A marker anywhere would read a person's
// quoted reply as lydite's own: the platform's quote-reply copies the raw
// markdown of the comment being answered, HTML comment included, so a
// reviewer quoting a thread would make themselves invisible to the
// sole-participant rule and have their words deleted with it.
func FingerprintIn(body string) string {
	rest, found := strings.CutPrefix(body, markerPrefix)
	if !found {
		return ""
	}
	raw, _, found := strings.Cut(rest, markerSuffix)
	if !found {
		return ""
	}
	token := strings.TrimSpace(raw)
	if token == "" || strings.ContainsAny(token, " \t\n") {
		return ""
	}
	return token
}

// Comment is one review comment as a hosting platform reports it.
type Comment struct {
	ID   int64
	Body string
	Path string
	// Line is where the platform currently shows the comment. It is zero for
	// an outdated one, which is the same thing Outdated says and is kept
	// separate because a transport reads it from a different field.
	Line int
	// Outdated is a thread whose anchor the change has moved out from under:
	// the platform hides it behind a "Show outdated" toggle, so a thread that
	// blocks the merge becomes one the author cannot see.
	Outdated bool
	// InReplyTo is the root comment this is a reply to, or zero for a root.
	InReplyTo int64
}

// Thread is a root comment and the replies under it.
type Thread struct {
	Root    Comment
	Replies []Comment
}

// Fingerprint is the claim this thread was opened about.
func (t Thread) Fingerprint() string { return FingerprintIn(t.Root.Body) }

// Sole reports whether lydite is the only participant.
//
// Every comment lydite writes into a thread carries the marker, so a comment
// without one is somebody else's — which is what this asks, without asking who
// authored anything. The author would answer wrongly the moment the identity
// changes, and ADR 0022 makes that happen in both directions.
//
// It governs both branches of a thread's life. A cleared finding whose thread
// lydite is alone in is deleted; one somebody has spoken in is replied to and
// left standing, because deleting it would take their words with it. An
// outdated thread is moved only under the same rule, and for the same reason.
func (t Thread) Sole() bool {
	if FingerprintIn(t.Root.Body) == "" {
		return false
	}
	for _, reply := range t.Replies {
		if FingerprintIn(reply.Body) == "" {
			return false
		}
	}
	return true
}

// Threads groups a flat listing of review comments into threads, in the order
// their roots appear.
//
// A reply whose root is not in the listing is dropped rather than promoted to
// a root of its own: it is a fragment of a thread this page did not reach, and
// treating it as a root would make lydite the sole participant in a thread it
// cannot see the whole of.
func Threads(comments []Comment) []Thread {
	at := map[int64]int{}
	var out []Thread
	for _, c := range comments {
		if c.InReplyTo == 0 {
			at[c.ID] = len(out)
			out = append(out, Thread{Root: c})
		}
	}
	for _, c := range comments {
		if c.InReplyTo == 0 {
			continue
		}
		if i, ok := at[c.InReplyTo]; ok {
			out[i].Replies = append(out[i].Replies, c)
		}
	}
	return out
}

// Create opens a thread on a line, or on a file.
type Create struct {
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
	// Line is the line to anchor to, and is zero when Subject is "file".
	Line int `json:"line,omitempty"`
	// Subject is "line" or "file", which is how far the claim reaches into
	// the change. A hosting platform that anchors to a diff cannot put a
	// comment on a line the change did not touch, and pointing at a nearby
	// one it did would be the surface lying about where the problem is.
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// Reply adds to a thread that is being left standing.
type Reply struct {
	Comment int64  `json:"comment"`
	Body    string `json:"body"`
}

// Delete removes one comment lydite wrote.
//
// Refused is what to say instead when the platform will not delete it. An
// identity may only delete comments it authored, so a repository that
// installed the app after lydite had already written threads under its own
// `github-actions[bot]` has threads neither identity can clear — the handover
// corner case ADR 0031 states rather than engineers around. The body travels
// with the operation rather than being composed by whatever applies it,
// because a transport that writes its own prose is a second place lydite's
// words live.
type Delete struct {
	Comment int64  `json:"comment"`
	Refused string `json:"refused"`
}

// Ops is everything one run changes about a pull request's threads.
//
// It is lydite's own shape and a private wire: it is written to the path
// --ops names rather than into .lydite-reports/, because nothing about it is
// promised to a consumer and a command that reaches no verdict writes no
// report document — `lydite test plan` is the precedent.
type Ops struct {
	Version     int      `json:"version"`
	PullRequest int      `json:"pull_request"`
	Head        string   `json:"head"`
	Create      []Create `json:"create,omitempty"`
	Reply       []Reply  `json:"reply,omitempty"`
	Delete      []Delete `json:"delete,omitempty"`
}

// Located is the findings a review can carry: the ones that reach the change
// at a line or at a file.
//
// The partition against the standing comment needs no coordination between the
// two renderers, because it is a partition on the anchor and the anchor is
// already in the document. Everything else is AnchorNowhere, and the comment
// takes exactly that.
func Located(findings []finding.Finding) []finding.Finding {
	var out []finding.Finding
	for _, f := range findings {
		if f.Anchor == finding.AnchorLine || f.Anchor == finding.AnchorFile {
			out = append(out, f)
		}
	}
	return out
}

// Delta is the operations that reconcile the threads standing on a pull
// request with the claims this run makes.
//
// Three things happen, and one predicate governs two of them:
//
//   - A claim with no thread gets one.
//   - A thread whose claim is gone is deleted where lydite is the only
//     participant, and replied to and left standing where anyone else spoke.
//   - A thread the change has made outdated is deleted and reopened at the
//     line the claim is on now, under the same rule.
//
// An outdated thread is moved rather than left because the platform collapses
// one behind a "Show outdated" toggle: a thread that blocks the merge becomes
// a thread the author cannot see, which is worse than the notification a
// repost costs. A fixed claim leaves no per-finding record, which is accepted
// — per-finding history is a later, additive step that the fingerprint is
// what makes possible.
//
// A thread lydite did not write — no marker — is not lydite's to touch, and is
// left out of every list.
func Delta(findings []finding.Finding, threads []Thread, pull int, head string) Ops {
	ops := Ops{Version: Version, PullRequest: pull, Head: head}
	claims := map[string]finding.Finding{}
	order := make([]string, 0, len(findings))
	for _, f := range findings {
		fp := f.Fingerprint()
		if _, ok := claims[fp]; !ok {
			order = append(order, fp)
		}
		claims[fp] = f
	}

	standing := map[string]bool{}
	for _, t := range threads {
		fp := t.Fingerprint()
		if fp == "" {
			continue
		}
		f, current := claims[fp]
		switch {
		case !current:
			ops.close(t, cleared(fp))
		case !t.Root.Outdated:
			standing[fp] = true
		case t.Sole():
			// Deleted and reopened, so the loop below writes it again at
			// the line the claim is on now. The refusal body says it moved
			// rather than that it cleared: a delete the platform will not
			// do leaves this thread standing beside the new one, and the
			// claim is still true.
			ops.close(t, moved(fp, f))
		default:
			// Somebody is in this thread, so it stays where it is with
			// their words in it, outdated and all.
			standing[fp] = true
			ops.answer(t, moved(fp, f))
		}
	}

	for _, fp := range order {
		if standing[fp] {
			continue
		}
		f := claims[fp]
		create := Create{Fingerprint: fp, Path: f.Path, Subject: "file", Body: Body(f)}
		if f.Anchor == finding.AnchorLine {
			create.Subject, create.Line = "line", f.Line
		}
		ops.Create = append(ops.Create, create)
	}
	return ops
}

// close ends a thread: deleted where lydite is alone in it, and answered where
// it is not.
//
// Every reply is deleted before the root, so a refusal partway through leaves
// a thread with its root still standing rather than a headless run of replies
// the platform shows under nothing. Their order among themselves says nothing
// — what matters is that the root goes last.
//
// Only the root's delete carries what to say if the platform refuses it. A
// refusal is refused for the whole thread at once — one identity cannot delete
// another's comments — so a body on every operation would answer one thread
// as many times as it has comments, on every run for as long as it stands.
func (o *Ops) close(t Thread, body string) {
	if !t.Sole() {
		o.answer(t, body)
		return
	}
	for _, reply := range t.Replies {
		o.Delete = append(o.Delete, Delete{Comment: reply.ID})
	}
	o.Delete = append(o.Delete, Delete{Comment: t.Root.ID, Refused: body})
}

// answer says something in a thread that is being left standing, once.
//
// A thread lydite has already said this in is left alone. Nothing takes such a
// thread down — it is somebody else's conversation, and lydite cannot resolve
// one — so without this the same sentence would be posted on every run for as
// long as the pull request is open, which is the notification churn the
// fingerprint exists to prevent.
func (o *Ops) answer(t Thread, body string) {
	for _, reply := range t.Replies {
		if reply.Body == body {
			return
		}
	}
	o.Reply = append(o.Reply, Reply{Comment: t.Root.ID, Body: body})
}

// messageRunes bounds the claim line, for the reason detailBytes bounds the
// quote: the text is a tool's rendering of somebody else's source, and a
// comment a platform refuses is no surface at all.
const messageRunes = 500

// claimText neutralises one line of a scanned repository's own text for a
// comment lydite posts under its own App identity.
//
// The claim line is prose rather than a quote, so it cannot be fenced the way
// writeDetail fences the detail beneath it — and it carries the same untrusted
// content. A tool reproduces author-written strings verbatim: clippy renders a
// `compile_error!` or a `#[deprecated(note = …)]`, and a component's label is
// whatever `.lydite/components.yml` says. Left raw, a pull-request author
// writes HTML or Markdown into a merge-gating thread — an image beacon, a link,
// or a fabricated line claiming the run passed.
//
// Four things are taken away and nothing else. HTML is escaped, because a
// hosting platform renders it and that is what an image beacon needs. Markdown's
// link and image syntax is escaped, because it reaches the network with no HTML
// at all. Newlines collapse to spaces, because Markdown's block constructs — a
// heading, a list, a table, a rule — all need the start of a line, and a claim
// is one line by construction. The result is bounded. Inline emphasis survives
// and is left alone: it renders as decoration inside a sentence a reader can
// see, and it cannot forge structure or reach the network.
//
// The standing comment needs none of this because it already fences what it
// renders — CommentDetail.render quotes every claim line — so this is the same
// rule reaching the same content on the other surface.
func claimText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	// The backslash first, and the order is the whole of why this works: the
	// escape below is a backslash, so text arriving with one already in it
	// would otherwise turn `\[` into `\\[` — which CommonMark reads as an
	// escaped backslash followed by a live bracket, handing back the syntax
	// this is removing. A scanned repository does not get to pre-escape
	// lydite's escape.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	// Markdown's link and image syntax reaches the network without any HTML:
	// `[text](url)` is a live hyperlink and `![alt](url)` fetches a remote
	// image the moment the comment renders. Escaping the three characters that
	// open them is enough, and leaves the text readable.
	for _, c := range []string{"[", "]", "!"} {
		s = strings.ReplaceAll(s, c, "\\"+c)
	}
	runes := []rune(s)
	if len(runes) > messageRunes {
		return string(runes[:messageRunes]) + "…"
	}
	return s
}

// detailLines and detailBytes bound what a repository lydite does not own can
// put into a comment lydite posts under its own identity.
//
// A hosting platform refuses a comment over a size limit — GitHub's is 65536
// bytes — and a refused comment is no surface at all. The quoted text is a
// tool's own rendering of somebody else's source: clippy's diagnostic, gosec's
// excerpt, cargo-deny's dependency graph, none of them bounded by anything at
// their end. It is the rule finding.Source already follows for a Site, and the
// standing comment for its quoted output.
const (
	detailLines = 40
	detailBytes = 4000
)

// fence opens and closes the quoted block, and fenceEscape is what a line
// holding one is rewritten to.
const (
	fence       = "```"
	fenceEscape = "'''"
)

// writeDetail quotes a finding's detail under its claim.
//
// Fenced, and the fence is what makes this safe to render at all: the lines
// are source from a repository lydite does not own, and source contains
// anything source contains — a line shaped like a heading, a table row, an
// HTML comment, or lydite's own marker. Unfenced, a pull-request author writes
// Markdown into a merge-gating thread posted under lydite's App identity.
// ui.CommentDetail.render states the same rule for the standing comment, and
// this is the same content reaching the other surface.
//
// The fence itself is rewritten rather than escaped, for the reason it is
// there: a line holding one would otherwise close the block and hand the rest
// of the quote back to the Markdown renderer.
func writeDetail(b *strings.Builder, detail []string) {
	if len(detail) == 0 {
		return
	}
	b.WriteString("\n\n" + fence + "\n")
	written, truncated := 0, false
	for i, line := range detail {
		if i >= detailLines || written+len(line)+1 > detailBytes {
			truncated = true
			break
		}
		safe := strings.ReplaceAll(line, fence, fenceEscape)
		b.WriteString(safe)
		b.WriteByte('\n')
		written += len(safe) + 1
	}
	b.WriteString(fence)
	if truncated {
		// Said rather than silent: a reader has to know the quote stops short
		// of what the tool reported, or they act on a call path that appears
		// to end where it does not.
		b.WriteString("\n\n_Quoted output truncated. The run's log holds all of it._")
	}
}

// Body is what a thread's root comment says.
//
// The marker first, so the line a transport checks is the first one, and the
// row's label after it: a reader arriving at a line wants to know which gate
// is talking before they read what it says.
func Body(f finding.Finding) string {
	var b strings.Builder
	b.WriteString(Marker(f.Fingerprint()))
	b.WriteString("\n**")
	b.WriteString(claimText(label(f)))
	b.WriteString("** — ")
	b.WriteString(claimText(f.Message))
	writeDetail(&b, f.Detail)
	if f.Anchor == finding.AnchorFile {
		fmt.Fprintf(&b, "\n\n_%s:%d — this change does not touch that line, so the thread sits on the file._",
			claimText(f.Path), f.Line)
	}
	return b.String()
}

// cleared is what lydite says in a thread it is finishing with.
//
// It carries the marker, so a thread lydite has spoken in twice is still one
// lydite is alone in — the predicate reads the marker and not the author, and
// a reply without one would make lydite a stranger in its own thread.
func cleared(fingerprint string) string {
	return Marker(fingerprint) + "\nThis no longer reports on the change: either the code cleared it, or the gate that made it did not run.\n\n" +
		"lydite cannot resolve a thread — that needs a permission it deliberately does not hold — so this one is yours to close."
}

// moved says a thread has come adrift from the line it is about.
//
// It is what a thread gets instead of being reopened when somebody else is in
// it: their words are worth more than the anchor, and deleting the thread to
// move it would take them with it.
func moved(fingerprint string, f finding.Finding) string {
	// The path through claimText, not a code span: a filename is the scanned
	// repository's own text, and a backtick in one closes the span and hands
	// the rest of the sentence back to the renderer.
	return Marker(fingerprint) + fmt.Sprintf(
		"\nStill reported, and the change has moved it: it is at %s:%d now. This thread stays here because it is not lydite's alone to move.",
		claimText(f.Path), f.Line)
}

// label names the row a claim was made by, falling back to the gate.
//
// A document written by a lydite that did not record the row still has the
// gate, which is the part a reader needs; the label is the fuller answer and
// not the only one.
func label(f finding.Finding) string {
	if f.Row != "" {
		return f.Row
	}
	if f.Component != "" {
		return f.Gate + "(" + f.Component + ")"
	}
	return f.Gate
}
