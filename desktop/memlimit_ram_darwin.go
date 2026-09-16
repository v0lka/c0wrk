//go:build darwin

package desktop

import "golang.org/x/sys/unix"

// physicalMemoryBytes returns the machine's total physical RAM via the
// darwin sysctl hw.memsize (bytes). Used by the AUTO memory-soft-limit mode
// (see memlimit.go).
func physicalMemoryBytes() (int64, bool) {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil || v == 0 {
		return 0, false
	}
	return int64(v), true
}
