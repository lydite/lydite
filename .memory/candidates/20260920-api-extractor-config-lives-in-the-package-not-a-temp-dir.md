---
about: api-extractor's configuration file has to sit inside the package directory it analyses, not a temp directory, because it locates the project's package.json by walking up from the config file
saw:
  - source/cli/internal/tsapisurface/tsapisurface.go
---

`internal/tsapisurface`'s `read` function (source/cli/internal/tsapisurface/tsapisurface.go,
see `writeConfig` and the `configPattern` comment) writes api-extractor's generated
`api-extractor.json`-equivalent config into the package directory itself
(`os.CreateTemp(pkgDir, configPattern)`), not into a scratch/temp directory. A configuration
placed outside the package fails with "Unable to find a package.json file for the project
being analyzed" — api-extractor resolves the project root by walking up the filesystem from
wherever its config file lives, it does not take the project root as an explicit argument.
The report output itself (`reportDir`/`tempDir`) is a real temp directory; only the config
file has this constraint. The config file is removed on every return path, including a
refusal.
