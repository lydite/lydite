# An author-controlled claim may only ever add a referral, never remove one

`internal/referral`'s own `Disqualifications` computes every ordinary disqualification from
diff evidence an author cannot rewrite by asserting something. A declaration — a conventional-
commit `!`, a `BREAKING CHANGE:` footer, or any future marker read from a title or commit
message — is text the author wrote, not evidence off two trees, so honouring it as a bypass
would let rewriting the text make a change greener than the diff underneath it. Conventional
Commits' `!` was deliberately left unimplemented until this asymmetry could be enforced (see
[ADR 0014](../../docs/adr/0014-evidence-only-referral-matching.md)); the API-break declaration
in [ADR 0040](../../docs/adr/0040-an-undeclared-go-api-break-fails-and-a-declared-one-is-referred.md)
is the first thing built to the rule rather than merely stated by it.

## Applies to

Any future `internal/referral.Disqualification` kind, exemption rule, or clearance check whose
input includes something an author wrote (a commit message, a PR title, a code comment) rather
than something computed from a diff. `/lydite exempt` is built to it: the commenter's `<shape>`
names the proposed entry, but a commenter-supplied glob for `paths` was rejected outright — it
would let an author widen a proposal past what their own change needs covered, and worse than in
a hand-written entry, because the block reads as lydite's own output. `paths` is always the
change's own uncovered set, never an argument.

## Example

```go
// wrong: a claim clears a gate
if declaration.Declared(title, commits) {
    return ui.StatusPass // the author said it's fine
}

// right: a claim only ever adds a referral, and never suppresses the gate
if hasUndeclaredBreak {
    status = ui.StatusFail
}
if declaration.Declared(title, commits) {
    disqualifications = append(disqualifications, referral.Disqualification{
        Kind: referral.DisqualificationAPIBreakDeclared,
    })
}
```

Reasoning: [`agentic/references/referral-and-clearance.md`](../references/referral-and-clearance.md).
