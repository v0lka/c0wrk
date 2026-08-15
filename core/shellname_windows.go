//go:build windows

package core

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform. On Windows this is posh_exec; on Unix
// (see shellname_unix.go) it is bash_exec.
//
// It keeps platform-specific references (verifier toolsets, tests) pointing
// at the shell tool that is actually registered on the current OS.
func activeShellToolName() string {
	return ToolPoshExec
}
