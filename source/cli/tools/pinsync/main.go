// Command pinsync makes every place that states a tool pin agree with the
// manifest Dependabot edits.
//
// A bump arrives with the manifest changed and the mirror stale, because
// Dependabot edits manifests and nothing else. -check is what fails such a
// change; the default run is what fixes it.
//
//	go run ./tools/pinsync            # write
//	go run ./tools/pinsync -check     # report, exit 1 on drift
//	go run ./tools/pinsync -files     # print every file a mirror reads or writes
//
// The rule itself is internal/pins. This is a way to run it.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"lydite/lydite/internal/pins"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the whole of the command, so that what it prints and what it exits
// with are assertable without building a binary. main does nothing this does
// not.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("pinsync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	check := flags.Bool("check", false, "report drift and exit non-zero, writing nothing")
	files := flags.Bool("files", false, "print every file a mirror reads or writes, one per line")
	root := flags.String("root", ".", "the Go module root to work in")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	// -files is what lets a caller assemble the tree pinsync needs without
	// restating which files that is. dependabot-pins.yml fetches exactly
	// these from a pull request, so a mirror added to internal/pins needs no
	// edit there.
	if *files {
		for _, path := range pins.Files() {
			_, _ = fmt.Fprintln(stdout, path)
		}
		return 0
	}

	apply := pins.Write
	if *check {
		apply = pins.Check
	}
	drifted, err := apply(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "pinsync: %v\n", err)
		return 1
	}
	if len(drifted) == 0 {
		_, _ = fmt.Fprintln(stderr, "pinsync: every mirror states its pin")
		return 0
	}
	for _, d := range drifted {
		_, _ = fmt.Fprintln(stderr, d)
	}
	if *check {
		_, _ = fmt.Fprintln(stderr, "pinsync: run `go run ./tools/pinsync` to write these")
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "pinsync: wrote %d mirror(s)\n", len(drifted))
	return 0
}
