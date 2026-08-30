# Referral matches on evidence, never on what a change says about itself

`lydite review` decides whether a change may merge unattended by matching it
against the exemptions declared in `.lydite.exemptions.yml`. Everything it
matches on — changed paths, added lines, the base commit's copy of the
exemption set — is read off the diff. Nothing an author asserts about their own
change may earn an exemption or clear a disqualifier.

This narrows [ADR 0013](0013-referral-not-approval.md), which is otherwise
implemented as written. That ADR closes on a property worth restating, because
it is the one this decision qualifies:

> A surviving mutant that cannot be killed must be annotated as equivalent. A
> public API break must be declared through the conventional-commit marker.
> Both annotations are themselves disqualifiers. So an agent may always proceed
> — but declaring why lands the change on a human's desk with the claim
> attached.

## Two kinds of disqualifier, and only one of them holds

The vetoes ADR 0013 lists are not the same kind of thing.

**Evidence-based** — a net-new `nosemgrep` or `#nosec`, a newly `t.Skip`ped
test, an edit to `.lydite.yml` or to `.github/workflows/`. These are read out of
the diff. An agent cannot remove one without removing the thing itself, and
removing the thing has its own cost: drop the suppression and the finding comes
back, and a *gate* fails. There is no move that makes the disqualifier vanish
while keeping the benefit.

**Claim-based** — the conventional-commit `!` marker, an "this mutant is
equivalent" annotation. These are read out of what the author wrote about their
own change. The honest path being available does not make the dishonest one
unavailable: nothing in lydite detects an undeclared API break, so rewriting
`feat!:` as `feat:` removes the disqualifier at no cost and changes nothing else
about the change.

A claim-based veto therefore works exactly when it is not needed — for an author
who would have declared anyway — and fails silently for the author this whole
design was built for. Worse, shipping it puts a line in the report that reads as
a verified fact, and it would be counted as covering the API-break risk, which
is how the detector that would actually cover it never gets built.

So `lydite review` ships the evidence-based set and not the claim-based one. The
`!` marker returns alongside something that can catch its absence; until then,
lydite does not detect API breaks and says nothing that implies otherwise.

The general rule, which every future disqualifier and every future exemption
condition has to satisfy:

> An author-controlled claim may only ever add a referral, never remove one.

Under it the `!` marker is safe to honour whenever it returns, because no
exemption may key on the commit type — the marker's only power is to refer. It
is a one-way ratchet: useful to an honest author, worthless as a bypass, and
never worse than having no marker at all.

## An exemption is a shape, not a set of blessed paths

ADR 0013 requires that an exemption match only when *every* changed path is
covered, and the reason is a hole it closes: an exemption matching on *any*
changed file lets an agent staple a README tweak onto a dangerous change.

That leaves a case it does not settle. A change touches `README.md` and
`docs/adr/0014.md`; the file declares `readme-only` and `docs-only`; neither
covers the change alone, and together they cover every path. This is referred.

The alternative — take the union — turns the file into one global allowlist of
paths, and that has a consequence out of all proportion to the convenience it
buys. Exemptions stop being independently reviewable: adding a narrow entry
silently widens every existing entry by union, so nobody can reason about one
entry at a time. That destroys the artefact the allowlist model exists to
produce, and which [#15](https://github.com/lydite/lydite/issues/15) isolates
exemption changes specifically to protect — `git log .lydite.exemptions.yml` as
the complete, readable record of every widening. Under union you would have to
re-derive the whole file's closure on every change, which is exactly the careful
reading this design assumes will not happen.

The union reading also has no answer once exemptions carry conditions. If
`dependency-bump` requires a clean SCA run and `readme-only` requires nothing,
what does their union require? The shape reading answers that trivially: neither,
because their union is not a declared shape.

The accepted cost is duplication. Somebody will eventually write an exemption
whose entire content is "readme *or* docs", next to two entries that already say
those things separately. That entry is not redundant — it is an explicit
statement that *this combination* is boring, which is precisely the thing being
reviewed.

## Matching detail that follows from the above

- **Path patterns are anchored.** `README.md` matches the README at the scan
  root and nothing else; any-depth matching is spelled `**/README.md`. This
  parts company with gitignore, whose slash-less patterns float. Floating is
  right for a list of things to skip, where over-matching is free. It is wrong
  here: these patterns decide what merges without a human, so every widening
  should have to be written down.
- **Unknown keys are rejected.** If a later lydite grows a condition field and
  an older binary ignores it, the exemption widens to whatever it says without
  that field — nobody edited the file and nobody reviewed a change. Same stance
  `config.validateLinter` takes toward a retired `linter: eslint`.
- **The built-in disqualifiers cannot be removed.** A repo's `disqualifiers:`
  block only adds. ADR 0013's argument for keeping vetoes in their own list —
  every future exemption would otherwise have to remember every disqualifier,
  and forgetting one is silent — applies one level up to the file itself: a veto
  list that can be emptied is not a floor.
- **A rename contributes both of its paths.** Counting only the destination
  would let a file be moved out of an exempt tree, or into one, while the
  exemption still matched.
- **The diff covers the whole repository, not just `--dir`.** Coverage scopes
  its diff to the scan root because it measures what it can reach. Referral
  decides whether a *pull request* needs a human, and a workflow edit outside a
  monorepo's scan root is exactly the change that must not slip past.
- **A change touching nothing is not referred.** Every exemption covers it
  vacuously, so leaving it to the matching loop would make the verdict depend on
  whether the file happens to be empty.
