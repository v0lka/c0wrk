//go:build !windows

package core

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform. On Unix this is bash_exec; on Windows
// (see shellname_windows.go) it is posh_exec.
//
// This keeps the security-policy lookup in builder.go (blacklist resolution)
// platform-aware: it reads cfg.Security.ToolPolicies for the tool name that
// will actually be registered, so the configured blacklist/confirm policy
// applies to the correct tool on every platform.
func activeShellToolName() string {
	return ToolBashExec
}
