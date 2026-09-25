package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"lydite/lydite/internal/clearance"
	"lydite/lydite/internal/executil"
	"lydite/lydite/internal/forge"
	"lydite/lydite/internal/referral"
	"lydite/lydite/internal/ui"
)

func newClearanceCmd() *cobra.Command {
	var dir string
	var eventPath string
	var statusOut string
	var base string
	var baseBranch string
	var surfacesPath string
	var noColor bool
	cmd := &cobra.Command{
		Use:           "clearance",
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Answer a lydite command posted on a pull request",
		Long: `Answer a lydite command posted on a pull request.

A referral is resolved by a person, not by pushing more code, and this is
where they say so. ` + "`/lydite clear`" + ` resolves the referral standing on the
pull request's current head; ` + "`/lydite explain`" + ` restates it;
` + "`/lydite exempt <shape>`" + ` answers with a draft exemptions-file entry
covering the change's own uncovered paths, and lands nothing.

A clearance names one revision. Any push produces a new head carrying no
verdict, so the clearance does not travel with the branch.

The input is the webhook payload the platform delivers, which a workflow
writes to the path in GITHUB_EVENT_PATH.

A clearance records two statuses on the head by either route: ` + clearance.ClearanceContext + `,
which says who cleared the revision, and ` + clearance.Context + ` resolved to success,
which is the gate a merge waits on.

The ` + clearance.ClearanceContext + ` description carries the fingerprint of the decision
that was cleared, which is what lets ` + "`clearance queue`" + ` carry the clearance onto a
merge-queue entry the same decision still holds for. Computing it reads the
checkout this runs against, which has to be the revision being cleared; a
clearance given anywhere else records no fingerprint and says so, and the
queue entry goes back to a person.

--surfaces reads a comparison ` + "`review compare`" + ` already made instead of running it
here. A component's own comparison executes its own code — a Rust crate's
build.rs, a proc-macro — so the job that records the clearance with a
credential should not also be the job that ran it: compute in one job with
none, clear and record in another that never runs the change's own code.

--status-out <file> renders them as documents instead of posting them, for a
step that posts them: the ` + clearance.ClearanceContext + ` status at <file>, and the
` + clearance.Context + ` status at the sibling <file> with .referral before its
extension. Posting here is the path for a repository that has not adopted the
reusable workflows. The two routes are alternatives, not a ladder. A comment
that clears nothing writes no document.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runClearance(cmd.Context(), cmd, clearanceOptions{
				dir:        dir,
				eventPath:  eventPath,
				statusOut:  statusOut,
				base:       base,
				baseBranch: baseBranch,
				surfaces:   surfacesPath,
				noColor:    noColor,
			})
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "root directory whose "+referral.FileName+" applies")
	cmd.Flags().StringVar(&eventPath, "event", "", "webhook payload to answer (defaults to GITHUB_EVENT_PATH)")
	// The base the cleared decision is recomputed against, the same two flags
	// review resolves its own with: a fingerprint is taken over a decision, and
	// a decision is taken over a diff, which needs the revision that diff is
	// read against.
	cmd.Flags().StringVar(&base, "base", "auto",
		`commit the cleared decision is recomputed against ("auto" resolves the merge-base with the base branch)`)
	cmd.Flags().StringVar(&baseBranch, "base-branch", "", baseBranchUsage)
	cmd.Flags().StringVar(&surfacesPath, "surfaces", "",
		"read a comparison 'review compare' already made instead of running it here, and fingerprint the decision it feeds")
	cmd.Flags().StringVar(&statusOut, "status-out", "",
		"render the "+clearance.ClearanceContext+" status as a JSON document at this path, and the "+clearance.Context+
			" status it resolves at the .referral sibling, for another step to post instead of posting them here")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "drop colour; glyphs are kept")
	// The queue path answers a merge_group event rather than a comment, and it
	// belongs here because what it decides is a clearance's reach: a merge
	// queue's own revision is one nobody can be asked to clear, so the question
	// is whether the clearance the pull request already holds still applies.
	cmd.AddCommand(newQueueCmd())
	return cmd
}

// clearanceOptions is what runClearance was asked for.
type clearanceOptions struct {
	dir        string
	eventPath  string
	statusOut  string
	base       string
	baseBranch string
	// surfaces names the comparison document a computing job already wrote,
	// and is empty for a run that makes the comparison itself.
	surfaces string
	noColor  bool
}

func runClearance(ctx context.Context, cmd *cobra.Command, opt clearanceOptions) error {
	report := ui.NewReport("clearance")

	eventPath := firstNonEmpty(opt.eventPath, os.Getenv("GITHUB_EVENT_PATH"))
	if eventPath == "" {
		return fmt.Errorf("clearance needs an event payload: pass --event, or run where GITHUB_EVENT_PATH is set")
	}
	// Resolved once, so the payload a break declaration is read out of is the
	// same one this command answers rather than whatever the environment says
	// a moment later.
	opt.eventPath = eventPath
	event, err := forge.LoadCommentEvent(eventPath)
	if err != nil {
		return err
	}

	// A comment on a plain issue names no revision, so there is nothing a
	// clearance could apply to.
	if !event.OnPullRequest() {
		return writeReport(cmd, report, opt.noColor, ui.Row{
			Status: ui.StatusContext,
			Label:  "not a pull request",
			Value:  "nothing to decide",
		})
	}
	command := clearance.Parse(event.Comment.Body)
	if command.Verb == clearance.VerbNone {
		return writeReport(cmd, report, opt.noColor, ui.Row{
			Status: ui.StatusContext,
			Label:  "not addressed to lydite",
			Value:  "ignored",
		})
	}

	token := firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN"))
	if token == "" {
		return fmt.Errorf("clearance needs GITHUB_TOKEN with `statuses: write` and `pull-requests: write`")
	}
	repo, err := forge.ParseRepo(event.Repository.FullName)
	if err != nil {
		return err
	}
	client := forge.New(token)

	head, err := client.HeadSHA(ctx, repo, event.Issue.Number)
	if err != nil {
		return err
	}
	canWrite, err := client.CanWrite(ctx, repo, event.Comment.User.Login)
	if err != nil {
		return err
	}
	status, err := client.ReferralStatus(ctx, repo, head)
	if err != nil {
		return err
	}

	request := clearance.Request{
		Command:   command,
		HeadSHA:   head,
		CanWrite:  canWrite,
		Status:    status,
		CommentAt: event.Comment.CreatedAt,
	}
	// The uncovered set is derived inside the ladder rather than before it,
	// so a command the ladder refuses never reads the exemptions file nor
	// asks the platform what the pull request touched. What went wrong goes
	// to the job log; the commenter reads a refusal, because a command that
	// errors out with no reply is one whose author has only silence to go on.
	uncover := func() ([]string, error) {
		uncovered, err := uncoveredPaths(ctx, client, repo, opt.dir, event.Issue.Number)
		if err != nil {
			fmt.Fprintf(os.Stderr, "lydite: deriving the change's uncovered paths: %v\n", err)
		}
		return uncovered, err
	}

	row, err := applyAction(ctx, cmd, client, repo, event, head, opt, clearance.Decide(request, uncover))
	if err != nil {
		return err
	}
	return writeReport(cmd, report, opt.noColor, row)
}

// applyAction carries out the decision and returns the row describing it.
//
// opt.statusOut names where a clearance is rendered instead of posted. It is
// read by this one branch: a decision that clears nothing has no status to
// write, and rendering one anyway would hand the posting step a document
// saying a referral was resolved by a comment that resolved nothing.
func applyAction(ctx context.Context, cmd *cobra.Command, client *forge.Client, repo forge.Repo, event forge.CommentEvent, head string, opt clearanceOptions, action clearance.Action) (ui.Row, error) {
	switch action.Kind {
	case clearance.KindClear:
		description := clearance.WithFingerprint(
			fmt.Sprintf("cleared by @%s at %s", event.Comment.User.Login, shortSHA(head)),
			clearedFingerprint(ctx, cmd, client, repo, event.Issue.Number, opt, head))
		// The head is the one the platform answered for this pull request a
		// moment ago, never anything the comment named: the poster resolves
		// it again and refuses a document naming anything else.
		ref := pullRequestRef{SHA: head, Number: event.Issue.Number}
		if err := recordClearance(ctx, client, repo, opt.statusOut, clearanceStatus(ref, description)); err != nil {
			return ui.Row{}, err
		}
		reply(ctx, client, repo, event.Issue.Number, ui.Comment{
			Verdict:  ui.VerdictPass,
			Headline: description,
			Version:  version,
			Base:     shortSHA(head),
		})
		return ui.Row{Status: ui.StatusPass, Label: "clearance", Value: description}, nil

	case clearance.KindExplain:
		return explain(ctx, client, repo, event, head)

	case clearance.KindExempt:
		return propose(ctx, client, repo, event, head, action)

	case clearance.KindRefuse:
		text := refusal(action.Reason, event.Comment.User.Login, head)
		reply(ctx, client, repo, event.Issue.Number, ui.Comment{
			Verdict:  ui.VerdictRefer,
			Headline: text,
			Version:  version,
			Base:     shortSHA(head),
		})
		return ui.Row{Status: ui.StatusRefer, Label: string(action.Reason), Value: text}, nil

	default:
		return ui.Row{Status: ui.StatusContext, Label: "no action", Value: "ignored"}, nil
	}
}

// clearedFingerprint is the fingerprint of the decision this clearance is
// given for, and is empty when it could not be computed here.
//
// Empty is a recorded answer rather than a silent one: clearance.WithFingerprint
// appends nothing, which clearance.FingerprintIn reads back as absent, and the
// relay refuses a comparison it cannot make rather than treating absence as
// agreement — so a clearance recorded without one still resolves the referral
// on this head and simply does not carry onto a merge-queue entry. Every way
// that can happen is named on stderr, because a clearance that quietly stops
// travelling is one nobody can tell from a queue entry that legitimately
// re-refers.
//
// Nothing here fails the run. Answering the comment is this command's job, and
// a fingerprint that could not be taken is not a reason to leave the referral
// standing with the commenter told nothing.
func clearedFingerprint(ctx context.Context, cmd *cobra.Command, client *forge.Client, repo forge.Repo, number int, opt clearanceOptions, head string) string {
	decision, err := clearedDecision(ctx, cmd, client, repo, number, opt, head)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"lydite: this clearance records no fingerprint, so it will not carry onto a merge-queue entry: %v\n", err)
		return ""
	}
	return referral.Fingerprint(decision.Uncovered, decision.Disqualifications)
}

// clearedDecision recomputes, for the revision being cleared, the decision a
// person is clearing.
//
// It is `review`'s own computation and not a summary of it: the exemptions at
// the base commit, the change's own diff, the API-surface comparison every
// opted-in component asked for, and the dependency comparison for every
// manifest the change touches — because the fingerprint has to describe the
// decision that was cleared. A narrower one would record a person's judgement
// against reasons that were never the whole of what referred the change.
//
// opt.surfaces names a comparison `review compare` already made, and is the
// route for a component whose comparison runs the change's own code: read here,
// it is reconciled against what this run resolves for itself and never re-run,
// so the job holding the credential this clearance is recorded with executes
// none of it. A run given no document makes the comparison itself, refusing an
// opted-in component whose comparison runs the change's own code — this
// process always holds the token `runClearance` requires, whether or not it
// also posts directly, so there is no invocation of `clearance` in which that
// comparison is safe to run in-process.
//
// The rows both comparisons render go to a report of their own rather than to
// the one this command writes. What this run reports is the clearance; the
// verdict those rows describe belongs to the `review` that published the
// referral, and restating it here as this command's own rows would put a second
// derivation of one verdict in front of a reader.
//
// The evidence is the zero referral.Evidence, the same value `clearance queue`
// recomputes under: a `versions:` condition needs the licence and SCA rows of a
// scan document, which no comment-answering job has, and passing nothing is the
// direction that refers.
func clearedDecision(ctx context.Context, cmd *cobra.Command, client *forge.Client, repo forge.Repo, number int, opt clearanceOptions, head string) (referral.Decision, error) {
	// Cheapest first, and refused before anything is resolved or fetched: a
	// decision computed over some other revision is not the decision being
	// cleared, and recording its fingerprint would attach a person's judgement
	// to reasons that were never in front of them.
	if err := checkoutIsHead(ctx, opt.dir, head); err != nil {
		return referral.Decision{}, err
	}
	baseSHA, err := resolveReviewBase(ctx, opt.dir, opt.base, opt.baseBranch)
	if err != nil {
		return referral.Decision{}, err
	}
	var surfaces []surfaceComparison
	if opt.surfaces != "" {
		doc, readErr := readSurfaces(opt.surfaces)
		switch readErr {
		case nil:
			// reconcileSurfaces checks the document's base against the
			// baseSHA resolved here and requires a result for every
			// component this tree says opted in — neither is taken on the
			// document's own word.
			surfaces, err = reconcileSurfaces(opt.dir, baseSHA, doc)
		default:
			// Unreadable, not absent: a document the computing job never
			// wrote is no evidence any surface is clean, so every opted-in
			// component is uncomputable. That is the decision `review`
			// reaches from the same document, and the fingerprint has to
			// describe the decision rather than a cleaner reading of it.
			surfaces, err = uncomputableSurfaces(opt.dir, "the comparison document could not be read: "+readErr.Error())
		}
		if err != nil {
			return referral.Decision{}, err
		}
	} else {
		// Always guarded, unlike `review`'s own fallback: `review`'s guard
		// tests whether the invocation also publishes, because a `review` run
		// that only renders holds no credential of its own. `runClearance`
		// requires GITHUB_TOKEN unconditionally — reading the head, the
		// commenter's permission, the standing referral, and posting the
		// reply all need it — so a `clearance` process holds a credential
		// whether or not it also posts the status directly, and there is no
		// invocation in which running a component's own build code here is
		// safe. A document from --surfaces is the only route to a full-parity
		// comparison for a component whose comparison runs the change's own
		// code; see
		// agentic/rules/give-untrusted-build-scripts-no-inherited-environment.md.
		surfaces, err = computeAPISurfaces(ctx, cmd, opt.dir, baseSHA, true)
		if err != nil {
			return referral.Decision{}, err
		}
	}
	file, err := loadExemptionsAt(ctx, opt.dir, baseSHA)
	if err != nil {
		return referral.Decision{}, err
	}
	change, err := referral.Changes(ctx, opt.dir, baseSHA)
	if err != nil {
		return referral.Decision{}, err
	}
	decision := referral.Decide(change, file, referral.Evidence{})
	report := ui.NewReport("cleared decision")
	// Resolved live rather than read from the comment payload: an
	// issue_comment event carries no pull_request.title at all, only the
	// comment thread's own issue.title, which is the pull request's title
	// only by convention. A break declared solely in the title would
	// otherwise never be seen here. A failure to resolve it can only
	// under-refer, so it is warned about rather than fatal.
	title, err := client.PullRequestTitle(ctx, repo, number)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
			"lydite: could not resolve the pull request's title (%v) — a break declared only there is not seen\n", err)
	}
	renderAPISurfaceRows(ctx, cmd, report, &decision, opt.dir, baseSHA, title, surfaces)
	addDependencyRows(report, &decision, measureDependencies(ctx, opt.dir, baseSHA, change.Paths), baseSHA)
	return decision, nil
}

// checkoutIsHead establishes that the tree the decision is recomputed over is
// the revision being cleared.
//
// A job answering a comment is free to check out anything, and the default
// branch is the ordinary choice: the change being cleared is not in that tree
// at all, so a recomputation there answers for the default branch and
// fingerprints a decision nobody was asked about.
func checkoutIsHead(ctx context.Context, dir, head string) error {
	r := executil.RunQuiet(ctx, dir, "git", "rev-parse", "HEAD")
	at := strings.TrimSpace(r.Output)
	if !r.Ok() || at == "" {
		return fmt.Errorf("reading which revision %s is checked out at: %w", dir, r.Err)
	}
	if at != head {
		return fmt.Errorf("the checkout is at %s, not the revision being cleared (%s) — "+
			"the decision has to be recomputed against the pull request's own head", shortSHA(at), shortSHA(head))
	}
	return nil
}

// explain restates the standing verdict without changing it.
func explain(ctx context.Context, client *forge.Client, repo forge.Repo, event forge.CommentEvent, head string) (ui.Row, error) {
	status, err := client.ReferralStatus(ctx, repo, head)
	if err != nil {
		return ui.Row{}, err
	}
	comment := ui.Comment{Version: version, Base: shortSHA(head)}
	if status == nil {
		comment.Verdict = ui.VerdictRefer
		comment.Headline = "no verdict has been published for this revision yet"
	} else {
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
	reply(ctx, client, repo, event.Issue.Number, comment)
	return ui.Row{Status: ui.StatusContext, Label: "explain", Value: comment.Headline}, nil
}

// uncoveredPaths answers which of a pull request's changed paths no declared
// exemption covers.
//
// The exemptions file comes from the working tree, which is the clearance
// job's own checkout of the default branch — so the declarations consulted
// are the ones in force, never the ones the pull request proposes for itself.
// Which file that is comes from --dir the way every other command resolves
// it: referral.RootRelative turns the scan root into its path from the
// repository root, so a repository whose scan root is a subdirectory reads
// the declarations governing it rather than missing them and proposing an
// entry over every path the change touches.
//
// An absent file is the day-one state rather than an error; an unparseable
// one is an error, because "nothing is exempt" and "the file nobody can read"
// are different answers and only the first is a repository's decision.
//
// The changed paths are the platform's own list of names, and are the only
// thing the comment surface asks about the pull request itself. Nothing of
// its content is fetched: see
// docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md.
func uncoveredPaths(ctx context.Context, client *forge.Client, repo forge.Repo, dir string, number int) ([]string, error) {
	prefix, err := referral.RootRelative(ctx, dir)
	if err != nil {
		return nil, err
	}
	// repoPath is repository-root-relative, for the parse label and any
	// error a person reads; opening the file has to go through dir instead,
	// since the process's own directory is not necessarily the repository
	// root and a path.Join with prefix would then name the wrong file.
	repoPath := path.Join(prefix, referral.FileName)
	var file referral.File
	data, err := os.ReadFile(filepath.Join(dir, referral.FileName)) // #nosec G304 -- dir is the operator's own --dir flag, not pull-request content
	switch {
	case err == nil:
		if file, err = referral.Parse(data, repoPath); err != nil {
			return nil, err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("reading %s: %w", repoPath, err)
	}
	changed, err := client.ChangedPaths(ctx, repo, number)
	if err != nil {
		return nil, err
	}
	return referral.Uncovered(changed, file.Exemptions), nil
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
// assembled into a line by hand carries whatever YAML syntax it contains
// into the document's structure — enough to close the paths sequence early
// and add a second entry, with a reason that answers itself, to something
// lydite posts under its own identity.
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

// propose answers with a draft exemptions-file entry, and changes nothing.
//
// The name is the commenter's and the paths are the change's own uncovered
// set: a pattern a person typed could widen the entry past what their change
// needs covered, and an entry that reads as lydite's output is the one a
// reviewer is least likely to re-derive.
func propose(ctx context.Context, client *forge.Client, repo forge.Repo, event forge.CommentEvent, head string, action clearance.Action) (ui.Row, error) {
	lines, err := proposalYAML(action.Name, action.Paths)
	if err != nil {
		return ui.Row{}, err
	}
	headline := fmt.Sprintf("a draft entry covering the %d path(s) no declared exemption covers. "+
		"Nothing has changed and nobody has reviewed this: the referral stands until an entry "+
		"like it is merged into `%s` on the default branch, and the reason has to be answered "+
		"before it will parse.", len(action.Paths), referral.FileName)
	reply(ctx, client, repo, event.Issue.Number, ui.Comment{
		Verdict:  ui.VerdictRefer,
		Headline: headline,
		Sections: []ui.CommentSection{{
			Status:  ui.StatusRefer,
			Title:   "proposed exemption",
			Summary: action.Name,
			Details: []ui.CommentDetail{{Lines: lines}},
		}},
		Version: version,
		Base:    shortSHA(head),
	})
	return ui.Row{
		Status: ui.StatusRefer,
		Label:  "exempt",
		Value:  fmt.Sprintf("proposed %q over %d path(s)", action.Name, len(action.Paths)),
	}, nil
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

// reply posts an answer to the conversation.
//
// A refusal or an explanation is an answer to a question somebody asked, so
// it is a new comment rather than an edit of the standing verdict. Editing
// the sticky comment would answer in a place the asker is not looking, and
// would overwrite the verdict with a reply to one person.
func reply(ctx context.Context, client *forge.Client, repo forge.Repo, number int, comment ui.Comment) {
	if err := client.CreateComment(ctx, repo, number, comment.Render()); err != nil {
		fmt.Fprintf(os.Stderr, "lydite: the decision was recorded but the reply was not posted: %v\n", err)
	}
}

// writeReport renders what happened, and never turns a row into an exit
// code.
//
// This command answers a comment; answering one is its whole job, and it
// succeeded whether or not the answer was "nothing changed". What a change's
// verdict is remains the commit status's to say, so a refused clearance is
// amber in the log and leaves the standing referral exactly as it was.
// Exiting non-zero here would report a stranger's mistyped comment as a
// broken pipeline.
func writeReport(cmd *cobra.Command, report *ui.Report, noColor bool, rows ...ui.Row) error {
	for _, row := range rows {
		report.Add(row)
	}
	out := cmd.OutOrStdout()
	return report.Write(out, false, ui.ColorEnabled(out, noColor))
}
