# The project is renamed from bulwark to lydite

`bulwark` is unavailable on GitHub — held since 2014 by a dormant personal account with no
repositories and no activity since 2017. GitHub does not reclaim usernames for inactivity, so the
name cannot be obtained; only a suffixed org (`bulwarkhq`, `bulwarkdev`) was available.

That alone did not force a rename. The squatting account is not a competing product, so there was
no brand collision to resolve, and keeping the name would have cost nothing but a suffix. The
rename was chosen because the project is already moving to a fresh repository at 0.1.0 with a new
module path and a renamed state branch — so the marginal cost of the name is at its lowest it will
ever be, and it buys the clean topology the move was for: `lydite/lydite` and `lydite/actions`,
with no suffix and one uncontested name across GitHub, npm, PyPI, and a matching domain.

## Why lydite

Lydite — Lydian stone — is the black touchstone an assayer rubs gold against; the colour of the
streak reveals the metal's purity. It is, literally, an instrument for testing quality against a
standard, which is what this tool is.

Fortification words were deliberately abandoned rather than re-mined. That namespace is the most
picked-over in security tooling — which is why `bulwark` was taken in the first place, along with
`aegis`, `bastion`, `rampart`, `redoubt`, `parapet`, `portcullis`, and every other candidate
tested. Assaying and measurement proved almost entirely unclaimed by comparison.

The accepted cost is that the name is opaque: nobody knows what a lydite is without being told.
This is the genre norm rather than an exception — Snyk, Trivy, Grype, Syft, and Nuclei are equally
meaningless on sight — and opacity is precisely what buys an uncontested namespace everywhere at
once. `tallystick` was the runner-up, a better metaphor for the ledger in ADR 0009 but four
characters longer for something typed daily.

## What the rename touches

The binary, `.bulwark.yml` -> `.lydite.yml` in every consuming repo, the state branch, the
`BULWARK_*` environment variables, the install script and its URLs, the goreleaser owner, the
sticky PR-comment header (changing it orphans existing comments rather than replacing them), the
logo, and every reference in `README.md`, `AGENTS.md`, and the `bump-version` skill.

One coupling is not local to this repository: `.gt-repo.yaml` carries a `bulwark:` key, so gt knows
this tool by name and must be updated alongside. Being the only consumer is what makes all of this
a one-time mechanical change rather than a migration.
