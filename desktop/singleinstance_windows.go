//go:build windows

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockExclusive takes a non-blocking exclusive LockFileEx byte-range lock (x/sys
// windows — stdlib syscall lacks FAIL_IMMEDIATELY); other-than-held errors fail open
// (specs/decisions/075-single-instance-lock.md, D3).
func tryLockExclusive(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	default:
		return false, err
	}
}
