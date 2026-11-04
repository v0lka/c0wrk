// Package sysproc configures process-creation attributes for child processes
// spawned by the GUI application.
//
// On Windows the app is a GUI-subsystem binary: every [os/exec] child would
// allocate a fresh console window and flash it on screen (the uv bootstrap,
// markitdown conversions, the git invocations on every startup). Call
// [HideConsole] on every [exec.Cmd] that is not an interactive terminal
// session — the ConPTY-backed terminal manager deliberately keeps console
// behaviour, launching its shell through a pseudo-terminal. HideConsole is a
// no-op on non-Windows platforms, so callers need no build tags.
package sysproc
