//go:build !windows

package smallllm_test

import (
	"testing"

	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// platformShellTool constructs the Unix shell tool (bash_exec) for the
// real-descriptor set: sp4rk's bash.go is //go:build !windows, so the
// constructor cannot be called from the platform-neutral test file (it would
// fail to compile on Windows with "undefined: builtins.NewBashExecTool").
// On Windows the counterpart in compact_descriptions_windows_test.go returns
// posh_exec instead; both names carry a compact one-liner, so the guard
// exercises the real platform shell description on every OS.
func platformShellTool(t *testing.T) tools.Tool {
	t.Helper()

	bash, err := builtins.NewBashExecTool(nil)
	if err != nil {
		t.Fatalf("NewBashExecTool: %v", err)
	}
	return bash
}
