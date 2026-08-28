//go:build windows

package crashlog

import (
	"os"
)

// redirectStdio routes Go-level stdio into the capture file. Windows GUI
// processes have no C-level stderr handle to dup2 onto, so raw runtime panic
// dumps are not capturable in-process on this platform (they surface through
// Windows Error Reporting instead); the liveness marker still detects the
// abnormal exit at the next start.
func redirectStdio(f *os.File) error {
	os.Stdout = f
	os.Stderr = f
	return nil
}

// reraise: the signal line is already written by the caller; Windows has no
// signal-delivery semantics, so terminate with a conventional exit code.
func reraise(_ os.Signal) {
	os.Exit(1)
}
