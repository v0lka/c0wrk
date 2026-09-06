//go:build unix

package workspace

import (
	"fmt"
	"os"
	"syscall"
)

// openRegularFile opens path for reading without ever blocking and without
// a Stat→Open window (review [14]): the open itself is non-blocking, and
// regularity is verified by fstat on the ALREADY-OPEN descriptor — what is
// read is exactly what was checked. A FIFO planted as .git/config (or
// swapped in between a stat and an open by a racing local adversary, which
// the previous Stat→Open pair left as a TOCTOU) is refused by the fstat
// instead of hanging the synchronous intake scan; a read-only O_NONBLOCK
// open of a FIFO returns immediately, so no writer is ever awaited.
func openRegularFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.NewFile(uintptr(fd), path), nil
}
