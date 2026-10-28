//go:build !windows

package sysproc

import "os/exec"

// HideConsole is a no-op on non-Windows: children never allocate a console,
// and callers can apply it unconditionally (see the package doc).
func HideConsole(_ *exec.Cmd) {}
