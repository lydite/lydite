package executil

import (
	"bytes"
	"context"
	"os"
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
