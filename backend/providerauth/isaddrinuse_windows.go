//go:build windows

package providerauth

import (
	"errors"
	"syscall"
)

// wsaEADDRINUSE is the Winsock address-in-use error code (10048) the OS
// actually returns for a failed bind on Windows. The syscall package's
// EADDRINUSE is an INVENTED errno there (APPLICATION_ERROR+2, see
// syscall/zerrors_windows.go) that no OS call ever produces, so comparing
// against it alone never matches a real busy port. The Winsock code is
// ABI-stable; golang.org/x/sys/windows spells the same value WSAEADDRINUSE.
const wsaEADDRINUSE = syscall.Errno(10048)

// isAddrInUse reports whether err is the platform's address-in-use error.
// On Windows a busy bind surfaces as the Winsock WSAEADDRINUSE code; the
// invented syscall.EADDRINUSE is accepted too (harmless, and keeps parity
// with the Unix implementation if a translation layer ever emits it).
func isAddrInUse(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == wsaEADDRINUSE || errno == syscall.EADDRINUSE
	}
	return false
}
