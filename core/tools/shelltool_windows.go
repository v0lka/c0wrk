//go:build windows

package tools

import (
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// newShellExecTool registers posh_exec (bash_exec on Unix); the constructor
// call is build-tag split because sp4rk's posh.go/bash.go are mutually exclusive
// per OS. Blocklist passes through unchanged. See specs/domains/tool-system/builtins.md.
func newShellExecTool(blocklist []string, timeouts builtins.BashTimeouts, bashInvocation, poshInvocation *tools.ShellInvocation) (tools.Tool, error) {
	invocation := tools.DefaultPoshInvocation()
	if poshInvocation != nil {
		invocation = *poshInvocation
	}
	return builtins.NewPoshExecToolWithInvocation(blocklist, timeouts, invocation)
}

// ShellExecToolName names the platform-registered shell tool ("posh_exec"
// here); name-keyed callers (verify-on-edit) go through it, never a hardcoded
// literal, to stay portable across the build-tagged split. See specs/domains/tool-system/builtins.md.
func ShellExecToolName() string {
	return "posh_exec"
}
