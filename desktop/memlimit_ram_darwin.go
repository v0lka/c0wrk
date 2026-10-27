//go:build darwin

package desktop

import "golang.org/x/sys/unix"

// physicalMemoryBytes returns total physical RAM via the sysctl hw.memsize, for the
// AUTO memory-soft-limit mode (specs/decisions/049-vector-index-memory-reduction.md).
func physicalMemoryBytes() (int64, bool) {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil || v == 0 {
		return 0, false
	}
	return int64(v), true
}
