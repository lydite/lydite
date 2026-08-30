// Package executil runs external scanner tools and captures their output
// uniformly, so every language package reports results the same way.
package executil

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Result is the outcome of running one external command.
type Result struct {
	Name string
	Args []string
	// Output is everything the command wrote to stdout and stderr. Run
	// streams it live as well, so for most tools it has already reached the
	// terminal by the time anyone reads this field.
	Output string
	// Detail is findings a scanner package derived itself, for a tool whose
	// real report never reaches the terminal at all. Biome is the case that
	// needs it: lydite sends its report to a file with --reporter-file so
	// that Biome's own chatter cannot corrupt the JSON, which means nothing
	// streams and Output holds no findings. A caller that only prints a
	// pass/fail line then shows the developer a failure with no reason
	// attached, in the terminal and in the PR comment alike.
	//
	// Empty for every tool that prints its own findings — reprinting those
	// would duplicate what already streamed.
	Detail string
	Err    error
}

// Ok reports whether the command exited zero.
func (r Result) Ok() bool { return r.Err == nil }

// Run executes name with args in dir, streaming combined stdout+stderr live
// to the terminal while also capturing it into the returned Result.
//
// name and args are always static, hardcoded tool invocations from lydite's
// own scanner packages (cargo, npx, gosec, go, semgrep, pipx) — never built
// from user input or shell-interpreted, so there is no injection surface here.
func Run(ctx context.Context, dir, name string, args ...string) Result {
	return run(ctx, dir, nil, name, args...)
}

// RunEnv is Run with extra "KEY=value" entries appended to the child's
// environment (e.g. GOBIN, to control where `go install` places a binary).
func RunEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) Result {
	return run(ctx, dir, extraEnv, name, args...)
}

// RunQuiet captures output without streaming it.
//
// Run's streaming is right for a scanner, whose findings are the point and
// should reach the terminal and the CI log as they happen. It is wrong for
// plumbing whose output is data the caller parses: `git diff` would print the
// entire patch into the middle of a report, and `git show` of a config file
// would print the file. Those commands are read, not watched.
// Unlike Run, some of RunQuiet's arguments are derived from CLI flags, so a
// caller must resolve a user-supplied revision or path before passing it —
// cmd/lydite's resolveReviewBase is the worked example. What holds
// unconditionally, and is what the annotation below rests on, is that name is
// a fixed literal at every call site and args reach the child as argv with no
// shell, so there is no command injection here; argument injection is the
// caller's to prevent.
func RunQuiet(ctx context.Context, dir, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name is a hardcoded tool name at every call site and args are passed as argv, never shell-interpreted
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return Result{Name: name, Args: args, Output: buf.String(), Err: err}
}

func run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name/args are static, hardcoded tool invocations, never user input or shell-interpreted
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	// Stdout and Stderr being distinct writers makes os/exec copy each stream
	// on its own goroutine, and both feed the same capture buffer — so the
	// buffer writes must be locked.
	var buf bytes.Buffer
	captured := &lockedWriter{w: &buf}
	cmd.Stdout = io.MultiWriter(streamTarget, captured)
	cmd.Stderr = io.MultiWriter(os.Stderr, captured)
	err := cmd.Run()
	return Result{Name: name, Args: args, Output: buf.String(), Err: err}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// streamTarget is where Run mirrors a command's live output. It is os.Stdout
// so a developer watching a scan sees findings as they appear.
//
// Under --json, stdout carries a document and nothing else, so the commands
// point this at stderr instead: the findings still reach the terminal and the
// CI log, and the document stays parseable. Data on stdout, diagnostics on
// stderr — the split every other tool makes.
var streamTarget io.Writer = os.Stdout

// StreamTo redirects live command output. Call it once, from the command
// layer, before any Run; it is not safe to change while a command is running.
func StreamTo(w io.Writer) { streamTarget = w }

// Available reports whether name is resolvable on PATH.
func Available(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
