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

	"lydite/lydite/internal/finding"
)

// Env is the two environments a language check runs with, which are
// deliberately not the same one.
//
// Check is what the tool is invoked with: the component's resolved toolchain
// and the environment its declaration asks for, because a repository's own
// build needs what its declaration says — SQLX_OFFLINE, CGO_ENABLED, PROTOC.
//
// Install is what lydite provisions its *own* pinned tools with, and carries
// nothing the scanned repository supplied. `go install`, `cargo install` and
// `npm ci` read GOPROXY, GOSUMDB, CARGO_REGISTRIES_* and npm_config_registry,
// so a declaration that reached them would choose where lydite fetches the
// security scanner it is about to run — and the result is cached under a key
// naming the tool's version, so one poisoned build would be reused by every
// later run and, on a runner sharing ~/.cache/lydite, by other repositories.
// A repository may say how its own code builds; it may not say where lydite's
// scanners come from.
type Env struct {
	Check   []string
	Install []string
}

// Result is the outcome of running one external command.
type Result struct {
	Name string
	Args []string
	// Output is everything the command wrote to stdout and stderr. Run
	// streams it live as well, so for most tools it has already reached the
	// terminal by the time anyone reads this field.
	Output string
	// Detail is findings a scanner package derived itself, rendered when a
	// caller needs more than a pass/fail line to explain a failing row. Two
	// things put text here: a tool whose real report never reaches the
	// terminal at all — Biome sends its report to a file with
	// --reporter-file so its own chatter cannot corrupt the JSON, which
	// means nothing streams and Output holds no findings — and clippy,
	// cargo-audit and cargo-deny, which run once in JSON mode with no
	// second, richer terminal rendering to lose, so Detail is rendered from
	// Findings instead of left empty.
	//
	// Empty for a tool whose own findings already reach a reader on the
	// terminal with nothing more needed: gosec's, Semgrep's, gitleaks's and
	// govulncheck's.
	Detail string
	// Findings are the located claims this check made, as data. A check that
	// parses a structured report sets them and renders Detail from them, so
	// the prose a human reads and the data a consumer anchors are one
	// derivation. Empty for a tool whose output lydite does not parse.
	Findings []finding.Finding
	// Stderr is what the command wrote to stderr, kept apart from Output by
	// RunQuiet only. Run deliberately merges the two, because for a scanner
	// they are one stream of findings; RunQuiet's callers parse Output as
	// data, and a git warning about an unreadable gitconfig prepended to a
	// YAML document turns a valid exemptions file into a parse error.
	Stderr string
	Err    error
	// MaxRSS is the peak resident set of the command and everything it
	// forked, in bytes on every OS. ru_maxrss is kilobytes on Linux and
	// bytes on Darwin — the same 256MiB child reports 268,912 on one and
	// 274,432,000 on the other — so the conversion happens here rather than
	// leaving every reader a factor of 1024 to get right.
	MaxRSS int64
	// MemoryLimit is the ceiling the caller asked for, in bytes, and zero
	// when no bound was asked for at all.
	MemoryLimit int64
	// MemoryBounded reports whether that ceiling actually reached the child.
	// The two are separate because three states have to be told apart: no
	// bound requested, a bound requested and enforced, and a bound requested
	// that the platform refused — Darwin's setrlimit rejects RLIMIT_DATA at
	// any value. The third renders as its own outcome, because a bound
	// quietly not applied otherwise reports the green of one that held.
	MemoryBounded bool
}

// Ok reports whether the command exited zero.
func (r Result) Ok() bool { return r.Err == nil }

// HitMemoryLimit reports whether the command died at its memory bound rather
// than for some unrelated reason.
//
// The exit status cannot tell the two apart: every language dies differently
// at the ceiling — a Go runtime's own out-of-memory error, a failed
// allocation, a signal — and all of them exit non-zero. The peak against the
// limit is the number that separates them, and it is a fraction rather than
// an equality because RLIMIT_DATA bounds the mappings a process holds while
// ru_maxrss counts the pages it touched: a runtime that dies on a refused
// mmap has reserved more than it ever faulted in. A Go child killed at a
// 1GiB ceiling reports a peak of 0.94 of it, and the same child under the
// race detector 0.58 — the shadow mappings count against the bound and are
// never all resident, which is the shape a mutated Go suite runs in. A third
// is the threshold because the ceiling a mutation run derives is a multiple
// of the baseline's own peak, so a run that failed for its own reasons sits
// well under it: that child failing under a bound it never approached reports
// 0.01.
func (r Result) HitMemoryLimit() bool {
	return r.Err != nil && r.MemoryBounded && r.MaxRSS*3 >= r.MemoryLimit
}

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
	return runTo(ctx, dir, extraEnv, out, out, 0, name, args...)
}

