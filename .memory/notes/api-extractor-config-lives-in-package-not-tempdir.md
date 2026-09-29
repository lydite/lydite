---
name: api-extractor-config-lives-in-package-not-tempdir
kind: gotcha
description: api-extractor's configuration file has to sit inside the package directory it analyses, not a temp directory, because it locates the project's package.json by walking up from the config file's own location rather than taking a project root argument.
anchors:
  - path: source/cli/internal/tsapisurface/tsapisurface.go
    blob: 97cf980e6f5d
confidence: verified
---

`internal/tsapisurface`'s `read` function writes api-extractor's generated config into the package
directory itself (`os.CreateTemp(pkgDir, configPattern)`, confirmed at `tsapisurface.go:456`), not
into a scratch/temp directory. A configuration placed outside the package fails with "Unable to find
a package.json file for the project being analyzed" — api-extractor resolves the project root by
walking up the filesystem from wherever its config file lives, it does not take the project root as
an explicit argument. The report output itself (`reportDir`/`tempDir`) is a real temp directory;
only the config file has this constraint. The config file is removed on every return path, including
a refusal.
</content>
