// Package declaration reads the author's claim that a change breaks an API.
//
// It answers one question — was a break declared — and nothing about whether
// one happened: the surface diff is evidence read off two trees, and this is a
// claim read off text the author wrote. The two are never mixed, because a
// claim may only ever add a referral and never remove one (see
// docs/adr/0014). Its absence is what the undeclared-break gate fires on.
//
// The same text is read wherever a marker can be spelled: a pull request
// title, and the message of every commit in base..HEAD. Squash merge makes the
// title the commit that lands, so a break declared only in a commit about to be
// squashed away leaves no marker in the history; locally there is no title and
// the commits are all there is. Either source declaring is enough.
package declaration

import (
	"regexp"
	"strings"
)

// header matches a conventional-commit header whose type carries the breaking
// `!`: `<type>[(scope)]!:`. The `!` must sit between the optional scope and the
// colon, which is the only position the grammar gives it any meaning — a `!`
// inside a scope (`feat(a!b):`), ahead of one (`feat!(scope):`, which the
// grammar does not admit at all) or anywhere in the description is ordinary
// text, and reading it as a declaration would have punctuation in a subject
// line refer a change nobody claimed anything about.
var header = regexp.MustCompile(`^[A-Za-z]+(\([^()]*\))?!:`)

// breakingFooters are the two spellings the Conventional Commits
// specification allows for the footer, case-sensitive. `Breaking change:` and
// `BREAKING_CHANGE:` are not among them and are not accepted: a footer token
// is matched verbatim by every tool that reads one, so a lenient match here
// would declare a break that a release-time check reading the same history
// would not see.
var breakingFooters = []string{"BREAKING CHANGE", "BREAKING-CHANGE"}

// Declared reports whether text carries a breaking-change marker — either the
// `!` in its conventional-commit type, or a `BREAKING CHANGE:` footer.
//
// One function for both sources. A caller asks it of the pull request title
// and of each commit message and ORs the answers, because the marker means the
// same thing wherever it is spelled and a second parser for the second source
// could only disagree with this one.
func Declared(text string) bool {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		// The header is the first line the text has: a `feat!:` further down a
		// body is a quoted or discussed subject, not this change's own.
		if i == 0 && header.MatchString(line) {
			return true
		}
		for _, token := range breakingFooters {
			// A footer is `TOKEN: description` or `TOKEN #description`. The
			// separator is required, or a line merely beginning with the words
			// would declare a break the author was only writing about.
			if strings.HasPrefix(line, token+":") || strings.HasPrefix(line, token+" #") {
				return true
			}
		}
	}
	return false
}

// AnyDeclared reports whether any of the texts declares a break — a pull
// request title and the range's commit messages in one call, where a caller
// has both.
func AnyDeclared(texts ...string) bool {
	for _, text := range texts {
		if Declared(text) {
			return true
		}
	}
	return false
}
