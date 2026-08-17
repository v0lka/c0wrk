//go:build windows

package tools

import (
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// windowsShellBlacklistSupplement are the PowerShell-only destructive-delete
// patterns that CANNOT live in the unified security.groups.execute.blacklist:
// that list is compiled into bash_exec on Unix too, and its Remove-Item alias
// tokens collide with ordinary Unix commands (`rm -r -f <dir>` is the routine
// separate-flags spelling of an in-workspace `rm -rf <dir>`; `ri`/`rmdir`/
// `del` appear in benign Unix compounds like `grep -ri x . && rm -r -f dist`).
// On Windows the aliases are real destructive vocabulary (pwsh resolves `rm`,
// `del`, `ri`, `rd`, `rmdir`, `erase` to Remove-Item, whose `-r\w*`/`-f\w*`
// also cover the -Recurse/-Force parameter spellings), so they are appended
// here on top of whatever the configurable group blacklist contains. This is
// an engine-level security floor like the group policy itself: it applies to
// every registration (initial build and runtime UpdateShellTool edits) and is
// not removable through the settings UI. See ADR-024 and
// backend/config.TestDefaultExecuteGroupBlacklist_CrossDialectSafe for the
// unified-list counterpart of this contract.
var windowsShellBlacklistSupplement = []string{
	`(?i)\b(rm|del|erase|ri|rd|rmdir)\b.*-r\w*.*-f\w*`,
	`(?i)\b(rm|del|erase|ri|rd|rmdir)\b.*-f\w*.*-r\w*`,
}

// newShellExecTool constructs the platform-appropriate shell-execution tool.
// On Windows this is the posh_exec tool (PowerShell); on Unix (see
// shelltool_unix.go) it is the bash_exec tool. Splitting the constructor call
// behind build tags keeps the registration path platform-portable: sp4rk's
// posh.go is //go:build windows and bash.go is //go:build !windows, so a
// single unconditional constructor call would fail to compile on the other OS.
//
// The blacklist (config merge + No-Project patterns) is assembled by the caller
// in builtin_registration.go; only the constructor call is platform-specific.
// On Windows the platform-only alias supplement is appended — a fresh slice,
// so the caller's list is never mutated.
func newShellExecTool(blacklist []string, timeouts builtins.BashTimeouts) (tools.Tool, error) {
	combined := make([]string, 0, len(blacklist)+len(windowsShellBlacklistSupplement))
	combined = append(combined, blacklist...)
	combined = append(combined, windowsShellBlacklistSupplement...)
	return builtins.NewPoshExecToolWithTimeouts(combined, timeouts)
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
