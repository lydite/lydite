// Command pinsync makes every place that states a tool pin agree with the
// manifest Dependabot edits.
//
// A bump arrives with the manifest changed and the mirror stale, because
// Dependabot edits manifests and nothing else. -check is what fails such a
// change; the default run is what fixes it.
//
//	go run ./tools/pinsync            # write
//	go run ./tools/pinsync -check     # report, exit 1 on drift
//
// The rule itself is internal/pins. This is a way to run it.
package main

import (
	"flag"
	"fmt"
	"os"

	"lydite/lydite/internal/pins"
)

func main() {
	check := flag.Bool("check", false, "report drift and exit non-zero, writing nothing")
	files := flag.Bool("files", false, "print every file a mirror reads or writes, one per line")
	root := flag.String("root", ".", "the Go module root to work in")
	flag.Parse()

	// -files is what lets a caller assemble the tree pinsync needs without
	// restating which files that is. dependabot-pins.yml fetches exactly
	// these from a pull request, so a mirror added here needs no edit there.
	if *files {
		for _, path := range pins.Files() {
			fmt.Println(path)
		}
		return
	}

	run := pins.Write
	if *check {
		run = pins.Check
	}
	drifted, err := run(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pinsync: %v\n", err)
		os.Exit(1)
	}
	if len(drifted) == 0 {
		fmt.Fprintln(os.Stderr, "pinsync: every mirror states its pin")
		return
	}
	for _, d := range drifted {
		fmt.Fprintln(os.Stderr, d)
	}
	if *check {
		fmt.Fprintln(os.Stderr, "pinsync: run `go run ./tools/pinsync` to write these")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "pinsync: wrote %d mirror(s)\n", len(drifted))
}
