//go:build !windows

package core

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform. On Unix this is bash_exec; on Windows
// (see shellname_windows.go) it is posh_exec.
//
// It keeps platform-specific references (verifier toolsets, tests) pointing
// at the shell tool that is actually registered on the current OS.
func activeShellToolName() string {
	return ToolBashExec
}
