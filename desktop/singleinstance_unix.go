//go:build darwin || linux

package desktop

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockExclusive takes a non-blocking exclusive flock; only EWOULDBLOCK means
// "held by another instance", every other error is fail-open setup
// (specs/decisions/075-single-instance-lock.md, D3).
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
