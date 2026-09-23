# Scope a workflow's `concurrency.group` to the key that must not lose a run

GitHub keeps only one pending run behind the one already executing in a `concurrency` group —
a third trigger evicts the second's still-queued run before it ever starts, silently, with no
later run left to recover what it would have measured. A group shared across every push to a
workflow makes that eviction ordinary rather than rare. Scope the group to whatever key needs a
guaranteed run — `${{ github.sha }}` when every commit must eventually be measured — so different
keys queue independently instead of competing for the workflow's one queue slot, and let the
state the workflow writes to absorb the resulting overlap (a fetch-and-retry loop, an idempotent
write) rather than using the group itself to prevent concurrent writers.

## Applies to

Any `.github/workflows/*.yml` with a `concurrency:` block whose runs write to shared state — a
default-branch cache, a recorded history, a published artifact — where `gitstate.Write`'s retry
loop (`source/cli/internal/gitstate/gitstate.go`) is one example of a write path built to absorb
the overlap this scoping permits.

Reasoning: [`agentic/references/quality-history.md`](../references/quality-history.md).
