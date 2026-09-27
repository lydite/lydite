package clearancestages

import (
	"context"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/pathmatch"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

func TestDescribeClearanceNamesWhoClearedWhichRevision(t *testing.T) {
	out, err := DescribeClearance(context.Background(), DescribeClearanceIn{
		Login: "octocat", Head: head, Version: "v1.2.3",
	})
	if err != nil {
		t.Fatalf("DescribeClearance: %v", err)
	}
	if want := "cleared by @octocat at 4c2eaea1f2b3"; out.Description != want {
		t.Errorf("Description = %q, want %q", out.Description, want)
	}
	want := ui.Comment{Verdict: ui.VerdictPass, Headline: out.Description, Version: "v1.2.3", Base: "4c2eaea1f2b3"}.Render()
	if out.Body != want {
		t.Errorf("Body =\n%s\nwant\n%s", out.Body, want)
	}
}

// The fingerprint rides on the description, where clearance.FingerprintIn
// reads it back from.
func TestDescribeClearanceCarriesTheFingerprint(t *testing.T) {
	out, err := DescribeClearance(context.Background(), DescribeClearanceIn{
		Login: "octocat", Head: head, Fingerprint: "abc123",
	})
	if err != nil {
		t.Fatalf("DescribeClearance: %v", err)
	}
	if got, ok := clearance.FingerprintIn(out.Description); !ok || got != "abc123" {
		t.Errorf("the description %q carries fingerprint %q (%t), want abc123", out.Description, got, ok)
	}
	if !strings.Contains(out.Body, out.Description) {
		t.Errorf("the reply does not carry the description:\n%s", out.Body)
	}
}

func TestComposeReplyExplainsTheStandingVerdict(t *testing.T) {
	for _, c := range []struct {
		name     string
		status   *clearance.Status
		verdict  ui.Verdict
		headline string
	}{
		{"none published", nil, ui.VerdictRefer, "no verdict has been published for this revision yet"},
		{"referred", &clearance.Status{State: clearance.StatePending, Description: "referred — comment /lydite clear"},
			ui.VerdictRefer, "referred — comment /lydite clear"},
		{"exempt", &clearance.Status{State: clearance.StateSuccess, Description: "exempt: docs"},
			ui.VerdictPass, "exempt: docs"},
		{"failing", &clearance.Status{State: clearance.StateFailure, Description: "not isolated"},
			ui.VerdictFail, "not isolated"},
		{"errored", &clearance.Status{State: clearance.StateError, Description: "no verdict reached"},
			ui.VerdictRefer, "no verdict reached"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := ComposeReply(context.Background(), ComposeReplyIn{
				Action: clearance.Action{Kind: clearance.KindExplain, SHA: head},
				Login:  "octocat", Head: head, Status: c.status, Version: "v1.2.3",
			})
			if err != nil {
				t.Fatalf("ComposeReply: %v", err)
			}
			want := ui.Comment{Verdict: c.verdict, Headline: c.headline, Version: "v1.2.3", Base: "4c2eaea1f2b3"}.Render()
			if out.Body != want || out.Headline != c.headline {
				t.Errorf("reply = %q / %q, want %q / %q", out.Headline, out.Body, c.headline, want)
			}
		})
	}
}

// Every refusal names its reason and a way forward, as a referral reply.
func TestComposeReplyRefusesWithTheReasonAndAWayForward(t *testing.T) {
	for _, c := range []struct {
		reason clearance.Reason
		want   string
	}{
		{clearance.ReasonNotPermitted, "@passer-by does not have write access to this repository, so this changes nothing"},
		{clearance.ReasonUnknownVerb, "unknown command — this surface has `/lydite clear`, `/lydite explain` and `/lydite exempt <shape>`, " +
			"where a shape is up to 64 letters, digits, dots, dashes and underscores"},
		{clearance.ReasonStaleSHA, "that revision is not the current head (4c2eaea1f2b3), so nothing was cleared"},
		{clearance.ReasonNoStatus, "no verdict has been published for 4c2eaea1f2b3 yet — there is nothing to clear"},
		{clearance.ReasonHeadMoved, "the head moved after this comment was written; re-issue `/lydite clear 4c2eaea1f2b3` to clear what is there now"},
		{clearance.ReasonNotReferred, "this is a failing gate, not a referral — it is cleared by splitting the change, not by a comment"},
		{clearance.ReasonAlreadyPassing, "this change already merges unattended; there is no referral to clear"},
		{clearance.ReasonNothingToPropose, "every path this change touches is already covered by a declared exemption, " +
			"and it is still referred — so there is no entry to propose. The standing verdict comment " +
			"says what is holding it; widening `.lydite/exemptions.yml` is not it"},
		{clearance.ReasonCouldNotDerive, "lydite could not work out which paths this change touches, so there is no entry to " +
			"propose — try again, and if it keeps happening the clearance job's log says what failed"},
		{clearance.Reason("something-else"), "nothing to do"},
	} {
		t.Run(string(c.reason), func(t *testing.T) {
			out, err := ComposeReply(context.Background(), ComposeReplyIn{
				Action: clearance.Action{Kind: clearance.KindRefuse, Reason: c.reason},
				Login:  "passer-by", Head: head, Version: "v1.2.3",
			})
			if err != nil {
				t.Fatalf("ComposeReply: %v", err)
			}
			want := ui.Comment{Verdict: ui.VerdictRefer, Headline: c.want, Version: "v1.2.3", Base: "4c2eaea1f2b3"}.Render()
			if out.Headline != c.want || out.Body != want {
				t.Errorf("reply = %q / %q, want %q / %q", out.Headline, out.Body, c.want, want)
			}
		})
	}
}

