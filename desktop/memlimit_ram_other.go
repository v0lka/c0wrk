//go:build !darwin && !linux && !windows

package desktop

// physicalMemoryBytes is the fallback for platforms without a RAM probe:
// RAM is reported unknown and the AUTO memory-soft-limit mode clamps to its
// 2 GiB floor (see memlimit.go).
func physicalMemoryBytes() (int64, bool) {
	return 0, false
}
