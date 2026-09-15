package executil

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// limitMemory caps the child's data segment, which since Linux 4.7 covers the
// anonymous mappings a Go, Rust or Node heap lives in. RLIMIT_AS is not the
// bound: it counts address space a runtime reserves far beyond what it uses,
// so a limit large enough to let the process start says nothing about what it
// allocated.
//
// The limit is set on the started process rather than on lydite's own fork,
// and is inherited across every fork and exec below it — which is what holds
// `go test`'s per-package binaries and nextest's or vitest's workers to one
// number without lydite knowing the shape of the tree.
func limitMemory(pid int, bytes int64) error {
	// #nosec G115 -- bytes is a positive byte count; runTo applies a limit
	// only when the caller asked for one.
	lim := unix.Rlimit{Cur: uint64(bytes), Max: uint64(bytes)}
	return unix.Prlimit(pid, unix.RLIMIT_DATA, &lim, nil)
}

// peakRSS reads ru_maxrss, which Linux reports in kilobytes.
func peakRSS(usage any) int64 {
	ru, ok := usage.(*syscall.Rusage)
	if !ok {
		return 0
	}
	return ru.Maxrss * 1024
}
