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
