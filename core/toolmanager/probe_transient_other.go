//go:build !windows

package toolmanager

import (
	"errors"
	"syscall"
)

// isTransientSpawnError reports whether a --version probe failure is the
// kernel's pre-exec ETXTBSY refusal — the only transient spawn failure on
// Unix. A missing, non-executable or denied binary is a real answer, returned
// unchanged to the caller. The Windows dialect lives in
// probe_transient_windows.go.
func isTransientSpawnError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ETXTBSY
}
