---
name: relay-audience-mismatch-degrades-silently
kind: gotcha
description: The relay's AUDIENCE and the workflow's requested audience are two independently-edited values with no shared source, and a mismatch falls back to the bot token forever without going red.
anchors:
  - path: source/cloud-services/pr-relay/wrangler.toml
    blob: 5e7b4557cc65
  - path: source/cloud-services/pr-relay/src/index.ts
    blob: a660487f0cc0
  - path: source/cloud-services/libs/github-app/src/oidc.ts
    blob: c3affd4a9859
  - path: .github/actions/lydite-comment/action.yml
    blob: dafe821c9fd6
  - path: .github/workflows/lydite-pr.yml
    blob: 1de36959efb1
confidence: verified
---

`verifyActionsToken` rejects a token whose `claims.aud` is not the relay's own
(`libs/github-app/src/oidc.ts:94-95`, "the token was minted for another audience").
The expected value is passed as `env.AUDIENCE` (`pr-relay/src/index.ts:99`), a hardcoded
Worker var: `AUDIENCE = "https://pr.lydite.org"` (`pr-relay/wrangler.toml:19`).

The caller mints the token against a different, independently-set string: the action
requests `"${ACTIONS_ID_TOKEN_REQUEST_URL}&audience=${RELAY}"`
(`.github/actions/lydite-comment/action.yml:65`) where `RELAY` is
`vars.LYDITE_RELAY_URL`, a GitHub Actions repository variable passed in at
`.github/workflows/lydite-pr.yml:581` and again at `:591`.

Nothing ties the two together — no shared constant, no check, no comment on either side
naming the other. One lives in a file deployed to Cloudflare, the other in repo
settings, and they must be byte-identical.

The failure is silent and permanent. On any drift (trailing slash, http vs https, a
stale value after a custom-domain change) the relay answers 401, and the action's relay
step just writes `posted=false` and logs `the relay answered ${status}; falling back to
this workflow's token` to stderr (`action.yml:82-83`), then the `posted != 'true'` step
(`action.yml:86`) comments as `github-actions[bot]`. That is indistinguishable in the
rendered PR comment from the intended app-not-installed fallback, and nothing goes red —
the fallback is a supported, permanently-green path, so a misconfiguration here can sit
unnoticed indefinitely. Check the Worker var against the repo var directly; the comment
appearing is not evidence the relay ran.
