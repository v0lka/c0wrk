// Package sysproc configures process-creation attributes for child processes
// spawned by the GUI application.
//
// On Windows the application is built as a GUI-subsystem binary and has no
// attached console. Any child process started via [os/exec] allocates a fresh
// console window by default, which appears as a flashing terminal window on
// screen (most visible during the lengthy uv/Python bootstrap in the
// tool-manager, the markitdown conversions, and the git invocations that run
// on every startup). HideConsole suppresses that window.
//
// Call HideConsole on every [exec.Cmd] that is not an interactive terminal
// session. The ConPTY-backed terminal manager intentionally keeps its console
// behaviour because it launches an interactive shell through a pseudo-terminal
// ([github.com/creack/pty.Start]), not a plain child process. The function is a
// no-op on non-Windows platforms, so callers do not need their own build tags.
package sysproc
