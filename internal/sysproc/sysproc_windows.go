//go:build windows

package sysproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the Windows process creation flag CREATE_NO_WINDOW
// (0x08000000). It prevents the child process from allocating a console
// window, which is essential when a GUI-subsystem application spawns helper
// processes (uv, markitdown, git, …) — otherwise each child flashes a console
// window on screen.
//
// Note: Go's syscall.SysProcAttr.HideWindow field is not sufficient here; it
// hides a window that has already been created via ShowWindow(SW_HIDE) and
// still allows a brief flash. CREATE_NO_WINDOW suppresses allocation entirely.
const createNoWindow = 0x08000000

// HideConsole configures cmd so the child process does not allocate a visible
// console window. It preserves any CreationFlags already set by the caller
// (e.g. CREATE_NEW_PROCESS_GROUP) by OR-ing CREATE_NO_WINDOW into them. On
// non-Windows platforms this is a no-op (see sysproc_unix.go).
//
// HideConsole must be called before cmd.Start/Run; mutating SysProcAttr after
// the process has started has no effect.
func HideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
