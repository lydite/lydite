// Package executil runs external scanner tools and captures their output
// uniformly, so every language package reports results the same way.
package executil

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// Stderr is what the command wrote to stderr, kept apart from Output by
	// RunQuiet only. Run deliberately merges the two, because for a scanner
	// they are one stream of findings; RunQuiet's callers parse Output as
	// data, and a git warning about an unreadable gitconfig prepended to a
	// YAML document turns a valid exemptions file into a parse error.
	Stderr string
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

// RunOutput runs a command writing its combined stdout and stderr to out, and
// captures the same into the returned Result.
//
// It exists for output that is only worth reading when it fails. A scanner's
// findings are the point and stream live through Run; a test suite's output is
// thousands of lines of passing tests, and a CI log carrying all of it buries
// the one component that failed among the ones that did not. The caller
// decides what out is — a log file, or a log file and the terminal both.
func RunOutput(ctx context.Context, dir string, extraEnv []string, out io.Writer, name string, args ...string) Result {
	return runTo(ctx, dir, extraEnv, out, out, name, args...)
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
	return RunQuietEnv(ctx, dir, nil, name, args...)
}

// RunQuietEnv is RunQuiet with extra "KEY=value" entries appended to the
// child's environment.
//
// It exists because a git commit needs an author it must not read from the
// machine, and its own output is chatter: `git commit` prints the commit it
// made, which is data nobody asked for in the middle of a report — and, under
// --json, in the middle of the document.
func RunQuietEnv(ctx context.Context, dir string, extraEnv []string, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, resolve(name, extraEnv), args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name is a hardcoded tool name at every call site and args are passed as argv, never shell-interpreted
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return Result{Name: name, Args: args, Output: out.String(), Stderr: errBuf.String(), Err: err}
}

// resolve finds name on the PATH the child is being given, rather than on the
// one this process happens to have.
//
// os/exec resolves a bare program name when the command is constructed, using
// this process's own PATH — cmd.Env is applied afterwards and has no bearing
// on it. So a toolchain lydite provisioned and put on the child's PATH is one
// the child can use and the lookup cannot find: `npm ci` fails with
// "executable file not found in $PATH" moments after lydite reported
// installing the Node that holds it. Resolving here is what makes the
// environment lydite composes and the binary it launches the same answer.
//
// A name that is already a path is returned untouched, and so is one no PATH
// entry holds — the second case so the failure stays os/exec's own message,
// which names the program the caller asked for.
func resolve(name string, extraEnv []string) string {
	if strings.ContainsRune(name, os.PathSeparator) {
		return name
	}
	path := ""
	// Last wins, matching how a process reads duplicate keys out of its own
	// environment.
	for _, kv := range extraEnv {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if path == "" {
		return name
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return name
}

func run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) Result {
	return runTo(ctx, dir, extraEnv, streamTarget, os.Stderr, name, args...)
}

func runTo(ctx context.Context, dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, resolve(name, extraEnv), args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name/args are static, hardcoded tool invocations, or a command from the scanned repository's own declaration; never shell-interpreted
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	// Stdout and Stderr being distinct writers makes os/exec copy each stream
	// on its own goroutine, and both feed the same capture buffer — so the
	// buffer writes must be locked. The destinations are locked too, since
	// they may be the same writer.
	var buf bytes.Buffer
	captured := &lockedWriter{w: &buf}
	out, errOut := &lockedWriter{w: stdout}, &lockedWriter{w: stderr}
	if stdout == stderr {
		errOut = out
	}
	cmd.Stdout = io.MultiWriter(out, captured)
	cmd.Stderr = io.MultiWriter(errOut, captured)
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
