package treesitter

import (
	"path"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// testCode is how one grammar's test code is told from the code it tests.
//
// It lives beside the parse tables rather than in one gate, because "this is
// the suite" is the same question whichever gate asks it: mutating an
// assertion asks whether a suite notices its own tests changing, and scoring
// one reports a complexity figure about code that ships nothing. Two copies of
// the rule would agree until one of them was edited for one gate's reason.
type testCode struct {
	// file reports whether a whole path is the suite.
	file func(path string) bool
	// module is the node type of a module whose contents are the suite, or
	// empty for a language that puts none inside the file it tests.
	//
	// Rust needs it and the other two do not: `#[cfg(test)] mod tests` sits
	// inside the file under test, so no path rule can see it.
	module string
	// attribute is the attribute, whitespace removed, that marks such a
	// module. It is a preceding sibling of the module rather than a child.
	attribute string
}

var testConventions = map[Grammar]testCode{
	Rust: {
		file:      rustTestFile,
		module:    "mod_item",
		attribute: "#[cfg(test)]",
	},
	TypeScript: {
		file: typeScriptTestFile,
	},
}

// TestFile reports whether a whole file is the suite rather than the code
// under test.
func (g Grammar) TestFile(p string) bool {
	convention, ok := testConventions[g.tables()]
	return ok && convention.file != nil && convention.file(p)
}

// TestModule reports whether n is a module whose contents are the suite.
//
// The attribute is a *preceding sibling* of the module rather than a child, so
// this reads the tree's own order rather than looking inside the module. The
// comparison is against the attribute with its whitespace removed, so
// `#[cfg( test )]` is recognised and `#[cfg(feature = "test")]` is not.
//
// A module reached only through a broader condition — `#[cfg(all(test,
// unix))]` — is not recognised, and is mutated and scored. That is the
// direction the rule has to fail in: a form this does not know about produces
// findings an author can see and answer, where a looser match would silently
// stop examining code that ships.
func (g Grammar) TestModule(n *gotreesitter.Node, src []byte, language *gotreesitter.Language) bool {
	convention := testConventions[g.tables()]
	if n == nil || convention.module == "" || convention.attribute == "" {
		return false
	}
	if n.Type(language) != convention.module {
		return false
	}
	prev := n.PrevSibling()
	return prev != nil && strings.EqualFold(compact(prev.Text(src)), convention.attribute)
}

// rustTestFile reports whether a whole file is Rust's test code: the
// integration and benchmark directories the toolchain treats as test targets.
//
// The unit tests Rust puts *inside* the file they test are excluded by
// TestModule instead, since no path rule can see them.
func rustTestFile(p string) bool {
	for _, dir := range []string{"tests/", "benches/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	return false
}

// typeScriptTestFile recognises the two conventions every JavaScript runner
// lydite ships supports: a `.test.` or `.spec.` infix, and a `__tests__`
// directory.
func typeScriptTestFile(p string) bool {
	base := path.Base(p)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	return strings.HasPrefix(p, "__tests__/") || strings.Contains(p, "/__tests__/")
}

// compact removes every space from an attribute, so one form is recognised
// however it is spaced.
func compact(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, s)
}
