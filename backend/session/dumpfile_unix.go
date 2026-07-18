//go:build !windows

package session

import (
	"os"
	"syscall"
)

// dupFile returns an independent handle to the given *os.File.
//
// On Unix this duplicates the underlying file descriptor via syscall.Dup so
// that background goroutines (title generation, ToolJudge) keep a working
// handle that survives session deletion. The caller owns the returned handle
// and must close it.
func dupFile(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}
