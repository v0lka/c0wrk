//go:build darwin || linux

package crashlog

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

// redirectStdio duplicates the capture file's descriptor onto fd 1 and fd 2.
// dup2 (not just assigning the os.Stdout/os.Stderr variables) is required
// because the Go runtime writes panic and fatal-error dumps with raw write(2)
// syscalls on fd 2, and native libraries (ONNX, WebKit) do the same — none of
// them consult the Go-level variables.
func redirectStdio(f *os.File) error {
	fd := int(f.Fd())
	if err := unix.Dup2(fd, 1); err != nil {
		return err
	}
	return unix.Dup2(fd, 2)
}

// reraise restores the signal's default disposition and re-delivers it to
// this process so termination keeps its true cause (exit-by-signal, visible
// to launchd/parent processes) instead of a plain os.Exit.
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
