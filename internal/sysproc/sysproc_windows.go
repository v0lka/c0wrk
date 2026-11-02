//go:build windows

package sysproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows process creation flag CREATE_NO_WINDOW
// (0x08000000): no console is allocated at all. SysProcAttr.HideWindow is
// NOT equivalent — it hides an already-created window and still allows a
// brief flash.
const createNoWindow = 0x08000000

// HideConsole configures cmd so the child process does not allocate a visible
// console window. Any CreationFlags the caller already set are preserved by
// OR-ing; on non-Windows this is a no-op (see sysproc_unix.go).
func HideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
