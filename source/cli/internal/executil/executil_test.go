package executil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Run captures stdout and stderr into one combined buffer, and os/exec copies
// each stream on its own goroutine — so a child writing to both concurrently
// used to race on that shared buffer (caught by -race the first time a test
// exercised a chatty-on-both-streams command, git push). The writes must be
// synchronized; this test is only meaningful under -race.
func TestRunCapturesConcurrentStdoutAndStderrWithoutRacing(t *testing.T) {
	r := Run(context.Background(), t.TempDir(), "sh", "-c",
		"for i in $(seq 1 200); do echo out$i; echo err$i >&2; done")
	if !r.Ok() {
		t.Fatalf("Run: %v", r.Err)
	}
	if !strings.Contains(r.Output, "out200") || !strings.Contains(r.Output, "err200") {
		t.Errorf("combined output missing a stream's tail: %q", r.Output[max(0, len(r.Output)-80):])
	}
}

// Under --json stdout carries a document and nothing else, so a tool's live
// output has to move rather than corrupt it. It moves to stderr rather than
// being discarded: losing the findings would be a worse trade than losing
// their placement.
func TestStreamToRedirectsLiveOutput(t *testing.T) {
	var buf bytes.Buffer
	StreamTo(&buf)
	t.Cleanup(func() { StreamTo(os.Stdout) })

	r := Run(context.Background(), t.TempDir(), "echo", "a finding")
	if !r.Ok() {
		t.Fatalf("echo: %v", r.Err)
	}
	if !strings.Contains(buf.String(), "a finding") {
		t.Errorf("live output did not reach the redirected target, got %q", buf.String())
	}
	// Still captured, so a caller that reads Result.Output is unaffected by
	// where the stream went.
	if !strings.Contains(r.Output, "a finding") {
		t.Errorf("Result.Output lost the output, got %q", r.Output)
	}
}

// RunQuiet is for plumbing whose output is data the caller parses. It must
// not reach the terminal at all: `git rev-parse` printing two SHAs into the
// middle of a report is what this exists to prevent.
func TestRunQuietStreamsNothing(t *testing.T) {
	var buf bytes.Buffer
	StreamTo(&buf)
	t.Cleanup(func() { StreamTo(os.Stdout) })

	r := RunQuiet(context.Background(), t.TempDir(), "echo", "plumbing")
	if !r.Ok() {
		t.Fatalf("echo: %v", r.Err)
	}
	if buf.Len() != 0 {
		t.Errorf("RunQuiet streamed %q", buf.String())
	}
	if !strings.Contains(r.Output, "plumbing") {
		t.Errorf("RunQuiet lost the output, got %q", r.Output)
	}
}

// RunQuiet's callers parse Output as data — a YAML document, a patch — so
// stderr has to stay out of it. Git warns about an unreadable gitconfig on
// stderr, and prepended to an exemptions file that turns a valid config into
// a parse error.
func TestRunQuietKeepsStderrOutOfOutput(t *testing.T) {
	r := RunQuiet(context.Background(), t.TempDir(), "sh", "-c", "echo data; echo noise >&2")
	if !r.Ok() {
		t.Fatalf("sh: %v", r.Err)
	}
	if strings.TrimSpace(r.Output) != "data" {
		t.Errorf("Output = %q, want just the stdout data", r.Output)
	}
	if strings.TrimSpace(r.Stderr) != "noise" {
		t.Errorf("Stderr = %q, want the stderr text kept separately", r.Stderr)
	}
}

// os/exec resolves a bare program name against *this* process's PATH when the
// command is constructed; cmd.Env is applied afterwards and has no say in it.
// Without resolving here, a toolchain lydite provisioned and put on the
// child's PATH is one the child could use and the lookup cannot find — `npm
// ci: executable file not found in $PATH`, moments after lydite reported
// installing the Node that holds it.
func TestRunFindsABinaryOnlyOnTheChildsPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lydite-only-here")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho found\n"), 0o700); err != nil { //nolint:gosec // a test fixture that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")

	res := RunQuietEnv(context.Background(), "", []string{"PATH=" + dir}, "lydite-only-here")
	if !res.Ok() {
		t.Fatalf("a binary on the child's PATH was not found: %v", res.Err)
	}
	if !strings.Contains(res.Output, "found") {
		t.Errorf("output = %q, want the child's own", res.Output)
	}
}

// A name no PATH entry holds is handed to os/exec untouched, so the failure
// stays its own message naming the program the caller asked for rather than a
// path lydite invented.
func TestResolveLeavesAnUnfoundNameAlone(t *testing.T) {
	if got := resolve("", "definitely-not-a-program", []string{"PATH=/nonexistent"}); got != "definitely-not-a-program" {
		t.Errorf("resolve = %q, want the name unchanged", got)
	}
}

