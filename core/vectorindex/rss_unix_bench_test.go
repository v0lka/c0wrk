//go:build darwin || linux

package vectorindex

import (
	"runtime"
	"syscall"
)

// benchmarkPeakRSSBytes reports the process high-water RSS (RUSAGE_SELF
// Maxrss). Linux reports KiB, Darwin bytes; unsupported platforms report 0
// via the other-platform sibling file.
func benchmarkPeakRSSBytes() uint64 {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	peak := uint64(usage.Maxrss) //nolint:gosec // Maxrss is non-negative by OS contract.
	if peak == 0 {
		return 0
	}
	if runtime.GOOS == "linux" {
		return peak * 1024
	}
	return peak
}
