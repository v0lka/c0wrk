//go:build !darwin && !linux && !windows

package desktop

// physicalMemoryBytes has no probe on this platform: RAM reports unknown, so the
// AUTO memory-soft-limit clamps to its 2 GiB floor
// (specs/decisions/049-vector-index-memory-reduction.md).
func physicalMemoryBytes() (int64, bool) {
	return 0, false
}
