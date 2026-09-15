# Ask `internal/treesitter` whether code is the suite, never reimplement the check

Whether a file, or an inline module, is test code rather than code under test is one question,
asked identically by every gate that walks a Rust or TypeScript file: `internal/treesitter`'s
`Grammar.TestFile` and `Grammar.TestModule` are the only answer. A second, gate-local copy of the
same path or node-type rule agrees with this one only until somebody edits one of them for one
gate's own reason — mutating an assertion and scoring one both ask "is this the suite", and a
diverging pair of answers is a bug neither gate's own tests can catch.

## Applies to

Any gate walking a Rust or TypeScript file — `internal/mutation`, `internal/crap`, and any gate
added later — that needs to tell suite code from code under test.

## Example

```go
// wrong: a gate's own copy of the rule
if strings.HasSuffix(path, ".test.ts") { ... }

// right: ask the shared classification
if grammar.TestFile(path) { ... }
```

Reasoning: [`agentic/references/mutation.md`](../references/mutation.md) and
[`agentic/references/crap.md`](../references/crap.md).