// An absolute path is what every pinned scanner is invoked by, and rewriting
// one against PATH could only find a different binary of the same name.
func TestResolveLeavesAPathAlone(t *testing.T) {
	want := filepath.Join(string(os.PathSeparator), "usr", "bin", "env")
	if got := resolve("", want, []string{"PATH=/nonexistent"}); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

// A relative PATH entry is relative to the child's working directory, not to
// lydite's. A JavaScript component declaring `PATH: node_modules/.bin` means
// its own node_modules, and resolving from here would find the wrong binary in
// a monorepo — or none, and then hand os/exec a bare name it looks up on a
// PATH the entry was never part of.
func TestResolveReadsARelativePathEntryFromTheChildsDirectory(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tools", "lydite-relative")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho found\n"), 0o700); err != nil { //nolint:gosec // a test fixture that has to be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")

	if got := resolve(dir, "lydite-relative", []string{"PATH=tools"}); got != bin {
		t.Errorf("resolve = %q, want %q", got, bin)
	}
	// Absolute, and this is the half an absolute `dir` cannot show: exec.Cmd
	// evaluates a relative Path against Dir, so returning the joined relative
	// path would apply dir twice and look for `web/web/tools/x`.
	if !filepath.IsAbs(resolve(dir, "lydite-relative", []string{"PATH=tools"})) {
		t.Error("resolve returned a relative path, which exec would resolve against Dir a second time")
	}
	// From anywhere else the same entry names nothing, and the bare name is
	// what os/exec should report on.
	if got := resolve(t.TempDir(), "lydite-relative", []string{"PATH=tools"}); got != "lydite-relative" {
		t.Errorf("resolve = %q, want the name unchanged where the entry holds nothing", got)
	}
}

// The failure a test passing an absolute directory cannot see: with a relative
// `dir` — which is what `--dir .` and the documented `--dir ..` give — a
// relative PATH entry has to be joined once, not applied twice by exec.
func TestARelativeWorkingDirectoryResolvesARelativePathEntryOnce(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "web", "tools"), 0o750); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(base, "web", "tools", "lydite-rel-run")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho found\n"), 0o700); err != nil { //nolint:gosec // a test fixture that has to be executable
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")

	res := RunQuietEnv(context.Background(), "web", []string{"PATH=tools"}, "lydite-rel-run")
	if !res.Ok() {
		t.Fatalf("a binary on the child's relative PATH was not found: %v", res.Err)
	}
	if !strings.Contains(res.Output, "found") {
		t.Errorf("output = %q, want the child's own", res.Output)
	}
}

// helperAllocEnv carries the size, in MiB, the helper child below allocates.
// Its presence is also what tells that child apart from an ordinary run of
// the same test binary.
const helperAllocEnv = "LYDITE_EXECUTIL_ALLOC_MIB"

// TestExecutilAllocatingChild is the child a memory bound is measured
// against, re-executed out of this test binary so the measurement needs no
// toolchain to build one.
func TestExecutilAllocatingChild(t *testing.T) {
	mib, err := strconv.Atoi(os.Getenv(helperAllocEnv))
	if err != nil {
		t.Skip("not the allocating child")
	}
	// Every byte is written, because a page is counted against RLIMIT_DATA
	// when it is mapped and against ru_maxrss only when it is touched — a
	// child that allocates without touching dies at a peak nothing recognises
	// as the bound. The writes go through copy so the race detector
	// instruments a chunk rather than a byte.
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = 1
	}
	held := make([][]byte, 0, mib)
	for range mib {
		b := make([]byte, len(chunk))
		copy(b, chunk)
		held = append(held, b)
	}
	fmt.Printf("allocated %d MiB\n", len(held))
}

// allocateBounded runs the child above under maxMemory bytes, asking it for
// mib MiB.
func allocateBounded(t *testing.T, maxMemory int64, mib int) Result {
	t.Helper()
	return RunOutputBounded(context.Background(), t.TempDir(),
		[]string{helperAllocEnv + "=" + strconv.Itoa(mib)}, io.Discard, maxMemory,
		os.Args[0], "-test.run=^TestExecutilAllocatingChild$")
}

// A bound that is reached kills the run, and says so as a bound rather than
// as an ordinary failure: every runtime dies differently at the ceiling and
// all of them exit non-zero, so the exit status alone would make a mutant
// that allocated without stopping indistinguishable from one whose tests
// failed.
func TestRunOutputBoundedKillsAChildThatReachesTheLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_DATA is settable only on linux — darwin's setrlimit refuses it with EINVAL at any value — so this is proven by the linux CI job")
	}
	const limit = 1 << 30
	r := allocateBounded(t, limit, 1536)

	if r.Ok() {
		t.Fatalf("a child allocating 1.5GiB survived a %d byte bound, peak %d", limit, r.MaxRSS)
	}
	if !r.MemoryBounded {
		t.Fatal("MemoryBounded is false, so the bound never reached the child")
	}
	if !r.HitMemoryLimit() {
		t.Errorf("HitMemoryLimit is false: peak %d against limit %d", r.MaxRSS, r.MemoryLimit)
	}
	if r.MemoryLimit != limit {
		t.Errorf("MemoryLimit = %d, want %d", r.MemoryLimit, limit)
	}
}

