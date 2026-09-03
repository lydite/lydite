// Package clearance turns a comment on a pull request into a decision, and
// decides nothing else.
//
// A referral is the one lydite verdict that is pending rather than terminal:
// no change to the branch resolves it, and what does is a person saying so.
// This package is where that saying-so is interpreted.
//
// Everything here is a pure function of a webhook payload and the statuses
// already standing on a commit. The rules are worth more than the plumbing
// and are far easier to get wrong, so they are kept where a test can reach
// them without a network. See docs/adr/0015-clearance-binds-to-a-commit.md.
package clearance

import "strings"

// Prefix is what addresses a comment to lydite. A comment not starting with
// it is not for us, and is not an error either — most comments on a pull
// request are conversation.
const Prefix = "/lydite"

// Verb is one word of the command surface.
type Verb string

const (
	// VerbNone means the comment does not address lydite at all.
	VerbNone Verb = ""
	// VerbClear resolves a referral on the commit it names.
	VerbClear Verb = "clear"
	// VerbExplain restates the standing verdict.
	VerbExplain Verb = "explain"
	// VerbUnknown is a comment addressed to lydite naming something else.
	// It is answered rather than ignored: silence is indistinguishable from
	// a broken workflow, and the one thing a person must never conclude
	// from a mistyped verb is that their change was cleared.
	VerbUnknown Verb = "unknown"
)

// Command is a parsed comment.
type Command struct {
	Verb Verb
	// SHA is the revision the author named, empty when they named none.
	// Naming one is the difference between "clear what is there now" and
	// "clear what I read", which matters when a push lands in between.
	SHA string
	// Word is what was written where a verb belongs, kept so an unknown
	// verb can be quoted back.
	Word string
}

// Parse reads the first line of a comment body.
//
// Only the first line, because a comment may quote an earlier one — GitHub's
// reply-with-quote produces a body whose later lines begin with "> " and can
// contain any command previously issued on the thread. Scanning the whole
// body would let a quoted "/lydite clear" clear a change nobody meant to
// clear, which is the kind of bug that only ever fails open.
func Parse(body string) Command {
	line := body
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 || fields[0] != Prefix {
		return Command{Verb: VerbNone}
	}
	if len(fields) == 1 {
		return Command{Verb: VerbUnknown}
	}
	cmd := Command{Word: fields[1]}
	switch fields[1] {
	case "clear":
		cmd.Verb = VerbClear
	case "explain":
		cmd.Verb = VerbExplain
	default:
		cmd.Verb = VerbUnknown
		return cmd
	}
	if len(fields) > 2 {
		cmd.SHA = fields[2]
	}
	return cmd
}
