package executil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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
	if got := resolve("definitely-not-a-program", []string{"PATH=/nonexistent"}); got != "definitely-not-a-program" {
		t.Errorf("resolve = %q, want the name unchanged", got)
	}
}

// An absolute path is what every pinned scanner is invoked by, and rewriting
// one against PATH could only find a different binary of the same name.
func TestResolveLeavesAPathAlone(t *testing.T) {
	want := filepath.Join(string(os.PathSeparator), "usr", "bin", "env")
	if got := resolve(want, []string{"PATH=/nonexistent"}); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}
