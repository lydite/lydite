---
description: "A tarball unpacked to a wrapper node runs under is not the same trust level as one whose contents are put directly on PATH and executed — the latter is verified against the registry's SHA-512 integrity only, never the SHA-1 shasum fallback."
---

# Verify a binary placed directly on PATH with SHA-512 only, never the SHA-1 `shasum` fallback

`verifyDist` accepts a registry package's legacy SHA-1 `shasum` when no SHA-512 `integrity` is
published, because that fallback exists for packages predating npm's integrity field and the
tarball it verifies still lands under a wrapper — `unpackManager`'s node-invoking script — that
lydite's own provisioned runtime executes. A native binary installed straight onto PATH with no
such wrapper, the way `downloadPnpm` installs pnpm ≥ 12's `@pnpm/exe.<os>-<arch>` package, is
executed directly the moment a component's suite runs it: the SHA-1 fallback is not strength
enough for bytes about to run with no intermediate runtime to have already been trusted. Refuse
a release with no published SHA-512 rather than falling back to SHA-1 for it.

## Applies to

`internal/toolchain/provision.go`'s `downloadPnpm`/`hasSHA512`/`verifySHA512`, and any future
provisioning path that places a fetched artifact directly on PATH rather than unpacking it under
a wrapper another trusted runtime executes.

## Example

```go
// wrong: falls back to the weaker legacy digest for a binary about to be
// placed directly on PATH and executed
if err := verifyDist(exeData, dist); err != nil {
    return err
}

// right: refuse outright when the registry publishes no SHA-512 to check
// the binary against
if !hasSHA512(dist.Integrity) {
    return fmt.Errorf("%s@%s publishes no sha512 integrity to verify its binary against", exe, version)
}
if _, err := verifySHA512(exeData, dist); err != nil {
    return err
}
```

Reasoning: [ADR 0075](../../docs/adr/0075-pnpm-is-provisioned-as-its-native-binary-and-a-pin-below-12-is-refused.md)
and [`agentic/references/toolchains.md`](../references/toolchains.md).
