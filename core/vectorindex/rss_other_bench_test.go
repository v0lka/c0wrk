//go:build !darwin && !linux

package vectorindex

// benchmarkPeakRSSBytes is unavailable on this platform; the store-protocol
// benchmark reports zero (BENCHMARKS.md: unsupported platforms report zero).
func benchmarkPeakRSSBytes() uint64 { return 0 }
