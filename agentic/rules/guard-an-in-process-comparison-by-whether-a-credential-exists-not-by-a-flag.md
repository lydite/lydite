# Guard an in-process untrusted-build comparison by whether the command always holds a credential, not by mirroring another command's flag-conditioned guard

`review`'s own `computeAPISurfaces` guards the in-process comparison only when the invocation
will also publish (`--publish` or no `--status-out`), because a `review` run that only renders
documents for another step to post holds no writing credential of its own — the guard there is
conditional because the credential is. `clearance` is not that shape: `runClearance` requires
`GITHUB_TOKEN` unconditionally (reading the head, the commenter's permission, the standing
referral, and posting the reply all need it), so every invocation of `clearance` holds a
credential whether or not it also posts the status directly. Copying `review`'s conditional guard
onto a command like this leaves one branch — the one that "only renders" — running a component's
own build code beside a token it does hold, because the premise the condition tests for
(`--status-out` implies no credential) is false for this command.

## Applies to

Any new `cmd/lydite` command or flag that runs an untrusted API-surface or build-code comparison
in-process, and any change to `clearedDecision`'s or `computeAPISurfaces`'s guard condition.

## Example

```go
// wrong: copies review's condition onto a command that always holds a token
surfaces, err = computeAPISurfaces(ctx, cmd, opt.dir, baseSHA, opt.statusOut == "")

// right: this command always holds the credential, so the guard is unconditional;
// --surfaces is the only route around it, reading a comparison a separate,
// credential-free job already made
surfaces, err = computeAPISurfaces(ctx, cmd, opt.dir, baseSHA, true)
```

Reasoning: [`docs/adr/0057-a-clearance-computes-its-fingerprint-in-a-job-holding-no-credential.md`](../../docs/adr/0057-a-clearance-computes-its-fingerprint-in-a-job-holding-no-credential.md)
and [`agentic/references/referral-and-clearance.md`](../references/referral-and-clearance.md).
