//go:build windows

package session

import (
	"os"

	"github.com/v0lka/sp4rk/safeio"
)

// dupFile returns an independent handle to f. Windows has no syscall.Dup, so
// the file re-opens by path with dumpFile's write mode (O_WRONLY|O_APPEND,
// manager.go); os.Open's read-only handle fails the background appends with
// ERROR_ACCESS_DENIED. Offset semantics are irrelevant; the caller closes.
func dupFile(f *os.File) (*os.File, error) {
	return safeio.OpenFile(f.Name(), os.O_WRONLY|os.O_APPEND, 0)
}
