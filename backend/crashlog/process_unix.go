//go:build darwin || linux

package crashlog

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processAlive reports whether a process with the given pid exists. Signal 0
// performs existence and permission checks only — it never delivers a
// signal. EPERM means the process exists but belongs to another user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
