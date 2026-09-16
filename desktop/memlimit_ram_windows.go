//go:build windows

package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// physicalMemoryBytes returns the machine's total physical RAM via
// GlobalMemoryStatusEx. Used by the AUTO memory-soft-limit mode (see
// memlimit.go).
func physicalMemoryBytes() (int64, bool) {
	var ms windows.MemoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if err := windows.GlobalMemoryStatusEx(&ms); err != nil || ms.TotalPhys == 0 {
		return 0, false
	}
	return int64(ms.TotalPhys), true
}
