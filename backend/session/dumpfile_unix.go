//go:build !windows

package session

import (
	"os"
	"syscall"
)

// dupFile returns an independent handle to f via syscall.Dup so background
// appends (title generation, ToolJudge) survive session deletion; caller closes.
func dupFile(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}
