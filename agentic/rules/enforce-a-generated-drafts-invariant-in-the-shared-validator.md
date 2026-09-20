# Enforce a generated draft's invariant in the shared validator, not in the generator

A draft `.lydite/exemptions.yml` entry lydite proposes in a comment is only "not yet landable"
if something checks that at the file's one point of entry. `/lydite exempt` reserves the literal
`TODO(lydite):` as its `reason` placeholder, but the check that rejects a reason still carrying
it lives in `internal/referral.Exemption.validate`, not in the code that generates the comment
(`cmd/lydite/clearance.go`). The generator is one route from a proposal into the file, and not a
route lydite controls — the block is copied by hand, or pasted by whatever an author's editor or
agent does with a fenced code block. `validate` is the one place every route already passes
through: a hand-written file, a generated one, and any future producer nobody has thought of yet.
Relying on wording alone (a reason that merely *reads* unfinished) makes the protection a hoped-for
norm a skimming reviewer can miss; a check at parse time is the only form of "a person must
actually supply this" that does not depend on somebody reading carefully.

## Applies to

Any future lydite verb or code path that emits a paste-ready fragment of a file lydite also
parses (an exemption, a component declaration, a config stub) with a placeholder standing in
for a judgement a person has to make.

## Example

```go
// wrong: only the generator refuses to emit a finished-looking placeholder
func proposalYAML(...) { /* trusts nobody edits this in by hand elsewhere */ }

// right: the shared parser rejects the marker regardless of how the text arrived
func (f File) validate(source string) error {
    if strings.Contains(e.Reason, ReasonPlaceholderMarker) {
        return fmt.Errorf("...reason still carries lydite's own placeholder marker...")
    }
}
```

Reasoning: [`docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md`](../../docs/adr/0049-exempt-proposes-an-entry-and-lands-nothing.md).