// A run that stays under its bound is untouched by it, and reports a peak the
// bound is compared against — without which a failure for an unrelated reason
// would read as a failure at the ceiling.
func TestRunOutputBoundedLeavesAChildUnderTheLimitAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_DATA is settable only on linux — darwin's setrlimit refuses it with EINVAL at any value — so this is proven by the linux CI job")
	}
	const limit = 1 << 30
	r := allocateBounded(t, limit, 16)

	if !r.Ok() {
		t.Fatalf("a child allocating 16MiB died under a 1GiB bound: %v\n%s", r.Err, r.Output)
	}
	if !r.MemoryBounded {
		t.Error("MemoryBounded is false, so the bound never reached the child")
	}
	if r.HitMemoryLimit() {
		t.Errorf("HitMemoryLimit on a run that succeeded, peak %d against limit %d", r.MaxRSS, r.MemoryLimit)
	}
	if r.MaxRSS < 8<<20 {
		t.Errorf("MaxRSS = %d, want the bytes a 16MiB child touched — ru_maxrss is kilobytes on linux and reading it raw is a factor of 1024", r.MaxRSS)
	}
}

// Three states have to be told apart, and a bound requested on a platform
// that cannot set one is the state that matters: reported as enforced it
// would render the green of a bound that held.
func TestRunOutputBoundedReportsWhetherTheBoundReachedTheChild(t *testing.T) {
	r := RunOutputBounded(context.Background(), t.TempDir(), nil, io.Discard, 1<<30, "echo", "bounded")

	if !r.Ok() {
		t.Fatalf("echo: %v", r.Err)
	}
	if r.MemoryLimit != 1<<30 {
		t.Errorf("MemoryLimit = %d, want the bound the caller asked for", r.MemoryLimit)
	}
	if want := runtime.GOOS == "linux"; r.MemoryBounded != want {
		t.Errorf("MemoryBounded = %v on %s, want %v", r.MemoryBounded, runtime.GOOS, want)
	}
}

// Every other lydite command runs through this package and asks for no
// bound. Nothing about such a run is limited, and the absence of a request is
// distinct from a request nothing applied.
func TestRunOutputWithoutABoundIsUnbounded(t *testing.T) {
	var buf bytes.Buffer
	r := RunOutput(context.Background(), t.TempDir(), nil, &buf, "sh", "-c", "echo out; echo err >&2")

	if !r.Ok() {
		t.Fatalf("sh: %v", r.Err)
	}
	if r.MemoryLimit != 0 || r.MemoryBounded {
		t.Errorf("MemoryLimit = %d, MemoryBounded = %v, want no bound requested", r.MemoryLimit, r.MemoryBounded)
	}
	if r.HitMemoryLimit() {
		t.Error("HitMemoryLimit on a run nothing bounded")
	}
	if r.MaxRSS <= 0 {
		t.Error("MaxRSS = 0, want the peak of a run that happened")
	}
	if !strings.Contains(buf.String(), "out") || !strings.Contains(buf.String(), "err") {
		t.Errorf("both streams should reach the writer, got %q", buf.String())
	}
	if !strings.Contains(r.Output, "out") || !strings.Contains(r.Output, "err") {
		t.Errorf("both streams should be captured, got %q", r.Output)
	}
}

// The peak against the limit is the whole discriminator, so its boundary is
// part of the meaning: a run whose peak reaches the fraction has hit the
// bound, and one a byte under it has not.
func TestHitMemoryLimitReadsThePeakAgainstTheLimit(t *testing.T) {
	const limit = 3 << 30
	for _, tc := range []struct {
		name string
		r    Result
		want bool
	}{
		{"at the threshold", Result{Err: os.ErrClosed, MemoryBounded: true, MemoryLimit: limit, MaxRSS: limit / 3}, true},
		{"a byte under it", Result{Err: os.ErrClosed, MemoryBounded: true, MemoryLimit: limit, MaxRSS: limit/3 - 1}, false},
		{"well over it", Result{Err: os.ErrClosed, MemoryBounded: true, MemoryLimit: limit, MaxRSS: limit - 1}, true},
		{"a failure that never approached the bound", Result{Err: os.ErrClosed, MemoryBounded: true, MemoryLimit: limit, MaxRSS: 12 << 20}, false},
		{"a bound the platform could not apply", Result{Err: os.ErrClosed, MemoryLimit: limit, MaxRSS: limit}, false},
		{"no bound asked for", Result{Err: os.ErrClosed, MaxRSS: limit}, false},
		{"a run that succeeded", Result{MemoryBounded: true, MemoryLimit: limit, MaxRSS: limit}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.HitMemoryLimit(); got != tc.want {
				t.Errorf("HitMemoryLimit = %v, want %v (peak %d, limit %d)", got, tc.want, tc.r.MaxRSS, tc.r.MemoryLimit)
			}
		})
	}
}