// RunOutputBounded is RunOutput with a ceiling, in bytes, on the memory the
// command and everything it forks may hold.
//
// It exists for a mutant's suite. A deadline alone is not a bound on one: a
// removed statement on a loop counter turns a bounded append into an
// unbounded one, and the runner's memory is exhausted long before a timeout
// derived from the baseline fires — which kills the agent rather than the
// step, and loses every result the shard had. The bound reaches the child
// only where the platform has one to set, so the caller reads MemoryBounded
// before treating it as held.
func RunOutputBounded(ctx context.Context, dir string, extraEnv []string, out io.Writer, maxMemory int64, name string, args ...string) Result {
	return runTo(ctx, dir, extraEnv, out, out, maxMemory, name, args...)
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
	cmd := exec.CommandContext(ctx, resolve(dir, name, extraEnv), args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name is a hardcoded tool name at every call site and args are passed as argv, never shell-interpreted
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	res := Result{Name: name, Args: args, Output: out.String(), Stderr: errBuf.String(), Err: err}
	if cmd.ProcessState != nil {
		res.MaxRSS = peakRSS(cmd.ProcessState.SysUsage())
	}
	return res
}

// RunQuietIsolatedEnv is RunQuiet with the child's environment being env
// entirely, rather than env layered onto this process's own the way every
// other Run* function here does.
//
// It exists for a subprocess that builds and executes code the tree under
// scan controls — a Rust crate's build.rs, a proc-macro — where the calling
// process's own environment may carry a credential that code must never
// reach. Every other Run* function inherits this process's environment
// because the tools they invoke are lydite's own, running in a directory the
// scanned repository does not get to execute anything in; this one exists
// because api_surface's Rust comparison is the first invocation in this
// package that does.
func RunQuietIsolatedEnv(ctx context.Context, dir string, env []string, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, resolve(dir, name, env), args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name is a hardcoded tool name at every call site and args are passed as argv, never shell-interpreted
	cmd.Dir = dir
	// A nil Env means os/exec inherits this process's own — the one thing this
	// function exists to refuse — so a caller passing none gets an empty
	// environment rather than every one of this process's variables.
	cmd.Env = env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	res := Result{Name: name, Args: args, Output: out.String(), Stderr: errBuf.String(), Err: err}
	if cmd.ProcessState != nil {
		res.MaxRSS = peakRSS(cmd.ProcessState.SysUsage())
	}
	return res
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
func resolve(dir, name string, extraEnv []string) string {
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
	for _, entry := range filepath.SplitList(path) {
		if entry == "" {
			continue
		}
		// A relative PATH entry is relative to the child's working directory,
		// not to lydite's — a JavaScript component declaring
		// `PATH: node_modules/.bin` means its own node_modules. Stat-ing it
		// from here would find the wrong binary in a monorepo, or none, and
		// then hand os/exec a bare name it would look up on a PATH the entry
		// was never part of.
		if !filepath.IsAbs(entry) {
			entry = filepath.Join(dir, entry)
		}
		candidate := filepath.Join(entry, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		// Absolute, because exec.Cmd evaluates a relative Path against Dir —
		// so returning the path this stat'ed would apply dir twice and look
		// for `web/web/tools/x`. The stat is from lydite's working
		// directory and the child's Path must not be.
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return candidate
		}
		return abs
	}
	return name
}

func run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) Result {
	return runTo(ctx, dir, extraEnv, streamTarget, os.Stderr, 0, name, args...)
}

func runTo(ctx context.Context, dir string, extraEnv []string, stdout, stderr io.Writer, maxMemory int64, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, resolve(dir, name, extraEnv), args...) // #nosec G204 -- nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command -- name/args are static, hardcoded tool invocations, or a command from the scanned repository's own declaration; never shell-interpreted
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
	res := Result{Name: name, Args: args, MemoryLimit: maxMemory}
	if err := cmd.Start(); err != nil {
		res.Err = err
		return res
	}
	// After Start, because the limit is set on the child's own process and
	// there is no process to set it on before that. The window this leaves is
	// the child's exec, which allocates nothing a ceiling meant for a test
	// suite would catch.
	if maxMemory > 0 {
		res.MemoryBounded = limitMemory(cmd.Process.Pid, maxMemory) == nil
	}
	res.Err = cmd.Wait()
	res.Output = buf.String()
	if cmd.ProcessState != nil {
		res.MaxRSS = peakRSS(cmd.ProcessState.SysUsage())
	}
	return res
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
