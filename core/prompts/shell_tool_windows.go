//go:build windows

package prompts

// shellToolName returns the name of the shell-execution tool registered on
// this platform. On Windows it is posh_exec; on Unix (see shell_tool_unix.go)
// it is bash_exec.
//
// This mirrors core.activeShellToolName() (from step 1) but lives in the
// prompts package because core cannot be imported here without a circular
// dependency (core already imports core/prompts). The literal strings match
// the tool-name constants in core/toolnames.go (ToolBashExec / ToolPoshExec).
//
// SubstituteShellTool uses it to resolve the {shell_tool} placeholder in
// embedded prompt markdown so platform-specific tool-priority guidance
// always references the tool that is actually registered for the current
// platform.
func shellToolName() string {
	return "posh_exec"
}
