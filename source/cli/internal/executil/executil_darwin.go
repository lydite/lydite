package executil

import (
	"errors"
	"syscall"
)

// limitMemory has no implementation on Darwin: setrlimit there refuses
// RLIMIT_DATA and RLIMIT_AS alike with EINVAL, at every value, so a memory
// bound asked for on a Mac is a bound nothing applies. Reporting that back is
// the whole point of the error — a caller that cannot tell renders the green
// of a bound that held.
func limitMemory(int, int64) error {
	return errors.New("setrlimit refuses RLIMIT_DATA on darwin")
}

// peakRSS reads ru_maxrss, which Darwin reports in bytes.
func peakRSS(usage any) int64 {
	ru, ok := usage.(*syscall.Rusage)
	if !ok {
		return 0
	}
	return ru.Maxrss
}
