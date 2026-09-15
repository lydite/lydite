# Sort a fallback transport's response into three, not two

A binary "200, or fall back" check cannot tell a transient outage from a permanent
misconfiguration, and a fallback substitutes one identity or one path for another
silently on both. Split the non-`200` case into three: fall back silently for the
transport's own documented "not opted in" answer, fall back under a `::warning::`
for an outage that plausibly resolves on retry, and fail the step with no fallback
for anything deterministic — a bad audience, a malformed request, a version skew —
because retrying it answers the same and a silent fallback there hides a
misconfiguration forever instead of surfacing it once.

Reasoning: [`docs/adr/0037-a-deterministic-relay-misconfiguration-fails-the-step-not-the-fallback.md`](../../docs/adr/0037-a-deterministic-relay-misconfiguration-fails-the-step-not-the-fallback.md)
and [`agentic/references/surface.md`](../references/surface.md).
