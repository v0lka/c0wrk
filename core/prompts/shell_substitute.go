package prompts

import "strings"

// ShellToolPlaceholder marks where embedded prompt markdown wants the active
// platform's shell-execution tool name (bash_exec on Unix, posh_exec on
// Windows), so prompt sections never hardcode a platform-specific name.
// See specs/domains/orchestration/conductor.md (SubstituteShellTool).
const ShellToolPlaceholder = "{shell_tool}"

// SubstituteShellTool resolves the {shell_tool} placeholder to the active
// platform's shell-execution tool name. It is the single substitution point,
// applied at each prompt-assembly call site; the embedded prompt vars stay raw
// templates with recoverable placeholders (no init-time baking).
// See specs/domains/orchestration/conductor.md (SubstituteShellTool).
func SubstituteShellTool(text string) string {
	return strings.ReplaceAll(text, ShellToolPlaceholder, shellToolName())
}
