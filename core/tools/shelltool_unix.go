//go:build !windows

package tools

import (
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// newShellExecTool constructs the platform-appropriate shell-execution tool.
// On Unix this is the bash_exec tool; on Windows (see shelltool_windows.go) it
// is the posh_exec tool. Splitting the constructor call behind build tags keeps
// the registration path platform-portable: sp4rk's bash.go is //go:build !windows
// and posh.go is //go:build windows, so a single unconditional constructor call
// would fail to compile on the other OS (e.g. on Windows:
// undefined: builtins.NewBashExecToolWithTimeouts).
//
// The blacklist (config merge + No-Project patterns) is assembled by the caller
// in builtin_registration.go; only the constructor call is platform-specific.
// No platform supplement exists on Unix: the bash half of the unified
// security.groups.execute.blacklist is already Unix-native, and the
// PowerShell alias supplement (shelltool_windows.go) exists precisely so it
// never compiles into bash_exec.
func newShellExecTool(blacklist []string, timeouts builtins.BashTimeouts) (tools.Tool, error) {
	return builtins.NewBashExecToolWithTimeouts(blacklist, timeouts)
}

// ShellExecToolName returns the name of the platform-registered
// shell-execution tool: "bash_exec" on Unix, "posh_exec" on Windows (see
// shelltool_windows.go). Callers that must address the shell tool by name —
// e.g. the verify-on-edit runner's ExecuteUnattended call — use this instead
// of a hardcoded literal so they stay portable across the build-tagged
// registration split (a hardcoded "bash_exec" would fail with
// tool-not-found on Windows, where only posh_exec is registered).
func ShellExecToolName() string {
	return "bash_exec"
}
