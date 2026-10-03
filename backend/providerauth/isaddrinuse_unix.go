//go:build !windows

package providerauth

import (
	"errors"
	"syscall"
)

// isAddrInUse reports whether err is the platform's address-in-use error
// (EADDRINUSE from a failed bind). See isaddrinuse_windows.go for the
// Windows spelling.
func isAddrInUse(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EADDRINUSE
	}
	return false
}
