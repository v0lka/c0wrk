//go:build windows

package desktop

import "github.com/shirou/gopsutil/v4/mem"

// physicalMemoryBytes returns the machine's total physical RAM via
// gopsutil's mem.VirtualMemory. Used by the AUTO memory-soft-limit mode (see
// memlimit.go).
func physicalMemoryBytes() (int64, bool) {
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return 0, false
	}
	return int64(vm.Total), true
}
