---
name: relay-audience-mismatch-degrades-silently
kind: gotcha
description: The relay's AUDIENCE and a caller's requested audience are two independently-edited values with no shared source, but a mismatch (401) now fails the posting step loudly — only a missing install (409) or an outage (5xx/000) fall back.
anchors:
  - path: source/cloud-services/pr-relay/wrangler.toml
    blob: d9f53f54fac4
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: cccaa62747de
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: 148f32f2e36f
  - path: .github/actions/lydite-comment/action.yml
    blob: 232a9df93a50
  - path: .github/actions/lydite-threads/action.yml
    blob: 7ac719f13a0a
  - path: source/cli/cmd/lydite/mergequeue.go
    blob: 884ec4e21b1e
confidence: verified
---

`verifyActionsToken` rejects a token whose `claims.aud` is not the relay's own
(`libs/github-app/src/oidc.ts:113-114`, "the token was minted for another audience").
The expected value is passed as `env.AUDIENCE` (`pr-relay/src/index.ts:249`), a
hardcoded Worker var: `AUDIENCE = "https://pr.lydite.org"` (`pr-relay/wrangler.toml:19`).
The caller mints its token against a separately-configured origin (e.g.
`vars.LYDITE_RELAY_URL`, a GitHub Actions repository variable) with no shared constant
or check tying the two strings together — they must be byte-identical by convention
alone.

**This drifting apart is no longer a silent failure.** `.github/actions/lydite-comment/action.yml`
and `.github/actions/lydite-threads/action.yml` both now sort the relay's answer into three
buckets (documented in each action's own header comment, `lydite-comment/action.yml:15-26`):

- **409, or no relay configured** — the App isn't installed on this repository. Falls back
  silently to `github-actions[bot]` (the ordinary, supported state for an opted-out repo).
- **5xx, or curl's `000`** — an outage. Falls back, under a `::warning::`.
- **Anything else — 401 (bad audience), 400 (bad payload), or an unambiguous 403** — fails
  the step outright (`::error::`, `exit 1`, no `posted`/`applied` output, no fallback
  comment posted). An audience mismatch now produces a red job naming the status, not a
  silently-green one under the wrong byline.

So the note's original claim — "a mismatch falls back to the bot token forever without
going red" — no longer holds; it was true before the three-way sort landed. What is still
true: `AUDIENCE` and the caller's relay URL remain two independently-edited values with no
shared source, so a drift is still possible and still worth checking directly — it just now
announces itself as a failing `publish`/`threads` step instead of hiding behind a green one.

**`cmd/lydite/mergequeue.go`'s relay call is a separate, unrelated code path** (`submitQueueComparison`,
`actionsIDToken`): it mints its own OIDC token in-process against `--relay` and its own doc
comment states "every answer but 200 fails the run, and nothing falls back" — it was never
subject to this gotcha, silent or otherwise.

Note also: this repo's own CI does not currently invoke `.github/actions/lydite-comment` at
all (orchestration moved to `lydite/actions`' reusable workflows per ADR 0051) — see
[[local-lydite-actions-composites-are-dead-in-this-repo]]. The gotcha above is about the
composite action's own code, live for any consumer (e.g. `lydite/actions`) that still calls it.
