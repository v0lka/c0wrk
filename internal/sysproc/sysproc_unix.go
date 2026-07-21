//go:build !windows

package sysproc

import "os/exec"

// HideConsole is a no-op on non-Windows platforms, where spawning a child
// process never creates a console window. It exists so callers can apply it
// unconditionally without their own platform build tags.
func HideConsole(_ *exec.Cmd) {}
