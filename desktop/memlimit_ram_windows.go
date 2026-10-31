//go:build windows

package desktop

import "github.com/shirou/gopsutil/v4/mem"

// physicalMemoryBytes returns total physical RAM via gopsutil mem.VirtualMemory, for
// the AUTO memory-soft-limit mode (specs/decisions/049-vector-index-memory-reduction.md).
func physicalMemoryBytes() (int64, bool) {
	vm, err := mem.VirtualMemory()
	if err != nil || vm == nil || vm.Total == 0 {
		return 0, false
	}
	return int64(vm.Total), true
}
