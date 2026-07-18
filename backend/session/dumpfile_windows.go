//go:build windows

package session

import "os"

// dupFile returns an independent handle to the given *os.File.
//
// On Windows the syscall package has no Dup. Because dumpFile is an
// append-only on-disk log opened O_WRONLY|O_APPEND (see manager.go), we must
// re-open the file by path with the SAME write mode so that the returned
// handle remains writable by background goroutines (title generation,
// ToolJudge) that append JSONL records to it. Opening with os.Open (O_RDONLY)
// would yield a read-only handle whose writes fail with ERROR_ACCESS_DENIED.
// Offset semantics are irrelevant for an append-only log. The caller owns the
// returned handle and must close it.
func dupFile(f *os.File) (*os.File, error) {
	return os.OpenFile(f.Name(), os.O_WRONLY|os.O_APPEND, 0)
}
