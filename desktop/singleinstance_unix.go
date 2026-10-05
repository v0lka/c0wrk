//go:build darwin || linux

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockExclusive takes a non-blocking exclusive flock on the file. flock is
// associated with the open file description, so a second OpenFile of the same
// path — in this or any other process — fails with EWOULDBLOCK while the
// first holder's descriptor is open; the kernel releases it on close and on
// every process exit, including SIGKILL. Only EWOULDBLOCK means "held by
// another instance"; every other error is a setup failure the caller treats
// as fail-open (see AcquireSingleInstanceLock).
func tryLockExclusive(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}
