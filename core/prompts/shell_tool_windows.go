//go:build windows

package prompts

// shellToolName returns the platform's shell-execution tool name; the literal
// must match core/toolnames.go's ToolPoshExec. Core keeps only a test-only
// equivalent (shellname_*_test.go) — core cannot import this package (core
// already imports core/prompts), and production core code resolves the shell
// tool via its `execute` capability group.
func shellToolName() string {
	return "posh_exec"
}
