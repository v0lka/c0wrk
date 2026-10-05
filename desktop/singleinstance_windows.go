//go:build windows

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockExclusive takes a non-blocking exclusive byte-range lock on the
// first byte of the file via LockFileEx (the extended LockFileEx lives in
// x/sys/windows; the stdlib syscall package exposes only the blocking-free
// LockFile without FAIL_IMMEDIATELY semantics). The lock is associated with
// the file handle, so a second OpenFile of the same path — in this or any
// other process — fails with ERROR_LOCK_VIOLATION while the first holder's
// handle is open; the kernel releases it on CloseHandle and on every process
// exit. Only ERROR_LOCK_VIOLATION means "held by another instance"; every
// other error is a setup failure the caller treats as fail-open (see
// AcquireSingleInstanceLock).
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
