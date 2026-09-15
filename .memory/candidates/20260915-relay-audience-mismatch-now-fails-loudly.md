---
about: .github/actions/lydite-comment/action.yml
kind: gotcha
---

# The relay-audience-mismatch note's "silent and permanent" claim no longer holds

The promoted note `relay-audience-mismatch-degrades-silently` says a `401` from the relay "is
silent and permanent... nothing goes red — the fallback is a supported, permanently-green path."
That was true before this branch (`docs/adr/0037-...`, landed on `lydite/lydite` PR #153): both
`.github/actions/lydite-comment/action.yml` and `.github/actions/lydite-threads/action.yml` now
classify the relay's answer into three buckets, and `401` (an audience the relay would not
accept), `400`, `404`, and `/comment`'s unambiguous `403`s fail the composite step outright —
`::error::`, `exit 1`, no `posted`/`applied` output set, no fallback comment posted. Only `409`
and an unset relay still fall back silently; `5xx`, curl's `000`, and `/review`'s
comment-id-race `403` fall back under a `::warning::`.

An audience drift (trailing slash, http vs https, a stale value after a custom-domain change)
now produces a red `publish` job naming the status, not a silent green one under the wrong
byline — see also the sibling candidate on why a bare trailing slash specifically yields `404`
rather than `401`.

The note's other claims — that `AUDIENCE` (`wrangler.toml:19`) and `vars.LYDITE_RELAY_URL` are
two independently-edited values with no shared source — are still true; only the consequence of
their drifting apart has changed. The note's file-line anchors are also stale independent of
this: the action's relay step is no longer a flat `posted=false` + stderr line, and
`lydite-pr.yml`'s `relay:` inputs moved from `:581`/`:591` to `:654`/`:664`.
