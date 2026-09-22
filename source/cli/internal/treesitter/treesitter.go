// Package treesitter is the parse tables lydite reads Rust, TypeScript and TSX
// with, and the questions both the coverage gates and the mutation engine ask
// of a syntax tree.
//
// Go is parsed with go/ast because the language ships its own parser and
// nothing could be more faithful. The other two have no parser in the standard
// library and lydite must stay a single statically-linked CGO_ENABLED=0 binary
// for four platforms, which every C-backed tree-sitter binding rules out.
//
// It sits below internal/coverage and internal/mutation rather than inside
// either, and that placement is the whole reason it exists. internal/mutation
// imports internal/coverage — one edge, for whether a Go file is generated —
// so a parser called from coverage could not live in mutation without making
// the mutation engine a dependency of every coverage figure. Nothing here
// imports either of them.
//
// A build missing a `grammar_subset_<lang>` tag panics at that language's
// first parse. The release tags and the module's own test invocation carry all
// five.
package treesitter

import (
	"fmt"
	"path"
	"strings"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"lydite/lydite/internal/runner"
)

// Grammar names one set of parse tables.
//
// It is the answer to "which tables read this file", asked once and shared, so
// that .ts and .tsx cannot be one language in one package and two in another.
type Grammar int

const (
	// Rust reads .rs.
	Rust Grammar = iota + 1
	// TypeScript reads the TypeScript family except .tsx.
	TypeScript
	// TSX reads .tsx, which the TypeScript tables cannot: `<T>(x) => x` is a
	// type assertion in one and an opening JSX tag in the other, so the two are
	// separate parse tables upstream and a .tsx file read by the TypeScript
	// tables produces a tree full of errors.
	TSX
)

// GrammarFor picks the tables for one file.
//
// By extension and not by the component's language alone, because .ts and .tsx
// are one language to lydite and two grammars to tree-sitter. A language lydite
// has no tables for is reported as none rather than as an error, so each caller
// decides what that means: no mutants for one, no exclusion for the other.
func GrammarFor(lang runner.Lang, file string) (Grammar, bool) {
	switch lang {
	case runner.Rust:
		return Rust, true
	case runner.TypeScript:
		if strings.EqualFold(path.Ext(file), ".tsx") {
			return TSX, true
		}
		return TypeScript, true
	default:
		return 0, false
	}
}

// Language loads the parse tables.
//
// Called per file rather than held, because the loader caches and a
// package-level value would be a global no test could reset.
func (g Grammar) Language() *gotreesitter.Language {
	switch g {
	case Rust:
		return grammars.RustLanguage()
	case TypeScript:
		return grammars.TypescriptLanguage()
	case TSX:
		return grammars.TsxLanguage()
	default:
		return nil
	}
}

// ErrUnparsed reports a file the grammar could not read.
//
// It is an error rather than an empty answer, because the two mean opposite
// things and a caller cannot tell them apart: nothing found is a file lydite
// read and had nothing to say about, and an unparsed file is source lydite
// examined nothing of. Which of the two a caller can live with is the caller's
// to decide — mutation reports it, coverage reads it as no exclusion — and
// neither can decide if the answers arrive spelled the same.
type ErrUnparsed struct {
	Path   string
	Reason string
}

func (e ErrUnparsed) Error() string {
	return fmt.Sprintf("%s: the grammar could not parse this file: %s", e.Path, e.Reason)
}

// ErrNoGrammar reports a language lydite holds no tree-sitter tables for.
type ErrNoGrammar struct {
	Lang runner.Lang
}

func (e ErrNoGrammar) Error() string {
	return fmt.Sprintf("no tree-sitter grammar for %q", e.Lang)
}

// Parse reads one file and answers with the root of its tree, together with the
// tables it was read under — every question about a node's type needs both.
//
// A tree carrying an error node is refused. It is a file the grammar did not
// understand — a syntax lydite's tables predate, or a file that does not
// compile — and the byte ranges and line spans read off it come from a
// misreading that nothing downstream can detect.
func (g Grammar) Parse(path string, src []byte) (*gotreesitter.Node, *gotreesitter.Language, error) {
	language := g.Language()
	if language == nil {
		return nil, nil, ErrUnparsed{Path: path, Reason: "the grammar tables did not load"}
	}
	tree, err := gotreesitter.NewParser(language).Parse(src)
	if err != nil {
		return nil, nil, ErrUnparsed{Path: path, Reason: err.Error()}
	}
	root := tree.RootNode()
	if root == nil {
		return nil, nil, ErrUnparsed{Path: path, Reason: "the parse produced no tree"}
	}
	if root.HasErrorOrMissing() {
		return nil, nil, ErrUnparsed{Path: path,
			Reason: "the tree carries a syntax error, so the byte ranges read off it cannot be trusted"}
	}
	return root, language, nil
}
