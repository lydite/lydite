---
name: pythons-decorator-is-a-child-not-a-sibling
kind: gotcha
about: source/cli/internal/treesitter/scope.go
description: Python's decorated_definition wraps every decorator and the def as children of one node, where Rust's #[attr] and TypeScript's @decorator precede the declaration as siblings — the shared declaration-scope walk needed a new enclosures table to step inside rather than just skip past.
anchors:
  - path: source/cli/internal/treesitter/scope.go
    blob: 746ade146efb2ee0cbca2803b5740d09fe223323
confidence: verified
---

`Grammar.scope` resolves an exclusion declaration's span by walking `NextSibling()` past
any `decorations` node until it lands on the thing the comment is written above. That walk
assumes a decorator and the declaration it modifies are siblings at the same tree level,
which holds for Rust's `attribute_item` and TypeScript's `decorator` but not for Python:
`@app.route` above a `def` parses as a `decorated_definition` node whose *children* are
every decorator followed by the `function_definition` (or `class_definition`) itself, not
as preceding siblings of a bare `function_definition`.

Naming `decorated_definition` itself as a function-introducing node (putting it in
`functions[Python]`) was rejected — the same node type wraps a decorated *class*, and a
table keyed by node type alone can't tell the two apart, so it would score every decorated
class as a function of its own with its methods folded in as nested units.

The fix is a new `enclosures` table (`scope.go`), read only by `Grammar.scope`'s own
sibling walk: when the walk's anchor is a node type listed there, it steps into that
node's first child instead of treating it as the target or a decoration to skip. Python's
`enclosures` entry needs two node types, not one: `decorated_definition` (the decorator
wrapper), and also `block` — Python's grammar lifts the *first* comment of an indented
suite out of the `block` node onto the statement introducing it, so a comment written
above the first method of a class has the whole class body as its next sibling, and
without `block` in `enclosures` that declaration would resolve to the entire class rather
than to its first method.
