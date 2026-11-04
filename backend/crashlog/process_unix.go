//go:build darwin || linux

package crashlog

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processAlive reports whether a pid exists: signal 0 delivers nothing and
// EPERM still proves existence (another user's process).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
