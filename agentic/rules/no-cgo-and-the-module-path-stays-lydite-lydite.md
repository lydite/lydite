# No cgo, and the module path stays `lydite/lydite`

lydite ships as one statically-linked binary (`CGO_ENABLED=0`) for linux/darwin × amd64/arm64,
so nothing may introduce cgo — that constraint is what rules out every C-backed tree-sitter
binding and decides how Rust and TypeScript are parsed. The module path is `lydite/lydite`,
deliberately not `github.com/lydite/lydite`; it is a considered deviation to be applied to the
other repositories in the org, not a mistake to fix back.
