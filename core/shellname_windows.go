//go:build windows

package core

// activeShellToolName returns the tool name under which the shell-execution
// tool is registered on this platform. On Windows this is posh_exec; on Unix
// (see shellname_unix.go) it is bash_exec.
//
// This keeps the security-policy lookup in builder.go (blacklist resolution)
// platform-aware: it reads cfg.Security.ToolPolicies for the tool name that
// will actually be registered, so the configured blacklist/confirm policy
// applies to the correct tool on every platform.
func activeShellToolName() string {
	return ToolPoshExec
}
