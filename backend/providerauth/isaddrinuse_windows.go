//go:build windows

package providerauth

import (
	"errors"
	"syscall"
)

// wsaEADDRINUSE is the Winsock address-in-use code (10048) a failed bind
// actually returns on Windows; syscall.EADDRINUSE there is an invented errno
// (APPLICATION_ERROR+2) no OS call ever produces, so matching on it alone
// never fires. The Winsock code is ABI-stable (x/sys/windows WSAEADDRINUSE).
const wsaEADDRINUSE = syscall.Errno(10048)

// isAddrInUse reports whether err is the platform's address-in-use error;
// the invented syscall.EADDRINUSE is accepted too, for Unix parity.
func isAddrInUse(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == wsaEADDRINUSE || errno == syscall.EADDRINUSE
	}
	return false
}
