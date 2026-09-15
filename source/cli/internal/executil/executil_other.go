//go:build !linux && !darwin

package executil

import "errors"

// limitMemory has no implementation off the two platforms lydite ships for,
// and peakRSS no source: ru_maxrss is not a portable field, and an OS whose
// usage lydite has not measured is one whose number would be reported in a
// unit nobody checked.
func limitMemory(int, int64) error {
	return errors.New("memory limits are unimplemented on this platform")
}

func peakRSS(any) int64 { return 0 }
