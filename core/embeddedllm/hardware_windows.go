//go:build windows

package embeddedllm

import (
	"context"
	"fmt"
	"syscall"
	"unsafe"
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX structure. Field order and
// types matter: the two leading uint32 fields occupy the first 8 bytes, after
// which every uint64 field is naturally 8-byte aligned, matching the C layout
// the API expects.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// platformTotalRAMBytes reads total physical RAM through GlobalMemoryStatusEx,
// the same source the fork's PowerShell demo scripts use. It reports the total
// installed physical memory, not what is currently free — resolution sizes the
// context tier from the machine's capacity, exactly like the demo's
// bonsai_ctx_default does on the other platforms.
func platformTotalRAMBytes(_ context.Context) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))

	ret, _, callErr := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, fmt.Errorf("GlobalMemoryStatusEx: %w", callErr)
	}
	return status.totalPhys, nil
}
