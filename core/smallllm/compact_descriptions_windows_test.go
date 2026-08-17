//go:build windows

package smallllm_test

import (
	"testing"

	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// platformShellTool constructs the Windows shell tool (posh_exec) for the
// real-descriptor set: sp4rk's posh.go is //go:build windows, mirroring the
// bash.go split. The compact set carries one-liners for both bash_exec and
// posh_exec, so the descriptor guard stays meaningful on Windows while the
// platform-neutral test file never references a platform-only constructor.
func platformShellTool(t *testing.T) tools.Tool {
	t.Helper()

	posh, err := builtins.NewPoshExecTool(nil)
	if err != nil {
		t.Fatalf("NewPoshExecTool: %v", err)
	}
	return posh
}
