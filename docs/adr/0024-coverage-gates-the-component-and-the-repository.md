# Coverage gates the component and the repository, and not the language

[ADR 0019](0019-coverage-per-component-gated-by-lydite-test.md) made the
component the coverage unit and reported at three altitudes — per component, per
language, and globally — gating all three. **The language altitude is removed.
Coverage and patch coverage are reported and gated per component and over the
repository, and nothing in between.**

## A language is not a unit anybody declared

ADR 0019's own premise is that nothing decides what a coverage unit is except
the declaration. A component is what somebody wrote down, owns, and can act on;
the repository is what the change as a whole did. A language is neither. It is a
grouping lydite derived from each component's runner, so a language row gates
something no repository ever said it wanted gated, and a repository that wants
its Go held to one number has no way to say so other than by declaring the
components that way — which is exactly the mechanism ADR 0019 built.

## What it caught, and why that is not enough

The language gate catches one thing the other two miss: a regression spread
across several components of one language, too small to fail any of them against
its own baseline, and diluted in the repository figure by another language
improving at the same time.

That is real and it is narrow, and it is bought at a price paid on every run.
Three altitudes with three tolerances means a change can fail at one and pass at
another, and a reader then has to work out which governs. The repository figure
already catches a regression spread across components; the language figure only
adds the case where a *different* language moved the other way in the same
change.

## It was mostly a restatement

A language composing one component reproduces that component's row exactly —
same lines, same baseline, same verdict. In a repository with one component per
language, which is the common shape and this repository's own, every language
row is a duplicate. Sixteen rows said twelve things.

Skipping the row only where it composes a single component was considered, and
rejected: it keeps a gate whose presence depends on how many components a
language happens to have, so the same change is gated differently before and
after an unrelated component is declared. A gate that appears and disappears
with the declaration is worse than one that is not there.

## What replaces it

Nothing, deliberately. A repository that wants a subset of its components gated
together declares them as it wishes and reads the component rows; one that wants
a single number reads the repository row.

The figures that remain are labelled `coverage(<component>)` and
`coverage(repo)`, so every row is one metric over one named unit and a reader
pairs a component's coverage with its patch by eye. `patch` gained the
repository altitude in the same change it lost the language one — the aggregate
says the repository did not get worse overall and each component's patch row says
that component's new code met its own standard, and neither answers whether the
new code in a change spanning components was tested.

The accepted cost is stated plainly: a regression spread thinly across one
language's components, masked in the repository figure by another language
improving, is no longer caught. Nothing observed it in practice; the rows that
would have caught it were duplicates in every repository lydite has run against.
