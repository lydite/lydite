# A per-language counting or scoring switch panics on an unhandled grammar, never falls back

A `default` branch that reuses another language's rule produces a plausible number under a
heading naming the wrong language — no error, no parse failure — which is the one failure in
a scoring gate nothing downstream can detect: the row renders green or amber exactly as a
correctly-scored one would. `internal/crap`'s `complexityOf` switches on every
`treesitter.Grammar` its own walked table admits and panics in `default` instead, because the
grammar reaching it already came from `treesitter.GrammarFor` over a language this gate
claims to support — an unhandled case there is a lydite bug, not an input a caller can trigger
by feeding it an unsupported file.

## Applies to

Any new `switch` on a `treesitter.Grammar` (or an equivalent per-language enum) that computes a
number or a verdict rather than routing control flow — a counting rule, a scoring formula, a
per-language threshold. Does not apply to a switch that classifies *unsupported* input on
purpose (`treesitter.GrammarFor`'s own lookup, `ErrNoGrammar`, `ErrNoTestEnumeration`) — those
have to fail open onto a stated "unsupported" answer, not panic, because their input is
whatever the repository under scan contains.

## Example

```go
// wrong: an unhandled grammar silently reuses another language's rule
func complexityOf(g treesitter.Grammar, f treesitter.Func, l *gotreesitter.Language) int {
	switch g {
	case treesitter.Rust:
		return complexityRust(f, l)
	default:
		return complexityTypeScript(f, l) // wrong number, right-looking heading
	}
}

// right: exhaustive, and loud where it is not
func complexityOf(g treesitter.Grammar, f treesitter.Func, l *gotreesitter.Language) int {
	switch g {
	case treesitter.Rust:
		return complexityRust(f, l)
	case treesitter.TypeScript, treesitter.TSX:
		return complexityTypeScript(f, l)
	case treesitter.Python:
		return complexityPython(f, l)
	default:
		panic(fmt.Sprintf("crap: no complexity counting rule for grammar %d", g))
	}
}
```

Reasoning: [`agentic/references/crap.md`](../references/crap.md) and the amendment to
[ADR 0036](../../docs/adr/0036-crap-scores-rust-and-typescript-from-a-hand-rolled-walk.md).
