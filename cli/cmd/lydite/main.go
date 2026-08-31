package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"lydite/lydite/internal/ui"
)

// version is overridden at release via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	// An interrupt cancels the command's context rather than killing the
	// process where it stands. `lydite test` starts containers and removes
	// them in a deferred teardown, and a signal that skips those defers
	// leaves one stack running per component that had started — holding the
	// host ports the next run has to bind, so the leak surfaces as an
	// unrelated failure one run later.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Unregister as soon as the first signal lands, so the second one gets
	// the default disposition and kills the process. NotifyContext leaves the
	// handler installed and stops reading its channel, so without this a
	// teardown hanging on an unresponsive container daemon cannot be
	// interrupted at all — the impatient second Ctrl-C is swallowed, and the
	// only way out is SIGKILL.
	go func() {
		<-ctx.Done()
		stop()
	}()

	root := newRootCmd()
	if err := root.ExecuteContext(ctx); err != nil {
		// A command that reached a verdict has already printed it, and its
		// last line said what happened. Restating that as "lydite: exit
		// status 2" would report a referral — which asserts nothing is
		// wrong — as a malfunction of the tool.
		var exit ui.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		fmt.Fprintln(os.Stderr, "lydite:", err)
		os.Exit(1)
	}
	maybeNudgeUpdate()
}
