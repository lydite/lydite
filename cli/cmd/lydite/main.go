package main

import (
	"errors"
	"fmt"
	"os"

	"lydite/lydite/internal/ui"
)

// version is overridden at release via -ldflags "-X main.version=<tag>".
var version = "dev"

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
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
