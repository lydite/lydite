// The version pin for gotestsum, which lydite runs a Go component's
// instrumented suite through so that suite emits a JUnit report.
//
// A separate module for the reason internal/golang/go-pin is one: declaring
// the tool in lydite's own go.mod works and drags its dependency graph into
// code lydite never links, which then generates Dependabot pull requests
// against a build that does not use any of it.
//
// Its own module rather than an entry in go-pin, because a pin is colocated
// with the package that uses it — this one is a test runner's wrapper, not a
// scanner — and because the two are installed under different rules: go-pin's
// tools are keyed by the resolved Go toolchain that will build them, and this
// one is not.
//
// Nothing here is ever built. It exists so Dependabot's `gomod` ecosystem can
// see the version, and is read by internal/pins so a bump that is not mirrored
// into runner.go's constant fails the build.
module lydite/lydite/internal/runner/gotestsum-pin

go 1.26.0

tool gotest.tools/gotestsum

require (
	github.com/bitfield/gotestdox v0.2.2 // indirect
	github.com/dnephin/pflag v1.0.7 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/mod v0.27.0 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
	golang.org/x/term v0.35.0 // indirect
	golang.org/x/text v0.17.0 // indirect
	golang.org/x/tools v0.36.0 // indirect
	gotest.tools/gotestsum v1.13.0 // indirect
)
