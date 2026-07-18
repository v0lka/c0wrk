package prompts

import "strings"

// ShellToolPlaceholder is the stable placeholder used in embedded prompt
// markdown to mark where the active platform's shell-execution tool name
// (bash_exec on Unix, posh_exec on Windows) should appear. Prompt sections
// reference the shell tool via this placeholder rather than hardcoding a
// platform-specific name, so the assembled prompt always points at the tool
// that is actually registered on the current platform.
const ShellToolPlaceholder = "{shell_tool}"

// SubstituteShellTool resolves the {shell_tool} placeholder in text to the
// active platform's shell-execution tool name. It is the single substitution
// point for shell-tool references in prompt data and is applied at each
// prompt-assembly call site (see core/systemprompt.go), so consumers see the
// platform-correct name. The embedded prompt vars themselves are kept as raw
// templates — the {shell_tool} placeholder remains recoverable rather than
// being baked in at package load via init(), which avoids an import-time
// global mutation and init-ordering dependency. On Unix the placeholder
// becomes bash_exec (behavior unchanged); on Windows it becomes posh_exec.
func SubstituteShellTool(text string) string {
	return strings.ReplaceAll(text, ShellToolPlaceholder, shellToolName())
}
