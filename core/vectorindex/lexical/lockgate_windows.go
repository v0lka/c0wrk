//go:build windows

package lexical

// Windows half of the lock gate: LockFileEx over a one-byte range at offset
// 0. Like POSIX flock, range locks bind to the handle, so a second open of
// the same sentinel — even inside this process — probes against the held
// lock and sees it busy, which is exactly the stuck-holder shape the gate
// refuses. LOCKFILE_FAIL_IMMEDIATELY makes every call a probe; the poll
// loop owns the waiting. The calls live in golang.org/x/sys/windows (the
// same package bbolt itself locks through); the stdlib syscall package
// exports no LockFileEx.

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

// gateLockRangeBytes is the locked range: a single byte at offset 0 keeps
// every opener (exclusive or shared) contending on the same region.
const gateLockRangeBytes = 1

// tryGateLock performs one non-blocking lock attempt of the sentinel file.
// busy is true when another holder owns the gate (ERROR_LOCK_VIOLATION, the
// immediate-failure verdict of a contended range); a real error is any
// other failure.
func tryGateLock(f *os.File, exclusive bool) (busy bool, err error) {
	flags := uint32(lockfileFailImmediately)
	if exclusive {
		flags |= lockfileExclusiveLock
	}
	var ol windows.Overlapped
	if lockErr := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, gateLockRangeBytes, 0, &ol); lockErr != nil {
		if errors.Is(lockErr, windows.ERROR_LOCK_VIOLATION) {
			return true, nil
		}
		return false, fmt.Errorf("LockFileEx: %w", lockErr)
	}
	return false, nil
}

// unlockGate drops the held range. The file is closed by the caller.
func unlockGate(f *os.File) error {
	var ol windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, gateLockRangeBytes, 0, &ol); err != nil {
		return fmt.Errorf("UnlockFileEx: %w", err)
	}
	return nil
}
