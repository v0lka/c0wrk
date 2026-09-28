//go:build windows

package embeddedllm

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isTransientRenameError reports whether err is one of the Windows sharing
// errors that make a rename over an existing target fail for a moment even
// though neither file is permanently locked: a concurrent
// MoveFileEx(REPLACE_EXISTING) replacing the same target holds the
// destination while it swaps (the overlap two concurrent writeManifest
// callers produce), and a real-time scanner holds a freshly written
// temporary. Such a failure is worth a bounded retry — see promoteManifest.
func isTransientRenameError(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case windows.ERROR_ACCESS_DENIED,
		windows.ERROR_SHARING_VIOLATION,
		windows.ERROR_LOCK_VIOLATION:
		return true
	default:
		return false
	}
}
