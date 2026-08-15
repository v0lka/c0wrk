package prompts

import (
	"runtime"
	"strings"
	"testing"
)

// expectedShellToolName returns the shell-execution tool name that should be
// registered on the given GOOS. It mirrors the build-tagged shellToolName()
// so a single test can assert platform-correct behavior without build tags.
func expectedShellToolName(goos string) string {
	if goos == "windows" {
		return "posh_exec"
	}
	return "bash_exec"
}

func TestSubstituteShellTool_ResolvesPlaceholder(t *testing.T) {
	want := expectedShellToolName(runtime.GOOS)
	got := SubstituteShellTool("use {shell_tool} now")
	if got != "use "+want+" now" {
		t.Errorf("SubstituteShellTool = %q, want %q", got, "use "+want+" now")
	}
}

func TestSubstituteShellTool_NoPlaceholderLeak(t *testing.T) {
	out := SubstituteShellTool("a {shell_tool} b {shell_tool} c")
	if strings.Contains(out, ShellToolPlaceholder) {
		t.Errorf("substituted text still contains placeholder %q: %q", ShellToolPlaceholder, out)
	}
}

func TestSubstituteShellTool_LeavesUnrelatedTextIntact(t *testing.T) {
	in := "no placeholder here"
	if out := SubstituteShellTool(in); out != in {
		t.Errorf("SubstituteShellTool modified text without placeholder: in=%q out=%q", in, out)
	}
}

// TestEmbeddedPrompts_ShellToolResolved verifies that SubstituteShellTool, when
// applied to each embedded prompt section that references the shell-execution
// tool, fully resolves the {shell_tool} placeholder to the platform-correct
// tool name with no leak. The exported vars are intentionally kept as raw
// templates (the placeholder is preserved, not baked in at package load), so
// the substitution is explicit at each consumer/assembly call site — see
// core/systemprompt.go. This test asserts the substitution function works
// correctly against the real embedded content.
func TestEmbeddedPrompts_ShellToolResolved(t *testing.T) {
	want := expectedShellToolName(runtime.GOOS)
	sections := map[string]string{
		"OrchestratorSystem":      OrchestratorSystem,
		"OrchestratorPlanContext": OrchestratorPlanContext,
	}
	for name, content := range sections {
		t.Run(name, func(t *testing.T) {
			resolved := SubstituteShellTool(content)
			if strings.Contains(resolved, ShellToolPlaceholder) {
				t.Errorf("SubstituteShellTool(%s) still contains unresolved placeholder %q", name, ShellToolPlaceholder)
			}
			if !strings.Contains(resolved, want) {
				t.Errorf("SubstituteShellTool(%s) does not reference the active shell tool %q", name, want)
			}
		})
	}
}

// TestEmbeddedPrompts_RawTemplatePreserved verifies that the exported prompt
// vars are NOT mutated at package load: sections that reference the
// shell-execution tool still carry the {shell_tool} placeholder in their raw
// form. This makes the template recoverable and keeps the substitution an
// explicit, testable step at each consumer site rather than an import-time
// global side effect (init()).
func TestEmbeddedPrompts_RawTemplatePreserved(t *testing.T) {
	for name, content := range map[string]string{
		"OrchestratorSystem":      OrchestratorSystem,
		"OrchestratorPlanContext": OrchestratorPlanContext,
	} {
		if !strings.Contains(content, ShellToolPlaceholder) {
			t.Errorf("%s lost the raw %q placeholder (template should be preserved, not pre-substituted)", name, ShellToolPlaceholder)
		}
	}
}
