# Refuse an unhandled grammar rather than let it fall through to another's rule

A per-grammar dispatch — `internal/crap`'s `complexityOf`, or any future switch keyed on
`treesitter.Grammar` — must name every grammar it counts and error on the rest, never end on an
`if`/`else` or a `default` that hands the last case whatever rule was written for a different one.
Each rule is a list of node-type names, and a name from one grammar matches nothing in another: a
walk pointed at the wrong table finds no decision point at all and reports every function as
complexity 1 — a well-formed number nothing downstream can tell apart from a genuinely simple
function. No error, no panic, and a green suite, so the refusal is the only thing that makes a
grammar added without its own counting rule visible.

## Applies to

Any switch keyed on `treesitter.Grammar` that answers a per-language rule rather than a shared
one: `internal/crap/treesitter.go`'s `complexityOf`, and `internal/treesitter/testcode.go`'s
`DeclaredTests`, whose `default` case returns `ErrNoGrammar` for a language the grammar tables
parse but that switch does not enumerate — an empty result and "no tests declared" are opposite
answers a caller cannot tell apart, and a diff's "new test" check is a set difference, so
answering empty for both revisions reports no new test at either and the gate passes without
having looked.

## Example

```go
// wrong: a new grammar with no rule of its own silently inherits TypeScript's
if g == treesitter.Rust {
    return complexityRust(f, language)
}
return complexityTypeScript(f, language)

// right: every counted grammar is named, and the rest fail loud
switch g {
case treesitter.Rust:
    return complexityRust(f, language), nil
case treesitter.TypeScript, treesitter.TSX:
    return complexityTypeScript(f, language), nil
case treesitter.Python:
    return complexityPython(f, language), nil
default:
    return 0, fmt.Errorf("%s is parsed and has no complexity counting rule", lang)
}
```

Reasoning: [`agentic/references/crap.md`](../references/crap.md).
