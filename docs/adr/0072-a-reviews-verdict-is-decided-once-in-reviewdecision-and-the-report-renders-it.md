# A review's verdict is decided once, in reviewdecision, and the report renders it

`reviewdecision.Decide` returns the review as an ordered list of outcomes — one per concern it
decided, in the order a reader meets them: each API-surface comparison, each dependency delta, the
bundling check, each disqualification, the referral itself — and each outcome carries a domain
status (`pass`, `refer`, `fail`, or a status that never votes) together with the values it was
decided from. The verdict is folded from that list, inside `reviewdecision`, and it is the only
verdict there is: the commit status a review publishes is composed from it by a stage, and the CLI
renders the report's rows from the outcomes, mapping each domain status to exactly one `ui.Status`.

The CLI may shorten a long run of disqualifications to a capped list and a "more not shown" row —
how much to show is presentation — and that can never move the verdict: any disqualification
makes the referral outcome itself `refer`, so the rows that remain always carry the severity of the
ones elided. The row statuses and the published state therefore fold over the same outcomes and
cannot disagree.

## Rejected: reading the verdict back from the report

The command this replaces built its report rows first and then published `report.Verdict()`, so
the machine-read status and the human-read report were one derivation by ordering. It put the
decision in the presentation layer: which concern fails, which refers and which passes was decided
while choosing a row's colour. A stage cannot read a `ui.Report` — nothing below the CLI may — so
the flow could only publish that verdict by splitting itself around the CLI or by deciding again.

## Rejected: a second derivation pinned by a test

Adding a verdict to `reviewdecision.Result` while leaving row statuses decided in the CLI is the
smaller change. It leaves two derivations of one fact, kept in step only by a test that has to
enumerate every shape a review can take — and the shape nobody enumerated is the one where the
exit code says refer and the status says success.

## Rejected: decomposing Decide into stages

`Decide` resolves its title and its scan evidence lazily, through closures, so a run that stops
early pays for neither. Declaring those as conditional stages would make the laziness visible in
the flow, but `clearancestages.Fingerprint` calls `Decide` whole, and a review flow composing the
same steps itself would be a second composition of one decision — the drift this record exists to
prevent. The laziness is the decision's own policy, not orchestration between concerns, so it
stays inside `Decide`. What the closures do moved below the CLI with it: reading the title from a
payload path, and interpreting the scan documents' licence and advisory gates, are
`reviewdecision`'s; only the reading of a report document is injected, behind an interface
`reviewdecision` declares and answers in its own terms.
