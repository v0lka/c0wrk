//go:build windows

package toolmanager

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// isTransientSpawnError reports whether a --version probe failure is the
// kernel's transient refusal to spawn a freshly written binary: antivirus
// real-time scanning (and other short-lived holders) keeps the file open
// without FILE_SHARE_EXECUTE, so CreateProcess fails although neither the
// binary nor the caller is at fault. Retrying is safe — the refusal happens
// before any child code runs, so a retry cannot repeat a side effect. A
// permanently unusable binary fails every attempt identically and the final
// error is returned unchanged. This is the Windows sibling of Unix ETXTBSY
// (see probe_transient_other.go); both exist because a CI runner copying an
// executable and executing it microseconds later hits exactly this window.
func isTransientSpawnError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch windows.Errno(errno) {
	case windows.ERROR_ACCESS_DENIED,
		windows.ERROR_SHARING_VIOLATION,
		windows.ERROR_LOCK_VIOLATION:
		return true
	default:
		return false
	}
}
