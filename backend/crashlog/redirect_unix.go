//go:build darwin || linux

package crashlog

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

// redirectStdio dup2's the capture file's descriptor onto fd 1 and fd 2 —
// required because runtime panic dumps and native libraries write with raw
// write(2) on fd 2 and never consult the Go-level variables.
// specs/domains/crash-logging.md.
func redirectStdio(f *os.File) error {
	fd := int(f.Fd())
	if err := unix.Dup2(fd, 1); err != nil {
		return err
	}
	return unix.Dup2(fd, 2)
}

// reraise restores the signal's default disposition and re-delivers it, so
// termination keeps its true exit-by-signal cause, not a plain os.Exit.
func reraise(sig os.Signal) {
	s, ok := sig.(unix.Signal)
	if !ok {
		os.Exit(1)
	}
	signal.Reset(s)
	_ = unix.Kill(os.Getpid(), s)
	// The re-delivered signal terminates the process; block forever as a
	// failsafe in case a custom disposition intercepts it.
	select {}
}
