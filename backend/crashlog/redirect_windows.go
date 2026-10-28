//go:build windows

package crashlog

import (
	"os"
)

// redirectStdio routes Go-level stdio into the capture file; Windows GUI
// processes have no C-level stderr to dup2 onto, so raw panic dumps are not
// capturable in-process and the marker carries the abnormal-exit evidence.
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
