//go:build windows

package crashlog

import (
	"errors"
	"math"

	"golang.org/x/sys/windows"
)

// processAlive reports whether a process with the given pid exists.
// PROCESS_QUERY_LIMITED_INFORMATION is the least-privilege handle that
// answers existence; ERROR_ACCESS_DENIED still proves the process exists
// (it runs under another user).
func processAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	_ = windows.CloseHandle(handle)
	return true
}
