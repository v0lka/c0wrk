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
// posh.go is //go:build windows and bash.go is //go:build !windows, so a
// single unconditional constructor call would fail to compile on the other OS.
//
// The blocklist (the user-authored security.groups.execute.blocklist) is
// assembled by the caller in builtin_registration.go and passed through
// unchanged. No platform supplement exists any more: the predefined
// engine-floor patterns died together with the shipped defaults (their
// rationale was cross-dialect safety of a unified default list, which no
// longer exists), so the compiled-in set is exactly what the caller passes
// (empty by default).
func newShellExecTool(blocklist []string, timeouts builtins.BashTimeouts) (tools.Tool, error) {
	return builtins.NewPoshExecToolWithTimeouts(blocklist, timeouts)
}

// ShellExecToolName returns the name of the platform-registered
// shell-execution tool: "posh_exec" on Windows, "bash_exec" on Unix (see
// shelltool_unix.go). Callers that must address the shell tool by name —
// e.g. the verify-on-edit runner's ExecuteUnattended call — use this instead
// of a hardcoded literal so they stay portable across the build-tagged
// registration split (a hardcoded "bash_exec" would fail with
// tool-not-found here, where only posh_exec is registered).
func ShellExecToolName() string {
	return "posh_exec"
}
