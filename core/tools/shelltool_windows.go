//go:build windows

package tools

import (
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// newShellExecTool constructs the platform-appropriate shell-execution tool.
// On Windows this is the posh_exec tool (PowerShell); on Unix (see
// shelltool_unix.go) it is the bash_exec tool. Splitting the constructor call
// behind build tags keeps the registration path platform-portable: sp4rk's
// posh.go is //go:build windows and bash.go is //go:build !windows, so a single
// unconditional constructor call would fail to compile on the other OS.
//
// The blacklist (config merge + No-Project patterns) is assembled by the caller
// in builtin_registration.go; only the constructor call is platform-specific.
func newShellExecTool(blacklist []string, timeouts builtins.BashTimeouts) (tools.Tool, error) {
	return builtins.NewPoshExecToolWithTimeouts(blacklist, timeouts)
}
