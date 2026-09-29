---
about: a golangci-lint built with an older Go than the module's toolchain refuses to load the config; CI pins v2.12.2
saw:
  - .github/workflows/ci-build.yml
  - source/cli/go.mod
---

`ci-build.yml` runs `golangci-lint-action` with `version: v2.12.2`. A binary built with go1.25 (v2.5.0) fails with "can't load config" against the module's go1.26 toolchain. `GOTOOLCHAIN=go1.26.6 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2` gives a matching build in `$(go env GOPATH)/bin`, which must precede any older one on PATH.
