//go:build unix

package lexical

// POSIX half of the lock gate: flock(2) with a non-blocking probe. flock
// locks belong to the open file description, so a second open of the same
// sentinel — even inside this process — probes against the held lock and
// sees it busy, which is exactly the stuck-holder shape the gate refuses.

import (
	"errors"
	"os"
	"syscall"
)

// tryGateLock performs one non-blocking lock attempt of the sentinel file.
// busy is true when another holder owns the gate (the caller keeps polling
// until its deadline); a real error is any other lock failure. EINTR is
// folded into busy: an interrupted probe is retried by the poll loop, never
// reported as damage.
func tryGateLock(f *os.File, exclusive bool) (busy bool, err error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	lockErr := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
	if lockErr == nil {
		return false, nil
	}
	if errors.Is(lockErr, syscall.EWOULDBLOCK) ||
		errors.Is(lockErr, syscall.EAGAIN) ||
		errors.Is(lockErr, syscall.EINTR) {
		return true, nil
	}
	return false, lockErr
}

// unlockGate drops the held lock. The file is closed by the caller.
func unlockGate(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