// A clearance's reply carries its fingerprint and is DescribeClearance's to
// compose; a comment nothing addressed has no reply at all.
func TestComposeReplyRefusesADecisionItDoesNotAnswer(t *testing.T) {
	for _, kind := range []clearance.Kind{clearance.KindClear, clearance.KindIgnore} {
		if _, err := ComposeReply(context.Background(), ComposeReplyIn{
			Action: clearance.Action{Kind: kind, SHA: head}, Head: head,
		}); err == nil {
			t.Errorf("a decision of kind %d was answered by ComposeReply", kind)
		}
	}
}

// proposed composes the reply to a proposal over paths.
func proposed(t *testing.T, name string, paths ...string) ComposeReplyOut {
	t.Helper()
	out, err := ComposeReply(context.Background(), ComposeReplyIn{
		Action: clearance.Action{Kind: clearance.KindExempt, SHA: head, Name: name, Paths: paths},
		Login:  "octocat", Head: head, Version: "v1.2.3",
	})
	if err != nil {
		t.Fatalf("ComposeReply: %v", err)
	}
	return out
}

// fencedBlock returns the one fenced block of a rendered comment, which is
// the YAML a reader copies out of it.
func fencedBlock(t *testing.T, body string) string {
	t.Helper()
	parts := strings.Split(body, "```")
	if len(parts) < 3 {
		t.Fatalf("the comment carries no fenced block:\n%s", body)
	}
	return strings.TrimPrefix(parts[1], "\n")
}

