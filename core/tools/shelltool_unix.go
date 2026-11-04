//go:build !windows

package tools

import (
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// newShellExecTool builds the platform shell tool (bash_exec here; posh_exec
// in shelltool_windows.go — sp4rk's bash.go/posh.go are mutually exclusive per
// OS). See specs/domains/tool-system/builtins.md (Shell-Execution Tool).
func newShellExecTool(blocklist []string, timeouts builtins.BashTimeouts, bashInvocation, poshInvocation *tools.ShellInvocation) (tools.Tool, error) {
	invocation := tools.DefaultBashInvocation()
	if bashInvocation != nil {
		invocation = *bashInvocation
	}
	return builtins.NewBashExecToolWithInvocation(blocklist, timeouts, invocation)
}

// ShellExecToolName names the platform-registered shell tool — use it instead
// of a hardcoded literal (specs/domains/tool-system/builtins.md).
func ShellExecToolName() string {
	return "bash_exec"
}