// The proposal answers with the draft block under a headline saying nothing
// has changed, and the commenter contributes the name alone.
func TestComposeReplyProposesTheEntry(t *testing.T) {
	out := proposed(t, "moved-sources", "src/new.go", "src/old.go")

	for _, want := range []string{"exemptions:", "- name: moved-sources", referral.ReasonPlaceholderMarker,
		"      - src/new.go", "      - src/old.go"} {
		if !strings.Contains(out.Body, want) {
			t.Errorf("the proposal is missing %q:\n%s", want, out.Body)
		}
	}
	wantHeadline := "a draft entry covering the 2 path(s) no declared exemption covers. " +
		"Nothing has changed and nobody has reviewed this: the referral stands until an entry " +
		"like it is merged into `.lydite/exemptions.yml` on the default branch, and the reason has to be answered " +
		"before it will parse."
	if out.Headline != wantHeadline {
		t.Errorf("Headline = %q, want %q", out.Headline, wantHeadline)
	}
	lines, err := proposalYAML("moved-sources", []string{"src/new.go", "src/old.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := ui.Comment{
		Verdict:  ui.VerdictRefer,
		Headline: wantHeadline,
		Sections: []ui.CommentSection{{
			Status:  ui.StatusRefer,
			Title:   "proposed exemption",
			Summary: "moved-sources",
			Details: []ui.CommentDetail{{Lines: lines}},
		}},
		Version: "v1.2.3",
		Base:    "4c2eaea1f2b3",
	}.Render()
	if out.Body != want {
		t.Errorf("Body =\n%s\nwant\n%s", out.Body, want)
	}
}

// The block a reader pastes is indented the two spaces `.lydite/exemptions.yml`
// is written in. yaml.v3 indents four unless told otherwise, and a block that
// has to be re-indented before it lands is one a reader edits by hand — which
// is exactly what the encoder is here to spare them.
func TestTheProposedBlockIsIndentedTwoSpacesPerLevel(t *testing.T) {
	lines, err := proposalYAML("docs-only", []string{"docs/one.md"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exemptions:", "  - name: docs-only", "    paths:", "      - docs/one.md"} {
		if !slices.Contains(lines, want) {
			t.Errorf("the block carries no %q line:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// The generated entry is a draft and not an exemption: landed unedited it
// fails the parse every route to the file passes through.
func TestTheProposedEntryDoesNotParseAsAnExemption(t *testing.T) {
	block := fencedBlock(t, proposed(t, "docs-only", "docs/one.md").Body)
	if _, err := referral.Parse([]byte(block), referral.FileName); err == nil {
		t.Fatal("the proposal parsed as a live exemption, so pasting it unedited would declare one")
	} else if !strings.Contains(err.Error(), "placeholder marker") {
		t.Errorf("the entry was rejected for something other than its unanswered reason: %v", err)
	}
}

// assertCoversOnly reads a proposed entry's path back as the pattern it will
// be in .lydite/exemptions.yml, and holds it to covering the one file it was
// derived from.
func assertCoversOnly(t *testing.T, pattern, literal, block string) {
	t.Helper()
	if err := pathmatch.ValidatePattern(pattern); err != nil {
		t.Errorf("the proposed path is not a pattern the file accepts: %v\n%s", err, block)
	}
	if !pathmatch.Match(pattern, literal) {
		t.Errorf("%q does not cover %q, the path it was derived from:\n%s", pattern, literal, block)
	}
}

// A name and a path are scalars the encoder quotes, not text spliced into a
// line: one carrying YAML syntax must not be able to close the entry and add
// a second one, with a reason that answers itself, to something lydite posts
// under its own identity.
func TestAProposalsScalarsCannotReshapeTheDocument(t *testing.T) {
	literals := []string{
		"src/\"a\": #x\n  - name: forged\n    reason: this one is fine\n    paths: [\"**\"]",
		"*anchor",
	}
	block := fencedBlock(t, proposed(t, "docs-only", literals...).Body)

	var file referral.File
	if err := yaml.Unmarshal([]byte(block), &file); err != nil {
		t.Fatalf("the proposal is not a document at all: %v\n%s", err, block)
	}
	if len(file.Exemptions) != 1 {
		t.Fatalf("the proposal carries %d entries, want the one it proposed:\n%s", len(file.Exemptions), block)
	}
	if got := file.Exemptions[0]; got.Name != "docs-only" || len(got.Paths) != 2 {
		t.Fatalf("the entry is not the one proposed: %+v\n%s", got, block)
	}
	// A path is a filename on the way in and a pattern on the way out, so
	// every entry has to come back matching the file it was derived from.
	for i, literal := range literals {
		assertCoversOnly(t, file.Exemptions[0].Paths[i], literal, block)
	}
	// Unescaped, "*anchor" is the pattern covering every name ending in
	// "anchor" — the widening an entry read as lydite's own output invites
	// nobody to re-derive.
	if pattern := file.Exemptions[0].Paths[1]; pathmatch.Match(pattern, "someone-elses-anchor") {
		t.Errorf("%q covers a file the change never touched:\n%s", pattern, block)
	}
	// Whatever the scalars carry, the reason is still the unanswered one, so
	// the block remains unlandable by the check every route to the file
	// passes through.
	if _, err := referral.Parse([]byte(block), referral.FileName); err == nil ||
		!strings.Contains(err.Error(), "placeholder marker") {
		t.Errorf("the proposal was rejected for something other than its unanswered reason: %v", err)
	}
}

// onlyProposedPath returns the single path of a proposal carrying one entry.
func onlyProposedPath(t *testing.T, block string) string {
	t.Helper()
	var file referral.File
	if err := yaml.Unmarshal([]byte(block), &file); err != nil {
		t.Fatalf("the proposal is not a document at all: %v\n%s", err, block)
	}
	if len(file.Exemptions) != 1 || len(file.Exemptions[0].Paths) != 1 {
		t.Fatalf("the proposal is not the one entry over one path it proposed:\n%s", block)
	}
	return file.Exemptions[0].Paths[0]
}

// A changed file's name is not a pattern. Proposed verbatim, a Next.js route
// segment is a character class covering four one-letter names and missing the
// file that produced it.
func TestAProposedPathWithACharacterClassCoversOnlyThatFile(t *testing.T) {
	block := fencedBlock(t, proposed(t, "routes", "app/[slug]/page.tsx").Body)
	pattern := onlyProposedPath(t, block)
	assertCoversOnly(t, pattern, "app/[slug]/page.tsx", block)
	for _, other := range []string{"app/s/page.tsx", "app/l/page.tsx", "app/u/page.tsx", "app/g/page.tsx"} {
		if pathmatch.Match(pattern, other) {
			t.Errorf("%q covers %q, which the change never touched:\n%s", pattern, other, block)
		}
	}
}

// A file named "**" is an edge case git permits, and the one path whose
// verbatim proposal would exempt the whole repository while reading as the
// narrow entry the change asked for.
func TestAProposedPathOfLiteralStarsCoversOnlyThatFile(t *testing.T) {
	block := fencedBlock(t, proposed(t, "stars", "**").Body)
	pattern := onlyProposedPath(t, block)
	assertCoversOnly(t, pattern, "**", block)
	for _, other := range []string{"src/a.go", ".github/workflows/lydite-pr.yml", "x"} {
		if pathmatch.Match(pattern, other) {
			t.Errorf("%q covers %q, so the entry exempts the repository:\n%s", pattern, other, block)
		}
	}
}

// Every rune pathmatch reads as syntax is escaped, the backslash included, and
// nothing else is touched.
func TestEscapeGlobEscapesExactlyThePatternSyntax(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"src/a.go", "src/a.go"},
		{"app/[slug]/page.tsx", `app/\[slug\]/page.tsx`},
		{"**", `\*\*`},
		{"a?b", `a\?b`},
		{`back\slash`, `back\\slash`},
		{"", ""},
	} {
		if got := escapeGlob(c.in); got != c.want {
			t.Errorf("escapeGlob(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
